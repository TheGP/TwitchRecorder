package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCompletedRecordingKeepsItsNumberAfterTransfer(t *testing.T) {
	dir := t.TempDir()
	start := time.Date(2024, 9, 10, 0, 30, 0, 0, time.FixedZone("UTC+2", 2*60*60))
	recording, err := nextRecordingPath(dir, "india", start)
	if err != nil {
		t.Fatal(err)
	}
	if recording.final != filepath.Join(dir, "india-2024-09-10.ts") {
		t.Fatalf("first recording path = %q", recording.final)
	}
	if err := os.WriteFile(recording.part, []byte("recorded audio"), 0600); err != nil {
		t.Fatal(err)
	}
	if completed, err := completeRecording(recording); err != nil || !completed {
		t.Fatalf("completed = %v, error = %v", completed, err)
	}
	if _, err := os.Stat(recording.part); !os.IsNotExist(err) {
		t.Fatalf("in-progress file still exists: %v", err)
	}
	if err := os.Remove(recording.final); err != nil {
		t.Fatal(err)
	}
	next, err := nextRecordingPath(dir, "india", start)
	if err != nil {
		t.Fatal(err)
	}
	if next.final != filepath.Join(dir, "india-2024-09-10-2.ts") {
		t.Fatalf("next recording path after transfer = %q", next.final)
	}
	if err := os.WriteFile(next.part, []byte("second recording"), 0600); err != nil {
		t.Fatal(err)
	}
	if completed, err := completeRecording(next); err != nil || !completed {
		t.Fatalf("second completed = %v, error = %v", completed, err)
	}
	if err := os.Remove(next.final); err != nil {
		t.Fatal(err)
	}
	third, err := nextRecordingPath(dir, "india", start)
	if err != nil || third.final != filepath.Join(dir, "india-2024-09-10-3.ts") {
		t.Fatalf("third recording path = %q, error = %v", third.final, err)
	}
}

func TestPrimeRecordingSequencePreservesExistingFiles(t *testing.T) {
	dir := t.TempDir()
	start := time.Date(2024, 9, 10, 0, 30, 0, 0, time.Local)
	for _, name := range []string{"india-2024-09-10.ts", "india-2024-09-10-3.ts"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("old recording"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := primeRecordingSequence(dir, "india", start); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"india-2024-09-10.ts", "india-2024-09-10-3.ts"} {
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	next, err := nextRecordingPath(dir, "india", start)
	if err != nil {
		t.Fatal(err)
	}
	if next.final != filepath.Join(dir, "india-2024-09-10-4.ts") {
		t.Fatalf("next recording path after old files moved = %q", next.final)
	}
}

func TestInProgressFileReservesNameAfterCrash(t *testing.T) {
	dir := t.TempDir()
	start := time.Date(2024, 9, 10, 0, 30, 0, 0, time.Local)
	if err := os.WriteFile(filepath.Join(dir, "india-2024-09-10.ts.part"), []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}
	next, err := nextRecordingPath(dir, "india", start)
	if err != nil {
		t.Fatal(err)
	}
	if next.final != filepath.Join(dir, "india-2024-09-10-2.ts") {
		t.Fatalf("next recording path after crash = %q", next.final)
	}
}

func TestEmptyRecordingCanReuseFirstName(t *testing.T) {
	dir := t.TempDir()
	start := time.Date(2024, 9, 10, 0, 30, 0, 0, time.Local)
	recording, err := nextRecordingPath(dir, "india", start)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(recording.part, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if completed, err := completeRecording(recording); err != nil || completed {
		t.Fatalf("empty recording completed = %v, error = %v", completed, err)
	}
	next, err := nextRecordingPath(dir, "india", start)
	if err != nil || next.final != recording.final {
		t.Fatalf("empty recording path changed to %q: %v", next.final, err)
	}
}
