package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type recordingFile struct {
	final      string
	part       string
	statePath  string
	sequenceNo int
}

func recordingBase(login string, start time.Time) string {
	return login + "-" + start.Format("2006-01-02")
}

func recordingStatePath(outputDir, base string) string {
	return filepath.Join(outputDir, ".recording-state", base+".seq")
}

func readSequence(path string) (int, error) {
	contents, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	number, err := strconv.Atoi(strings.TrimSpace(string(contents)))
	if err != nil || number < 1 {
		return 0, fmt.Errorf("invalid recording sequence in %s", path)
	}
	return number, nil
}

func existingSequence(outputDir, base string) (int, error) {
	entries, err := os.ReadDir(outputDir)
	if err != nil {
		return 0, err
	}
	maxNumber := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".part")
		if !strings.HasSuffix(name, ".ts") {
			continue
		}
		stem := strings.TrimSuffix(name, ".ts")
		number := 0
		if stem == base {
			number = 1
		} else if suffix, ok := strings.CutPrefix(stem, base+"-"); ok {
			parsed, err := strconv.Atoi(suffix)
			if err == nil && parsed >= 2 {
				number = parsed
			}
		}
		if number > maxNumber {
			maxNumber = number
		}
	}
	return maxNumber, nil
}

func writeSequence(path string, number int) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".seq-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := fmt.Fprintln(file, number); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

func primeRecordingSequence(outputDir, login string, start time.Time) error {
	base := recordingBase(login, start)
	statePath := recordingStatePath(outputDir, base)
	stored, err := readSequence(statePath)
	if err != nil {
		return err
	}
	existing, err := existingSequence(outputDir, base)
	if err != nil {
		return err
	}
	if existing > stored {
		return writeSequence(statePath, existing)
	}
	return nil
}

func nextRecordingPath(outputDir, login string, start time.Time) (recordingFile, error) {
	base := recordingBase(login, start)
	statePath := recordingStatePath(outputDir, base)
	stored, err := readSequence(statePath)
	if err != nil {
		return recordingFile{}, err
	}
	existing, err := existingSequence(outputDir, base)
	if err != nil {
		return recordingFile{}, err
	}
	number := max(stored, existing) + 1
	name := base + ".ts"
	if number > 1 {
		name = fmt.Sprintf("%s-%d.ts", base, number)
	}
	final := filepath.Join(outputDir, name)
	return recordingFile{final: final, part: final + ".part", statePath: statePath, sequenceNo: number}, nil
}

func completeRecording(recording recordingFile) (bool, error) {
	info, err := os.Stat(recording.part)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if info.Size() == 0 {
		return false, os.Remove(recording.part)
	}
	stored, err := readSequence(recording.statePath)
	if err != nil {
		return false, err
	}
	if recording.sequenceNo > stored {
		if err := writeSequence(recording.statePath, recording.sequenceNo); err != nil {
			return false, err
		}
	}
	if _, err := os.Stat(recording.final); err == nil {
		return false, fmt.Errorf("final recording already exists: %s", recording.final)
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	if err := os.Rename(recording.part, recording.final); err != nil {
		return false, err
	}
	return true, nil
}
