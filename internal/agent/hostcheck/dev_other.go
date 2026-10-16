//go:build !linux

package hostcheck

// deviceOf cannot tell filesystems apart here; every path counts as the same disk.
func deviceOf(string) uint64 { return 0 }
