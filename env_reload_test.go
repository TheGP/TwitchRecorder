package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRecordingStateWaitsForAllRecordings(t *testing.T) {
	state := newRecordingState()
	if !state.begin() || !state.begin() {
		t.Fatal("recordings should start before a restart is requested")
	}
	if active := state.requestRestart(); active != 2 {
		t.Fatalf("active recordings = %d, want 2", active)
	}
	if !state.begin() {
		t.Fatal("new recording should start while another recording is still active")
	}
	state.end()
	select {
	case <-state.restartReady:
		t.Fatal("restart became ready while a recording was active")
	default:
	}
	state.end()
	select {
	case <-state.restartReady:
		t.Fatal("restart became ready while a recording was active")
	default:
	}
	state.end()
	select {
	case <-state.restartReady:
	default:
		t.Fatal("restart did not become ready after all recordings ended")
	}
	if state.begin() {
		t.Fatal("new recording started after restart became ready")
	}
}

func TestRecordingStateRestartsWhenIdle(t *testing.T) {
	state := newRecordingState()
	if active := state.requestRestart(); active != 0 {
		t.Fatalf("active recordings = %d, want 0", active)
	}
	select {
	case <-state.restartReady:
	default:
		t.Fatal("restart did not become ready while idle")
	}
}

func TestHashEnvFileDetectsSameSizeEdit(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("CHANNEL_LOGINS=first\n"), 0600); err != nil {
		t.Fatal(err)
	}
	before, err := hashEnvFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("CHANNEL_LOGINS=other\n"), 0600); err != nil {
		t.Fatal(err)
	}
	after, err := hashEnvFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if before == after {
		t.Fatal("file content changed without changing its hash")
	}
}
