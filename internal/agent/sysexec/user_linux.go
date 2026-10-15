package sysexec

import (
	"os/exec"
	"syscall"
)

func setUser(cmd *exec.Cmd, o Opts) {
	if o.AsUser {
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uint32(o.UID), Gid: uint32(o.GID)}}
	}
}
