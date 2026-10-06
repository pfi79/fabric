//go:build !windows

/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package main

import (
	"os"
	"path/filepath"
	"syscall"
)

// freeBytes returns how many bytes are free on the filesystem the given path is
// on, and says so when it cannot find out: a check that cannot be made is
// reported as a check that was not made, rather than as a check that passed.
func freeBytes(path string) (uint64, bool) {
	probe := path
	for {
		if _, err := os.Stat(probe); err == nil {
			break
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			return 0, false
		}
		probe = parent
	}

	var stat syscall.Statfs_t
	if err := syscall.Statfs(probe, &stat); err != nil {
		return 0, false
	}
	return stat.Bavail * uint64(stat.Bsize), true
}
