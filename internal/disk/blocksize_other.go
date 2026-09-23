//go:build unix && !linux

package disk

import "syscall"

// blockSize is the unit Blocks/Bavail are counted in. BSD-derived systems
// (including macOS) report it as Bsize.
func blockSize(st *syscall.Statfs_t) uint64 {
	return uint64(st.Bsize)
}
