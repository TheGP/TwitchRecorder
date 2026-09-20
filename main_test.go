package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestNextRecordingPath(t *testing.T) {
	dir := t.TempDir()
	start := time.Date(2024, 9, 10, 0, 30, 0, 0, time.FixedZone("UTC+2", 2*60*60))
	want := []string{"india-2024-09-09.ts", "india-2024-09-09-2.ts", "india-2024-09-09-3.ts"}
	for _, name := range want {
		path, err := nextRecordingPath(dir, "india", start)
		if err != nil {
			t.Fatal(err)
		}
		if path != filepath.Join(dir, name) {
			t.Fatalf("path = %q, want %q", path, filepath.Join(dir, name))
		}
		if err := os.WriteFile(path, nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	path, err := nextRecordingPath(dir, "other_channel", start)
	if err != nil || path != filepath.Join(dir, "other_channel-2024-09-09.ts") {
		t.Fatalf("other channel path = %q, error = %v", path, err)
	}
}

func TestParseChannels(t *testing.T) {
	channels, err := parseChannels(" N_Y_X_Official:audio_only, bcomplex_matia:best, n_y_x_official:audio_only ")
	if err != nil {
		t.Fatal(err)
	}
	want := []channelConfig{{login: "n_y_x_official", quality: "audio_only"}, {login: "bcomplex_matia", quality: "best"}}
	if !reflect.DeepEqual(channels, want) {
		t.Fatalf("channels = %v, want %v", channels, want)
	}
}

func TestParseChannelsDefaultsToAudioOnly(t *testing.T) {
	channels, err := parseChannels("n_y_x_official")
	if err != nil {
		t.Fatal(err)
	}
	if len(channels) != 1 || channels[0].quality != "audio_only" {
		t.Fatalf("channels = %v, want one audio_only channel", channels)
	}
}

func TestParseChannelsRejectsEmptyEntry(t *testing.T) {
	if _, err := parseChannels("n_y_x_official,"); err == nil {
		t.Fatal("expected an error for an empty channel entry")
	}
}

func TestParseChannelsRejectsInvalidQuality(t *testing.T) {
	if _, err := parseChannels("n_y_x_official:--output"); err == nil {
		t.Fatal("expected an error for a quality that looks like an option")
	}
}

func TestParseChannelsRejectsConflictingQualities(t *testing.T) {
	if _, err := parseChannels("n_y_x_official:best,n_y_x_official:audio_only"); err == nil {
		t.Fatal("expected an error for conflicting qualities")
	}
}
