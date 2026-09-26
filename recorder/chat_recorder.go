package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

const twitchIRCURL = "wss://irc-ws.chat.twitch.tv:443"

type chatIdentity struct {
	login string
	token string
}

type chatCaptureResult struct {
	count int
	err   error
}

type chatRecordingFile struct {
	final string
	part  string
}

type chatRecord struct {
	Timestamp  time.Time         `json:"timestamp"`
	ReceivedAt time.Time         `json:"received_at"`
	Offset     float64           `json:"offset_seconds"`
	Channel    string            `json:"channel"`
	Username   string            `json:"username"`
	Display    string            `json:"display_name,omitempty"`
	Message    string            `json:"message"`
	MessageID  string            `json:"message_id,omitempty"`
	Badges     map[string]string `json:"badges,omitempty"`
	Color      string            `json:"color,omitempty"`
	Moderator  bool              `json:"moderator,omitempty"`
	Subscriber bool              `json:"subscriber,omitempty"`
}

func validateChatToken(ctx context.Context, client *http.Client, token string) (chatIdentity, error) {
	var result struct {
		Login  string   `json:"login"`
		Scopes []string `json:"scopes"`
	}
	if err := getJSON(ctx, client, "https://id.twitch.tv/oauth2/validate", map[string]string{"Authorization": "Bearer " + token}, &result); err != nil {
		return chatIdentity{}, err
	}
	if result.Login == "" || !slices.Contains(result.Scopes, "chat:read") {
		return chatIdentity{}, errors.New("token requires a login and chat:read scope")
	}
	return chatIdentity{login: strings.ToLower(result.Login), token: token}, nil
}

func chatRecordingFor(recording recordingFile) chatRecordingFile {
	base := strings.TrimSuffix(recording.final, ".ts")
	return chatRecordingFile{final: base + ".chat.jsonl", part: base + ".chat.jsonl.part"}
}

func completeChatRecording(recording chatRecordingFile) error {
	info, err := os.Lstat(recording.part)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("chat recording is not a regular file: %s", recording.part)
	}
	if _, err := os.Lstat(recording.final); err == nil {
		return fmt.Errorf("final chat recording already exists: %s", recording.final)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(recording.part, recording.final)
}

func discardChatRecordingWithoutMedia(recording chatRecordingFile) error {
	info, err := os.Lstat(recording.part)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("chat recording is not a regular file: %s", recording.part)
	}
	return os.Remove(recording.part)
}

func recordChat(ctx context.Context, identity chatIdentity, channel, path string, startedAt time.Time) (int, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return 0, fmt.Errorf("open chat recording: %w", err)
	}
	encoder := json.NewEncoder(file)
	count := 0
	backoff := time.Second
	var lastErr error
	for ctx.Err() == nil {
		var writeErr error
		sessionCount, err := recordChatSession(ctx, identity, channel, func(record chatRecord) error {
			record.Offset = chatOffset(startedAt, record.ReceivedAt)
			writeErr = encoder.Encode(record)
			return writeErr
		})
		count += sessionCount
		if writeErr != nil {
			lastErr = fmt.Errorf("write chat recording: %w", writeErr)
			break
		}
		if ctx.Err() != nil {
			break
		}
		lastErr = err
		if sessionCount > 0 {
			backoff = time.Second
		}
		log.Printf("Chat connection for %s lost; retrying in %s: %v", channel, backoff, err)
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
	if err := file.Sync(); err != nil && lastErr == nil {
		lastErr = err
	}
	if err := file.Close(); err != nil && lastErr == nil {
		lastErr = err
	}
	return count, lastErr
}

func chatOffset(startedAt, receivedAt time.Time) float64 {
	return max(0, receivedAt.Sub(startedAt).Seconds())
}

func recordChatSession(ctx context.Context, identity chatIdentity, channel string, write func(chatRecord) error) (int, error) {
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, twitchIRCURL, nil)
	if err != nil {
		return 0, fmt.Errorf("connect Twitch IRC: %w", err)
	}
	closed := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			conn.Close()
		case <-closed:
		}
	}()
	defer close(closed)
	defer conn.Close()
	for _, command := range []string{
		"PASS oauth:" + identity.token,
		"NICK " + identity.login,
		"CAP REQ :twitch.tv/tags twitch.tv/commands",
		"JOIN #" + channel,
	} {
		if err := conn.SetWriteDeadline(time.Now().Add(15 * time.Second)); err != nil {
			return 0, err
		}
		if err := conn.WriteMessage(websocket.TextMessage, []byte(command+"\r\n")); err != nil {
			return 0, fmt.Errorf("authenticate Twitch IRC: %w", err)
		}
	}
	count := 0
	for {
		if err := conn.SetReadDeadline(time.Now().Add(6 * time.Minute)); err != nil {
			return count, err
		}
		_, raw, err := conn.ReadMessage()
		if err != nil {
			if ctx.Err() != nil {
				return count, nil
			}
			return count, fmt.Errorf("read Twitch IRC: %w", err)
		}
		for _, line := range strings.Split(string(raw), "\r\n") {
			switch {
			case strings.HasPrefix(line, "PING "):
				if err := conn.SetWriteDeadline(time.Now().Add(15 * time.Second)); err != nil {
					return count, err
				}
				if err := conn.WriteMessage(websocket.TextMessage, []byte("PONG "+strings.TrimPrefix(line, "PING ")+"\r\n")); err != nil {
					return count, err
				}
			case strings.Contains(line, " RECONNECT"):
				return count, errors.New("Twitch requested reconnect")
			case strings.Contains(line, " NOTICE "):
				return count, errors.New("Twitch IRC rejected the request")
			default:
				record, ok := parseChatMessage(line, time.Now().UTC())
				if !ok || record.Channel != channel {
					continue
				}
				if err := write(record); err != nil {
					return count, fmt.Errorf("write chat message: %w", err)
				}
				count++
			}
		}
	}
}

func parseChatMessage(line string, receivedAt time.Time) (chatRecord, bool) {
	if !strings.HasPrefix(line, "@") {
		return chatRecord{}, false
	}
	tagText, remainder, ok := strings.Cut(strings.TrimPrefix(line, "@"), " ")
	if !ok || !strings.HasPrefix(remainder, ":") {
		return chatRecord{}, false
	}
	prefix, command, ok := strings.Cut(strings.TrimPrefix(remainder, ":"), " ")
	if !ok || !strings.HasPrefix(command, "PRIVMSG #") {
		return chatRecord{}, false
	}
	target, message, ok := strings.Cut(strings.TrimPrefix(command, "PRIVMSG #"), " :")
	if !ok || target == "" {
		return chatRecord{}, false
	}
	username, _, _ := strings.Cut(prefix, "!")
	if username == "" {
		return chatRecord{}, false
	}
	tags := parseIRCTags(tagText)
	timestamp := receivedAt
	if milliseconds, err := strconv.ParseInt(tags["tmi-sent-ts"], 10, 64); err == nil {
		timestamp = time.UnixMilli(milliseconds).UTC()
	}
	return chatRecord{
		Timestamp:  timestamp,
		ReceivedAt: receivedAt,
		Channel:    strings.ToLower(target),
		Username:   strings.ToLower(username),
		Display:    tags["display-name"],
		Message:    message,
		MessageID:  tags["id"],
		Badges:     parseBadges(tags["badges"]),
		Color:      tags["color"],
		Moderator:  tags["mod"] == "1",
		Subscriber: tags["subscriber"] == "1",
	}, true
}

func parseIRCTags(value string) map[string]string {
	tags := make(map[string]string)
	for _, field := range strings.Split(value, ";") {
		key, raw, found := strings.Cut(field, "=")
		if found {
			tags[key] = unescapeIRCTag(raw)
		} else {
			tags[key] = ""
		}
	}
	return tags
}

func unescapeIRCTag(value string) string {
	var result strings.Builder
	for index := 0; index < len(value); index++ {
		if value[index] != '\\' || index+1 >= len(value) {
			result.WriteByte(value[index])
			continue
		}
		index++
		switch value[index] {
		case 's':
			result.WriteByte(' ')
		case ':':
			result.WriteByte(';')
		case 'r':
			result.WriteByte('\r')
		case 'n':
			result.WriteByte('\n')
		case '\\':
			result.WriteByte('\\')
		default:
			result.WriteByte(value[index])
		}
	}
	return result.String()
}

func parseBadges(value string) map[string]string {
	if value == "" {
		return nil
	}
	badges := make(map[string]string)
	for _, badge := range strings.Split(value, ",") {
		name, version, found := strings.Cut(badge, "/")
		if found && name != "" {
			badges[name] = version
		}
	}
	if len(badges) == 0 {
		return nil
	}
	return badges
}

func runChatTest(channel string, duration time.Duration, output string) error {
	channel = strings.ToLower(strings.TrimSpace(channel))
	if !validLogin.MatchString(channel) {
		return fmt.Errorf("invalid Twitch login %q", channel)
	}
	if duration < time.Second || duration > 10*time.Minute {
		return errors.New("chat test duration must be between 1 second and 10 minutes")
	}
	if err := loadEnv(".env"); err != nil {
		return err
	}
	token := strings.TrimPrefix(strings.TrimSpace(os.Getenv("CHAT_OAUTH")), "oauth:")
	if token == "" {
		token = strings.TrimPrefix(strings.TrimSpace(os.Getenv("TWITCH_CHAT_TOKEN")), "oauth:")
	}
	if token == "" {
		return errors.New("CHAT_OAUTH or TWITCH_CHAT_TOKEN is required for -chat-test")
	}
	client := &http.Client{Timeout: 15 * time.Second}
	identity, err := validateChatToken(context.Background(), client, token)
	if err != nil {
		return fmt.Errorf("validate CHAT_OAUTH: %w", err)
	}
	if output == "" {
		output = filepath.Join(os.TempDir(), channel+"-chat-test.chat.jsonl")
	}
	output, err = filepath.Abs(output)
	if err != nil {
		return err
	}
	if !strings.HasSuffix(strings.ToLower(output), ".chat.jsonl") {
		return errors.New("chat test output must end in .chat.jsonl")
	}
	chat := chatRecordingFile{final: output, part: output + ".part"}
	if _, err := os.Lstat(chat.final); err == nil {
		return fmt.Errorf("chat test output already exists: %s", chat.final)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if _, err := os.Lstat(chat.part); err == nil {
		return fmt.Errorf("chat test partial output already exists: %s", chat.part)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), duration)
	defer cancel()
	count, captureErr := recordChat(ctx, identity, channel, chat.part, time.Now().UTC())
	if err := completeChatRecording(chat); err != nil {
		return err
	}
	if captureErr != nil {
		return fmt.Errorf("chat test captured %d messages to %s but ended with an error: %w", count, chat.final, captureErr)
	}
	log.Printf("Chat test captured %d messages to %s", count, chat.final)
	return nil
}
