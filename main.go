package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

var validLogin = regexp.MustCompile(`^[A-Za-z0-9_]{1,25}$`)

type config struct {
	botLogin     string
	token        string
	channels     []string
	outputDir    string
	pollInterval time.Duration
	streamlink   string
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	if err := loadEnv(".env"); err != nil {
		return err
	}
	config, err := readConfig()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(config.outputDir, 0755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	client := &http.Client{Timeout: 15 * time.Second}
	clientID, login, err := validateToken(ctx, client, config.token)
	if err != nil {
		return fmt.Errorf("validate BOT_OAUTH: %w", err)
	}
	if !strings.EqualFold(login, config.botLogin) {
		return fmt.Errorf("BOT_OAUTH belongs to %q, not BOT_LOGIN %q", login, config.botLogin)
	}
	log.Printf("Monitoring %s every %s; recordings go to %s", strings.Join(config.channels, ", "), config.pollInterval, config.outputDir)
	var monitors sync.WaitGroup
	for _, channel := range config.channels {
		monitors.Add(1)
		go func() {
			defer monitors.Done()
			monitorChannel(ctx, client, clientID, config, channel)
		}()
	}
	validationTicker := time.NewTicker(time.Hour)
	defer validationTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			monitors.Wait()
			return nil
		case <-validationTicker.C:
			_, validatedLogin, err := validateToken(ctx, client, config.token)
			if err != nil {
				log.Printf("Token validation failed: %v", err)
			} else if !strings.EqualFold(validatedLogin, config.botLogin) {
				log.Printf("Token login changed to %q; update BOT_OAUTH", validatedLogin)
			}
		}
	}
}

func monitorChannel(ctx context.Context, client *http.Client, clientID string, config config, channel string) {
	for ctx.Err() == nil {
		streamID, isLive, err := getStream(ctx, client, config.token, clientID, channel)
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("Live check failed for %s: %v", channel, err)
			}
		} else if isLive {
			filename := fmt.Sprintf("%s-%s-%s.ts", channel, streamID, time.Now().UTC().Format("20060102-150405.000"))
			output := filepath.Join(config.outputDir, filename)
			log.Printf("%s stream %s is live; recording to %s", channel, streamID, output)
			command := exec.CommandContext(ctx, config.streamlink, "--output", output, "https://www.twitch.tv/"+channel, "audio_only")
			command.Stdout = os.Stdout
			command.Stderr = os.Stderr
			if err := command.Run(); err != nil && ctx.Err() == nil {
				log.Printf("Streamlink exited for %s: %v", channel, err)
			}
			log.Printf("Recording stopped for %s stream %s", channel, streamID)
		}

		select {
		case <-ctx.Done():
		case <-time.After(config.pollInterval):
		}
	}
}

func readConfig() (config, error) {
	config := config{
		botLogin:     strings.TrimSpace(os.Getenv("BOT_LOGIN")),
		token:        strings.TrimPrefix(strings.TrimSpace(os.Getenv("BOT_OAUTH")), "oauth:"),
		outputDir:    strings.TrimSpace(os.Getenv("OUTPUT_DIR")),
		pollInterval: 30 * time.Second,
	}
	if config.botLogin == "" || config.token == "" {
		return config, errors.New("BOT_LOGIN and BOT_OAUTH are required in .env")
	}
	channelList := strings.TrimSpace(os.Getenv("CHANNEL_LOGINS"))
	if channelList == "" {
		channelList = strings.TrimSpace(os.Getenv("CHANNEL_LOGIN"))
	}
	if channelList == "" {
		channelList = "n_y_x_official"
	}
	channels, err := parseChannels(channelList)
	if err != nil {
		return config, err
	}
	config.channels = channels
	if config.outputDir == "" {
		config.outputDir = "recordings"
	}
	if value := os.Getenv("POLL_SECONDS"); value != "" {
		seconds, err := strconv.Atoi(value)
		if err != nil || seconds < 10 {
			return config, errors.New("POLL_SECONDS must be an integer of at least 10")
		}
		config.pollInterval = time.Duration(seconds) * time.Second
	}
	streamlink, err := exec.LookPath("streamlink")
	if err != nil {
		return config, errors.New("Streamlink is not installed or not on PATH; install it before starting this app")
	}
	config.streamlink = streamlink
	return config, nil
}

func parseChannels(value string) ([]string, error) {
	var channels []string
	seen := make(map[string]bool)
	for _, part := range strings.Split(value, ",") {
		channel := strings.ToLower(strings.TrimSpace(part))
		if !validLogin.MatchString(channel) {
			return nil, fmt.Errorf("invalid Twitch login %q in CHANNEL_LOGINS", part)
		}
		if !seen[channel] {
			seen[channel] = true
			channels = append(channels, channel)
		}
	}
	return channels, nil
}

func loadEnv(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return fmt.Errorf("invalid .env line: expected KEY=VALUE")
		}
		key = strings.TrimSpace(key)
		if _, exists := os.LookupEnv(key); !exists {
			if err := os.Setenv(key, strings.Trim(strings.TrimSpace(value), `"'`)); err != nil {
				return fmt.Errorf("invalid .env key %q: %w", key, err)
			}
		}
	}
	return scanner.Err()
}

func validateToken(ctx context.Context, client *http.Client, token string) (string, string, error) {
	var result struct {
		ClientID string `json:"client_id"`
		Login    string `json:"login"`
	}
	if err := getJSON(ctx, client, "https://id.twitch.tv/oauth2/validate", map[string]string{"Authorization": "Bearer " + token}, &result); err != nil {
		return "", "", err
	}
	if result.ClientID == "" || result.Login == "" {
		return "", "", errors.New("token response lacks client ID or login")
	}
	return result.ClientID, result.Login, nil
}

func getStream(ctx context.Context, client *http.Client, token, clientID, channel string) (string, bool, error) {
	var result struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	endpoint := "https://api.twitch.tv/helix/streams?user_login=" + url.QueryEscape(channel)
	if err := getJSON(ctx, client, endpoint, map[string]string{
		"Authorization": "Bearer " + token,
		"Client-Id":     clientID,
	}, &result); err != nil {
		return "", false, err
	}
	if len(result.Data) == 0 {
		return "", false, nil
	}
	return result.Data[0].ID, true, nil
}

func getJSON(ctx context.Context, client *http.Client, endpoint string, headers map[string]string, target any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("Twitch returned HTTP %d", response.StatusCode)
	}
	return json.NewDecoder(response.Body).Decode(target)
}
