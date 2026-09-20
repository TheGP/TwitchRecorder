//go:build !linux && !windows

package main

import "errors"

func diskFreeBytes(string) (uint64, error) {
	return 0, errors.New("disk space checks are supported on Linux and Windows")
}
