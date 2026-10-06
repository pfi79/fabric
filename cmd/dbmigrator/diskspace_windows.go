//go:build windows

/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package main

// freeBytes returns how many bytes are free on the filesystem the given path is
// on, and reports that it cannot find out: this platform is not asked the
// question, and a migration there goes ahead without the check rather than
// refusing to begin over a check that was never made.
func freeBytes(path string) (uint64, bool) {
	return 0, false
}
