package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"time"
)

const recordingDiskCheckInterval = time.Second
const recordingStopGracePeriod = 5 * time.Second

func runRecordingCommand(ctx context.Context, executable string, args []string, freeBytes func() (uint64, error), checkInterval time.Duration) error {
	if err := recordingSpaceError(freeBytes()); err != nil {
		return err
	}
	recordingCtx, stop := context.WithCancel(ctx)
	defer stop()
	command := exec.CommandContext(recordingCtx, executable, args...)
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	command.Cancel = func() error { return command.Process.Signal(os.Interrupt) }
	command.WaitDelay = recordingStopGracePeriod
	if err := command.Start(); err != nil {
		return err
	}

	var spaceErr error
	watcherDone := make(chan struct{})
	go func() {
		defer close(watcherDone)
		ticker := time.NewTicker(checkInterval)
		defer ticker.Stop()
		for {
			select {
			case <-recordingCtx.Done():
				return
			case <-ticker.C:
				free, err := freeBytes()
				if err != nil {
					log.Printf("Cannot check disk space for active recording: %v", err)
					continue
				}
				if err := activeRecordingSpaceError(free); err != nil {
					spaceErr = err
					stop()
					return
				}
			}
		}
	}()
	err := command.Wait()
	stop()
	<-watcherDone
	if spaceErr != nil {
		return fmt.Errorf("recording stopped to protect disk space: %w", spaceErr)
	}
	return err
}
