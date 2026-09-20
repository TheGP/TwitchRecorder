package main

import (
	"reflect"
	"testing"
)

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
