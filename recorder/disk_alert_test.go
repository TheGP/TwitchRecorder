package main

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (roundTrip roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

func TestTelegramProxyDoesNotAffectOtherClients(t *testing.T) {
	var proxyCalls atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Proxy-Authorization") != "" {
			t.Error("proxy credentials reached the destination")
		}
		io.WriteString(w, "direct")
	}))
	defer origin.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyCalls.Add(1)
		wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("test-user:test-password"))
		if r.Header.Get("Proxy-Authorization") != wantAuth || r.URL.String() != origin.URL+"/check" {
			t.Error("proxy did not receive the expected target and authentication")
		}
		io.WriteString(w, "proxied")
	}))
	defer proxy.Close()
	proxyURL, err := url.Parse(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxyURL.User = url.UserPassword("test-user", "test-password")
	telegram, err := newTelegramClient(proxyURL.String())
	if err != nil {
		t.Fatal(err)
	}
	defer telegram.CloseIdleConnections()
	for _, request := range []struct {
		client *http.Client
		want   string
	}{
		{client: telegram, want: "proxied"},
		{client: &http.Client{Timeout: time.Second}, want: "direct"},
	} {
		response, err := request.client.Get(origin.URL + "/check")
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil || string(body) != request.want {
			t.Fatalf("response = %q, error = %v, want %q", body, err, request.want)
		}
	}
	if proxyCalls.Load() != 1 {
		t.Fatalf("proxy received %d requests, want only the Telegram client request", proxyCalls.Load())
	}
}

func TestTelegramProxyConfiguration(t *testing.T) {
	for _, address := range []string{"", "http://proxy.example:8888", "https://proxy.example:8443", "socks5://proxy.example:1080", "socks5h://proxy.example:1080"} {
		client, err := newTelegramClient(address)
		if err != nil {
			t.Fatalf("valid proxy configuration rejected: %v", err)
		}
		client.CloseIdleConnections()
	}
	for _, address := range []string{"proxy.example:8888", "ftp://proxy.example:21", "socks5h://:1080", "http://user:private-secret@proxy.example:bad", "http://user:private-secret@proxy.example/path", "http://proxy.example?private-secret", "http://proxy.example#private-secret"} {
		_, err := newTelegramClient(address)
		if err == nil {
			t.Error("invalid proxy configuration was accepted")
		} else if strings.Contains(err.Error(), "private-secret") {
			t.Fatal("proxy credentials leaked into the configuration error")
		}
	}
}

func TestDiskAlertSendsOnceUntilRecovery(t *testing.T) {
	sent := 0
	alert := diskAlert{
		outputDir: "recordings",
		host:      "test-host",
		notify: func(_ context.Context, message string) error {
			sent++
			if !strings.Contains(message, "below 1.5 GB") || !strings.Contains(message, "test-host") {
				t.Fatalf("unexpected alert message: %q", message)
			}
			return nil
		},
	}
	ctx := context.Background()
	for _, free := range []uint64{lowDiskThresholdBytes - 1, lowDiskThresholdBytes - 1} {
		if err := alert.check(ctx, free); err != nil {
			t.Fatal(err)
		}
	}
	if sent != 1 {
		t.Fatalf("sent %d alerts while continuously low, want 1", sent)
	}
	if err := alert.check(ctx, lowDiskThresholdBytes); err != nil {
		t.Fatal(err)
	}
	if err := alert.check(ctx, lowDiskThresholdBytes-1); err != nil {
		t.Fatal(err)
	}
	if sent != 2 {
		t.Fatalf("sent %d alerts after recovery and another drop, want 2", sent)
	}
}

func TestDiskAlertRetriesFailedSend(t *testing.T) {
	attempts := 0
	alert := diskAlert{notify: func(context.Context, string) error {
		attempts++
		if attempts == 1 {
			return errors.New("temporary failure")
		}
		return nil
	}}
	ctx := context.Background()
	if err := alert.check(ctx, 0); err == nil {
		t.Fatal("expected first alert attempt to fail")
	}
	if err := alert.check(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if attempts != 2 {
		t.Fatalf("attempted %d sends, want 2", attempts)
	}
}

func TestCriticalDiskAlertAfterWarningAndRecovery(t *testing.T) {
	var messages []string
	alert := diskAlert{notify: func(_ context.Context, message string) error {
		messages = append(messages, message)
		return nil
	}}
	ctx := context.Background()
	for _, free := range []uint64{1_400_000_000, 999_999_999, 0, 1_000_000_000, 999_999_999} {
		if err := alert.check(ctx, free); err != nil {
			t.Fatal(err)
		}
	}
	if len(messages) != 3 || !strings.Contains(messages[0], "below 1.5 GB") || !strings.Contains(messages[1], "New recordings are blocked below 1 GB") || !strings.Contains(messages[2], "New recordings are blocked below 1 GB") {
		t.Fatalf("want warning, critical alert, then another critical alert after recovery; got %v", messages)
	}
}

func TestActiveRecordingSpaceGuard(t *testing.T) {
	for _, test := range []struct {
		name    string
		free    uint64
		blocked bool
	}{
		{name: "full", free: 0, blocked: true},
		{name: "below active cutoff", free: 99_999_999, blocked: true},
		{name: "exact active cutoff", free: 100_000_000},
		{name: "below new recording cutoff", free: 999_999_999},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := activeRecordingSpaceError(test.free); (err != nil) != test.blocked {
				t.Fatalf("free=%d: blocked=%v, error=%v", test.free, test.blocked, err)
			}
		})
	}
}

func TestCriticalDiskAlertRetriesAfterEarlierWarning(t *testing.T) {
	attempts := 0
	alert := diskAlert{notify: func(context.Context, string) error {
		attempts++
		if attempts == 2 {
			return errors.New("temporary failure")
		}
		return nil
	}}
	ctx := context.Background()
	if err := alert.check(ctx, 1_400_000_000); err != nil {
		t.Fatal(err)
	}
	if err := alert.check(ctx, 0); err == nil || alert.criticalAlerted {
		t.Fatal("failed critical warning must remain eligible for retry")
	}
	if err := alert.check(ctx, 0); err != nil || !alert.criticalAlerted || attempts != 3 {
		t.Fatalf("critical warning not retried: attempts=%d, error=%v", attempts, err)
	}
}

func TestRecordingSpaceGuard(t *testing.T) {
	for _, test := range []struct {
		name    string
		free    uint64
		err     error
		blocked bool
	}{
		{name: "full", free: 0, blocked: true},
		{name: "below minimum", free: 999_999_999, blocked: true},
		{name: "exact minimum", free: 1_000_000_000},
		{name: "above minimum", free: 1_000_000_001},
		{name: "cannot determine space", err: errors.New("filesystem unavailable"), blocked: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := recordingSpaceError(test.free, test.err); (err != nil) != test.blocked {
				t.Fatalf("free=%d: blocked=%v, error=%v", test.free, test.blocked, err)
			}
		})
	}
}

func TestDiskFreeBytesForOutputDirectory(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "windows" {
		t.Skip("disk space checks are supported on Linux and Windows")
	}
	if _, err := diskFreeBytes(t.TempDir()); err != nil {
		t.Fatal(err)
	}
}

func TestSendTelegramUsesConfiguredChat(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodPost || request.URL.Path != "/bot12345:abcdef/sendMessage" {
			t.Fatalf("unexpected Telegram request: %s %s", request.Method, request.URL.Path)
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatal(err)
		}
		form, err := url.ParseQuery(string(body))
		if err != nil {
			t.Fatal(err)
		}
		if form.Get("chat_id") != "42" || form.Get("text") != "low disk" {
			t.Fatalf("unexpected Telegram form: %v", form)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"ok":true}`))}, nil
	})}
	if err := sendTelegram(context.Background(), client, "12345:abcdef", "42", "low disk"); err != nil {
		t.Fatal(err)
	}
}
