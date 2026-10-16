package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

const lowDiskThresholdBytes uint64 = 1_000_000_000
const minimumRecordingFreeBytes uint64 = 500_000_000
const diskCheckInterval = time.Minute

var validTelegramToken = regexp.MustCompile(`^[0-9]+:[A-Za-z0-9_-]+$`)

type diskAlert struct {
	outputDir       string
	host            string
	alerted         bool
	criticalAlerted bool
	notify          func(context.Context, string) error
}

func (alert *diskAlert) check(ctx context.Context, freeBytes uint64) error {
	if freeBytes >= minimumRecordingFreeBytes {
		alert.criticalAlerted = false
	}
	if freeBytes >= lowDiskThresholdBytes {
		alert.alerted = false
		return nil
	}
	critical := freeBytes < minimumRecordingFreeBytes
	if alert.alerted && (!critical || alert.criticalAlerted) {
		return nil
	}
	message := fmt.Sprintf("⚠️ Twitch recorder: low disk space on %s. %d MB free on the filesystem containing %s (below 1 GB).", alert.host, freeBytes/1_000_000, alert.outputDir)
	if critical {
		message = fmt.Sprintf("🚨 Twitch recorder on %s: only %d MB free on the filesystem containing %s. New recordings are blocked below 500 MB and will resume automatically when at least 500 MB is available. Existing recordings are not stopped.", alert.host, freeBytes/1_000_000, alert.outputDir)
	}
	if err := alert.notify(ctx, message); err != nil {
		return err
	}
	alert.alerted = true
	if critical {
		alert.criticalAlerted = true
	}
	return nil
}

func recordingSpaceError(freeBytes uint64, err error) error {
	if err != nil {
		return fmt.Errorf("cannot check free disk space; new recording skipped: %w", err)
	}
	if freeBytes < minimumRecordingFreeBytes {
		return fmt.Errorf("only %d bytes free; new recordings require at least 500 MB", freeBytes)
	}
	return nil
}

func monitorDisk(ctx context.Context, client *http.Client, config config) {
	host, err := os.Hostname()
	if err != nil {
		host = "unknown host"
	}
	alert := diskAlert{
		outputDir: config.outputDir,
		host:      host,
		notify: func(ctx context.Context, message string) error {
			return sendTelegram(ctx, client, config.telegramBot, config.telegramChat, message)
		},
	}
	ticker := time.NewTicker(diskCheckInterval)
	defer ticker.Stop()
	for {
		freeBytes, err := diskFreeBytes(config.outputDir)
		if err != nil {
			log.Printf("Disk space check failed for %s: %v", config.outputDir, err)
		} else {
			wasAlerted := alert.alerted
			wasCriticalAlerted := alert.criticalAlerted
			if err := alert.check(ctx, freeBytes); err != nil && ctx.Err() == nil {
				log.Printf("Telegram disk alert failed: %v", err)
			} else if !wasCriticalAlerted && alert.criticalAlerted {
				log.Printf("Telegram critical disk alert sent; new recordings blocked: %d bytes available", freeBytes)
			} else if !wasAlerted && alert.alerted {
				log.Printf("Telegram low disk alert sent: %d bytes available", freeBytes)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func sendTelegram(ctx context.Context, client *http.Client, token, chatID, message string) error {
	if !validTelegramToken.MatchString(token) {
		return errors.New("invalid Telegram bot token format")
	}
	form := url.Values{"chat_id": {chatID}, "text": {message}}
	endpoint := "https://api.telegram.org/bot" + token + "/sendMessage"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return errors.New("cannot prepare Telegram request")
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := client.Do(request)
	if err != nil {
		return errors.New("Telegram request failed; check network access")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("Telegram returned HTTP %d", response.StatusCode)
	}
	var result struct {
		OK bool `json:"ok"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&result); err != nil {
		return errors.New("Telegram returned an invalid response")
	}
	if !result.OK {
		return errors.New("Telegram rejected the message")
	}
	return nil
}
