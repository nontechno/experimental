//go:build linux || darwin

package nfsserver

import "syscall"

// diskStats reports the size and available space of the filesystem holding
// path. syscall.Statfs is a direct kernel call on both Linux and macOS, so
// this needs no cgo.
func diskStats(path string) (total, free uint64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	bsize := uint64(st.Bsize)
	return uint64(st.Blocks) * bsize, uint64(st.Bavail) * bsize, nil
}
