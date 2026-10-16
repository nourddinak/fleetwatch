package hostcheck

import "syscall"

// deviceOf identifies the filesystem a path is on.
func deviceOf(path string) uint64 {
	var st syscall.Stat_t
	if syscall.Lstat(path, &st) != nil {
		return 0
	}
	return uint64(st.Dev)
}
