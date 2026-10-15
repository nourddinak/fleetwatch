package sysexec

import (
	"os"
	"os/user"
	"strconv"
	"syscall"
)

// Owner returns the uid, gid and user name owning path.
func Owner(path string) (uid, gid int, name string, ok bool) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, 0, "", false
	}
	st, isUnix := fi.Sys().(*syscall.Stat_t)
	if !isUnix {
		return 0, 0, "", false
	}
	name = strconv.Itoa(int(st.Uid))
	if u, err := user.LookupId(name); err == nil {
		name = u.Username
	}
	return int(st.Uid), int(st.Gid), name, true
}
