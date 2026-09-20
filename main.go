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
	"syscall"
	"time"
)

var validLogin = regexp.MustCompile(`^[A-Za-z0-9_]{1,25}$`)

type config struct {
	botLogin     string
	token        string
	channel      string
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
	log.Printf("Monitoring %s every %s; recordings go to %s", config.channel, config.pollInterval, config.outputDir)
	lastValidation := time.Now()

	for ctx.Err() == nil {
		if time.Since(lastValidation) >= time.Hour {
			newID, newLogin, err := validateToken(ctx, client, config.token)
			if err != nil {
				log.Printf("Token validation failed: %v", err)
			} else if !strings.EqualFold(newLogin, config.botLogin) {
				log.Printf("Token login changed to %q; waiting for correct credentials", newLogin)
			} else {
				clientID = newID
				lastValidation = time.Now()
			}
		}

		streamID, isLive, err := getStream(ctx, client, config.token, clientID, config.channel)
		if err != nil {
			log.Printf("Live check failed: %v", err)
		} else if isLive {
			filename := fmt.Sprintf("%s-%s-%s.ts", config.channel, streamID, time.Now().UTC().Format("20060102-150405.000"))
			output := filepath.Join(config.outputDir, filename)
			log.Printf("Stream %s is live; recording to %s", streamID, output)
			command := exec.CommandContext(ctx, config.streamlink, "--output", output, "https://www.twitch.tv/"+config.channel, "audio_only")
			command.Stdout = os.Stdout
			command.Stderr = os.Stderr
			if err := command.Run(); err != nil && ctx.Err() == nil {
				log.Printf("Streamlink exited: %v", err)
			}
			log.Printf("Recording stopped for stream %s", streamID)
		}

		select {
		case <-ctx.Done():
		case <-time.After(config.pollInterval):
		}
	}
	return nil
}

func readConfig() (config, error) {
	config := config{
		botLogin:     strings.TrimSpace(os.Getenv("BOT_LOGIN")),
		token:        strings.TrimPrefix(strings.TrimSpace(os.Getenv("BOT_OAUTH")), "oauth:"),
		channel:      strings.TrimSpace(os.Getenv("CHANNEL_LOGIN")),
		outputDir:    strings.TrimSpace(os.Getenv("OUTPUT_DIR")),
		pollInterval: 30 * time.Second,
	}
	if config.botLogin == "" || config.token == "" {
		return config, errors.New("BOT_LOGIN and BOT_OAUTH are required in .env")
	}
	if config.channel == "" {
		config.channel = "n_y_x_official"
	}
	if !validLogin.MatchString(config.channel) {
		return config, errors.New("CHANNEL_LOGIN must be a Twitch login name")
	}
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
