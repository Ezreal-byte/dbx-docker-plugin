//go:build !windows

package main

import (
	"os/exec"
	"runtime"
)

func launchBrowser(address string) error {
	program := "xdg-open"
	if runtime.GOOS == "darwin" {
		program = "open"
	}
	cmd := exec.Command(program, address)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
