//go:build unix

package disk

import "syscall"

// Stat returns the usage of the filesystem path lives on.
func Stat(path string) (Usage, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return Usage{}, err
	}
	bs := blockSize(&st)
	return Usage{Free: uint64(st.Bavail) * bs, Total: uint64(st.Blocks) * bs}, nil
}
