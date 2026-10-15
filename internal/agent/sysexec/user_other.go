//go:build !linux

package sysexec

import "os/exec"

// The agent only runs on Linux; other platforms build for tests and ignore AsUser.
func setUser(cmd *exec.Cmd, o Opts) {}
