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
var validQuality = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_-]*$`)

type channelConfig struct {
	login   string
	quality string
}

type config struct {
	botLogin     string
	token        string
	telegramBot  string
	telegramChat string
	channels     []channelConfig
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
	envHash, err := hashEnvFile(".env")
	if err != nil {
		return fmt.Errorf("read .env: %w", err)
	}
	if err := loadEnv(".env"); err != nil {
		return err
	}
	config, err := readConfig()
	if err != nil {
		return err
	}
	config.outputDir, err = filepath.Abs(config.outputDir)
	if err != nil {
		return fmt.Errorf("resolve output directory: %w", err)
	}
	if err := os.MkdirAll(config.outputDir, 0755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}
	if _, err := diskFreeBytes(config.outputDir); err != nil {
		return fmt.Errorf("check output directory disk space: %w", err)
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
	log.Printf("Monitoring %d channels every %s; recordings go to %s", len(config.channels), config.pollInterval, config.outputDir)
	recordings := newRecordingState()
	var monitors sync.WaitGroup
	monitors.Add(1)
	go func() {
		defer monitors.Done()
		monitorDisk(ctx, client, config)
	}()
	for _, channel := range config.channels {
		monitors.Add(1)
		go func(channel channelConfig) {
			defer monitors.Done()
			monitorChannel(ctx, client, clientID, config, channel, recordings)
		}(channel)
	}
	envTicker := time.NewTicker(5 * time.Second)
	defer envTicker.Stop()
	validationTicker := time.NewTicker(time.Hour)
	defer validationTicker.Stop()
	var candidateHash [32]byte
	hasCandidate := false
	for {
		select {
		case <-ctx.Done():
			monitors.Wait()
			return nil
		case <-recordings.restartReady:
			log.Print(".env changed and no recordings are active; restarting through PM2")
			stop()
			monitors.Wait()
			return errors.New("restart requested after .env change")
		case <-envTicker.C:
			currentHash, err := hashEnvFile(".env")
			if err != nil {
				log.Printf("Cannot check .env for changes: %v", err)
				continue
			}
			if currentHash == envHash {
				hasCandidate = false
				continue
			}
			if !hasCandidate || currentHash != candidateHash {
				candidateHash = currentHash
				hasCandidate = true
				continue
			}
			active := recordings.requestRestart()
			log.Printf(".env changed; waiting for %d active recording(s) before restarting", active)
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

func monitorChannel(ctx context.Context, client *http.Client, clientID string, config config, channel channelConfig, recordings *recordingState) {
	log.Printf("Monitoring %s at %s", channel.login, channel.quality)
	for ctx.Err() == nil {
		streamID, isLive, err := getStream(ctx, client, config.token, clientID, channel.login)
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("Live check failed for %s: %v", channel.login, err)
			}
		} else if isLive {
			output, err := nextRecordingPath(config.outputDir, channel.login, time.Now())
			if err != nil {
				log.Printf("Cannot choose recording filename for %s: %v", channel.login, err)
			} else if recordings.begin() {
				log.Printf("%s stream %s is live; recording %s to %s", channel.login, streamID, channel.quality, output)
				command := exec.CommandContext(ctx, config.streamlink, "--output", output, "https://www.twitch.tv/"+channel.login, channel.quality)
				command.Stdout = os.Stdout
				command.Stderr = os.Stderr
				if err := command.Run(); err != nil && ctx.Err() == nil {
					log.Printf("Streamlink exited for %s: %v", channel.login, err)
				}
				recordings.end()
				log.Printf("Recording stopped for %s stream %s", channel.login, streamID)
			}
		}

		select {
		case <-ctx.Done():
		case <-time.After(config.pollInterval):
		}
	}
}

func nextRecordingPath(outputDir, login string, start time.Time) (string, error) {
	base := filepath.Join(outputDir, login+"-"+start.Format("2006-01-02"))
	for number := 1; ; number++ {
		path := base + ".ts"
		if number > 1 {
			path = fmt.Sprintf("%s-%d.ts", base, number)
		}
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			return path, nil
		} else if err != nil {
			return "", err
		}
	}
}

func readConfig() (config, error) {
	config := config{
		botLogin:     strings.TrimSpace(os.Getenv("BOT_LOGIN")),
		token:        strings.TrimPrefix(strings.TrimSpace(os.Getenv("BOT_OAUTH")), "oauth:"),
		telegramBot:  strings.TrimPrefix(strings.TrimSpace(os.Getenv("DEVELOPER_TELEGRAM_BOT_TOKEN")), "bot"),
		telegramChat: strings.TrimSpace(os.Getenv("DEVELOPER_TELEGRAM_CHAT_ID")),
		outputDir:    strings.TrimSpace(os.Getenv("OUTPUT_DIR")),
		pollInterval: 30 * time.Second,
	}
	if config.botLogin == "" || config.token == "" {
		return config, errors.New("BOT_LOGIN and BOT_OAUTH are required in .env")
	}
	if config.telegramBot == "" || config.telegramChat == "" {
		return config, errors.New("DEVELOPER_TELEGRAM_BOT_TOKEN and DEVELOPER_TELEGRAM_CHAT_ID are required for disk alerts")
	}
	if !validTelegramToken.MatchString(config.telegramBot) {
		return config, errors.New("DEVELOPER_TELEGRAM_BOT_TOKEN has an invalid format")
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

func parseChannels(value string) ([]channelConfig, error) {
	var channels []channelConfig
	seen := make(map[string]string)
	for _, part := range strings.Split(value, ",") {
		login, quality, hasQuality := strings.Cut(strings.TrimSpace(part), ":")
		login = strings.ToLower(strings.TrimSpace(login))
		if !validLogin.MatchString(login) {
			return nil, fmt.Errorf("invalid Twitch login %q in CHANNEL_LOGINS", part)
		}
		if !hasQuality {
			quality = "audio_only"
		}
		quality = strings.TrimSpace(quality)
		if !validQuality.MatchString(quality) {
			return nil, fmt.Errorf("invalid Streamlink quality %q for %s", quality, login)
		}
		if previous, exists := seen[login]; exists {
			if previous != quality {
				return nil, fmt.Errorf("conflicting qualities for %s: %s and %s", login, previous, quality)
			}
			continue
		}
		seen[login] = quality
		channels = append(channels, channelConfig{login: login, quality: quality})
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
