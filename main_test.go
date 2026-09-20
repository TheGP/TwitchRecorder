package main

import (
	"reflect"
	"testing"
)

func TestParseChannels(t *testing.T) {
	channels, err := parseChannels(" N_Y_X_Official, bcomplex_matia, n_y_x_official ")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"n_y_x_official", "bcomplex_matia"}
	if !reflect.DeepEqual(channels, want) {
		t.Fatalf("channels = %v, want %v", channels, want)
	}
}

func TestParseChannelsRejectsEmptyEntry(t *testing.T) {
	if _, err := parseChannels("n_y_x_official,"); err == nil {
		t.Fatal("expected an error for an empty channel entry")
	}
}
