//go:build !linux

package sysexec

func Owner(path string) (uid, gid int, name string, ok bool) { return 0, 0, "", false }
