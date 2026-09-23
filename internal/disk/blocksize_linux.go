package disk

import "syscall"

// blockSize is the unit Blocks/Bavail are counted in. On Linux that's the
// fragment size; Bsize is only the preferred I/O size, and on shared-folder
// mounts (virtiofs, 9p — e.g. Docker Desktop/Rancher Desktop bind mounts) it
// can be hundreds of times larger, inflating the totals accordingly.
func blockSize(st *syscall.Statfs_t) uint64 {
	if st.Frsize > 0 {
		return uint64(st.Frsize)
	}
	return uint64(st.Bsize)
}
