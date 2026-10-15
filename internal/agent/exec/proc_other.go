//go:build !linux

package exec

import osexec "os/exec"

// The agent only runs on Linux; elsewhere commands run as the current user.
func prepare(cmd *osexec.Cmd, uid, gid int) {}
