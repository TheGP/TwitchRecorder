package main

import (
	"crypto/sha256"
	"os"
	"sync"
)

func hashEnvFile(path string) ([32]byte, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(contents), nil
}

type recordingState struct {
	mu               sync.Mutex
	active           int
	restartRequested bool
	stopping         bool
	restartReady     chan struct{}
}

func newRecordingState() *recordingState {
	return &recordingState{restartReady: make(chan struct{})}
}

func (state *recordingState) begin() bool {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.stopping {
		return false
	}
	state.active++
	return true
}

func (state *recordingState) end() {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.active--
	if state.restartRequested && state.active == 0 {
		state.stopping = true
		close(state.restartReady)
	}
}

func (state *recordingState) requestRestart() int {
	state.mu.Lock()
	defer state.mu.Unlock()
	if !state.restartRequested {
		state.restartRequested = true
		if state.active == 0 {
			state.stopping = true
			close(state.restartReady)
		}
	}
	return state.active
}
