package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (roundTrip roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

func TestDiskAlertSendsOnceUntilRecovery(t *testing.T) {
	sent := 0
	alert := diskAlert{
		outputDir: "recordings",
		host:      "test-host",
		notify: func(_ context.Context, message string) error {
			sent++
			if !strings.Contains(message, "below 1 GB") || !strings.Contains(message, "test-host") {
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
