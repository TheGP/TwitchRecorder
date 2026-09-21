//go:build windows

package main

import (
	"errors"
	"path/filepath"
	"runtime"
	"syscall"
	"unsafe"
)

var getDiskFreeSpaceEx = syscall.NewLazyDLL("kernel32.dll").NewProc("GetDiskFreeSpaceExW")

func diskFreeBytes(path string) (uint64, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return 0, err
	}
	pathPointer, err := syscall.UTF16PtrFromString(absolute)
	if err != nil {
		return 0, err
	}
	var available uint64
	result, _, callErr := getDiskFreeSpaceEx.Call(uintptr(unsafe.Pointer(pathPointer)), uintptr(unsafe.Pointer(&available)), 0, 0)
	runtime.KeepAlive(pathPointer)
	if result == 0 {
		if callErr != syscall.Errno(0) {
			return 0, callErr
		}
		return 0, errors.New("GetDiskFreeSpaceExW failed")
	}
	return available, nil
}
