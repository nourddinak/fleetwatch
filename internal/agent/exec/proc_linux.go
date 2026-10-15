package exec

import (
	osexec "os/exec"
	"syscall"
)

// prepare runs the command as uid/gid in its own process group, and makes the
// timeout kill the whole group (shell plus children such as npm workers).
func prepare(cmd *osexec.Cmd, uid, gid int) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Credential: &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)}}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
