//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

func hideCommandWindow(command *exec.Cmd) {
	const createNoWindow = 0x08000000
	command.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createNoWindow,
	}
}
