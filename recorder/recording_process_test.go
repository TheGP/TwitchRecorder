package main

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestRecordingCommandStopsAndPreservesCapturedMedia(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("graceful interrupt signals are unavailable on Windows")
	}
	for _, test := range []struct {
		name      string
		mode      string
		cancel    bool
		checkFail bool
	}{
		{name: "disk below 100 MB"},
		{name: "disk check fails", checkFail: true},
		{name: "parent shutdown", cancel: true},
		{name: "normal completion", mode: "finish"},
		{name: "active continues at 100 MB", mode: "finish-after-drop"},
		{name: "unresponsive recording", mode: "ignore-interrupt"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			recording, err := nextRecordingPath(t.TempDir(), "test", time.Now())
			if err != nil {
				t.Fatal(err)
			}
			diskCheckFailed, diskCheckRecovered := false, false
			freeBytes := func() (uint64, error) {
				if _, err := os.Stat(recording.part + ".ready"); errors.Is(err, os.ErrNotExist) {
					return 1_000_000_000, nil
				} else if err != nil {
					return 0, err
				}
				if test.mode == "finish" {
					return 1_000_000_000, nil
				}
				if test.mode == "finish-after-drop" {
					return 100_000_000, nil
				}
				if test.cancel {
					cancel()
					return 1_000_000_000, nil
				}
				if test.checkFail && !diskCheckFailed {
					diskCheckFailed = true
					return 0, errors.New("filesystem unavailable")
				}
				diskCheckRecovered = true
				return 99_999_999, nil
			}
			args := []string{"-test.run=^TestRecordingProcessHelper$", "--", "--recording-process-helper", recording.part, test.mode}
			err = runRecordingCommand(ctx, os.Args[0], args, freeBytes, 10*time.Millisecond)
			if test.mode == "finish" || test.mode == "finish-after-drop" {
				if err != nil {
					t.Fatal(err)
				}
			} else if test.cancel {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("shutdown error = %v, want context cancellation", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "stopped to protect disk space") {
				t.Fatalf("recording did not stop for disk protection: %v", err)
			}
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				t.Fatal("recording did not stop before the test deadline")
			}
			if test.checkFail && (!diskCheckFailed || !diskCheckRecovered) {
				t.Fatal("recording stopped before disk checks recovered and confirmed low space")
			}
			if completed, err := completeRecording(recording); err != nil || !completed {
				t.Fatalf("captured recording was not finalized: completed=%v, error=%v", completed, err)
			}
			media, err := os.ReadFile(recording.final)
			if err != nil {
				t.Fatal(err)
			}
			want := "captured media\nflushed on shutdown\n"
			if test.mode == "ignore-interrupt" {
				want = "captured media\n"
			}
			if string(media) != want {
				t.Fatalf("saved media = %q, want %q", media, want)
			}
		})
	}
}

func TestRecordingCommandDoesNotStartBelowCutoff(t *testing.T) {
	path := filepath.Join(t.TempDir(), "recording.part")
	args := []string{"-test.run=^TestRecordingProcessHelper$", "--", "--recording-process-helper", path, "finish"}
	err := runRecordingCommand(context.Background(), os.Args[0], args, func() (uint64, error) {
		return 999_999_999, nil
	}, time.Millisecond)
	if err == nil {
		t.Fatal("recording was allowed below 1 GB")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("recording started below the cutoff: %v", err)
	}
}

func TestRecordingProcessHelper(t *testing.T) {
	index := slices.Index(os.Args, "--recording-process-helper")
	if index < 0 {
		return
	}
	path, mode := os.Args[index+1], os.Args[index+2]
	interrupt := make(chan os.Signal, 1)
	if mode == "ignore-interrupt" {
		signal.Ignore(os.Interrupt)
	} else {
		signal.Notify(interrupt, os.Interrupt)
	}
	if err := os.WriteFile(path, []byte("captured media\n"), 0600); err != nil {
		os.Exit(2)
	}
	if err := os.WriteFile(path+".ready", nil, 0600); err != nil {
		os.Exit(2)
	}
	if mode == "finish-after-drop" {
		time.Sleep(100 * time.Millisecond)
	} else if mode != "finish" {
		select {
		case <-interrupt:
		case <-time.After(30 * time.Second):
			os.Exit(2)
		}
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		os.Exit(2)
	}
	if _, err := file.WriteString("flushed on shutdown\n"); err != nil {
		os.Exit(2)
	}
	if err := file.Close(); err != nil {
		os.Exit(2)
	}
	os.Exit(0)
}
