/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package pebblehelper

import (
	"os"
	"path/filepath"
	"sync"
	"syscall"

	"github.com/pkg/errors"
)

// FileLock encapsulates a file lock using syscall.Flock.
// As the FileLock is intended to be used by a single process/goroutine,
// the internal mutex provides basic safety.
type FileLock struct {
	filePath string
	file     *os.File
	mu       sync.Mutex
}

// NewFileLock returns a new file based lock manager.
func NewFileLock(filePath string) *FileLock {
	return &FileLock{
		filePath: filePath,
	}
}

// Lock acquires a file lock on the given path using syscall.Flock.
func (f *FileLock) Lock() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.file != nil {
		return errors.Errorf("lock is already acquired on file %s", f.filePath)
	}
	if err := os.MkdirAll(filepath.Dir(f.filePath), 0o755); err != nil {
		return errors.Wrapf(err, "error creating lock directory for %s", f.filePath)
	}
	file, err := os.OpenFile(f.filePath, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return errors.Wrapf(err, "error opening lock file %s", f.filePath)
	}
	if err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		if err == syscall.EAGAIN {
			return errors.Errorf("lock is already acquired on file %s", f.filePath)
		}
		return errors.Wrapf(err, "error acquiring lock on file %s", f.filePath)
	}
	f.file = file
	return nil
}

// IsLocked returns whether the lock is currently held.
func (f *FileLock) IsLocked() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.file != nil
}

// Unlock releases a previously acquired lock.
func (f *FileLock) Unlock() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.file == nil {
		return
	}
	if err := syscall.Flock(int(f.file.Fd()), syscall.LOCK_UN); err != nil {
		logger.Warningf("unable to release the lock on file %s: %s", f.filePath, err)
	}
	if err := f.file.Close(); err != nil {
		logger.Warningf("unable to close the lock file %s: %s", f.filePath, err)
	}
	f.file = nil
}
