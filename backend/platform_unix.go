//go:build !windows

package main

import (
	"golang.org/x/sys/unix"
	"os"
)

func namedPipeSupported() bool                { return false }
func tryFileLock(f *os.File) bool             { return unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB) == nil }
func unlockFile(f *os.File)                   { _ = unix.Flock(int(f.Fd()), unix.LOCK_UN) }
func replaceFile(source, target string) error { return os.Rename(source, target) }
