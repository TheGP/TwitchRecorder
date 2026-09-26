package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestParseChatMessage(t *testing.T) {
	received := time.Date(2026, 9, 26, 12, 0, 1, 0, time.UTC)
	line := `@badges=moderator/1,subscriber/12;color=#1E90FF;display-name=Some\sUser;id=message-id;mod=1;subscriber=1;tmi-sent-ts=1790424000000 :some_user!some_user@some_user.tmi.twitch.tv PRIVMSG #tkkttony :hello chat`
	record, ok := parseChatMessage(line, received)
	if !ok {
		t.Fatal("message was not parsed")
	}
	if record.Channel != "tkkttony" || record.Username != "some_user" || record.Display != "Some User" || record.Message != "hello chat" || record.MessageID != "message-id" {
		t.Fatalf("unexpected record: %+v", record)
	}
	if !record.Moderator || !record.Subscriber || !reflect.DeepEqual(record.Badges, map[string]string{"moderator": "1", "subscriber": "12"}) {
		t.Fatalf("unexpected chat metadata: %+v", record)
	}
	if record.Timestamp.Equal(received) || !record.ReceivedAt.Equal(received) {
		t.Fatalf("timestamps were not separated: %+v", record)
	}
}

func TestParseChatMessageIgnoresNonMessages(t *testing.T) {
	for _, line := range []string{"PING :tmi.twitch.tv", ":server 001 bot :Welcome", "@badge-info= :server ROOMSTATE #channel"} {
		if _, ok := parseChatMessage(line, time.Now()); ok {
			t.Fatalf("parsed non-message line %q", line)
		}
	}
}

func TestChatOffsetUsesRecordingStart(t *testing.T) {
	started := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	if got := chatOffset(started, started.Add(12*time.Second+500*time.Millisecond)); got != 12.5 {
		t.Fatalf("chat offset = %v, want 12.5", got)
	}
	if got := chatOffset(started, started.Add(-time.Second)); got != 0 {
		t.Fatalf("negative chat offset = %v, want 0", got)
	}
}

func TestChatRecordingUsesMatchingFilenameAndFinalizesEmptyFile(t *testing.T) {
	dir := t.TempDir()
	media := recordingFile{final: filepath.Join(dir, "tkkttony-2026-09-26-2.ts")}
	chat := chatRecordingFor(media)
	if chat.final != filepath.Join(dir, "tkkttony-2026-09-26-2.chat.jsonl") || chat.part != chat.final+".part" {
		t.Fatalf("unexpected chat paths: %+v", chat)
	}
	if err := os.WriteFile(chat.part, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := completeChatRecording(chat); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(chat.final); err != nil || info.Size() != 0 {
		t.Fatalf("final chat file: info=%v err=%v", info, err)
	}
}

func TestDiscardChatWhenMediaIsEmpty(t *testing.T) {
	chat := chatRecordingFile{part: filepath.Join(t.TempDir(), "recording.chat.jsonl.part")}
	if err := os.WriteFile(chat.part, []byte("partial chat\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := discardChatRecordingWithoutMedia(chat); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(chat.part); !os.IsNotExist(err) {
		t.Fatalf("chat partial still exists: %v", err)
	}
}
