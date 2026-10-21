//go:build unix

package system

import "syscall"

// diskUsage reports the size and used bytes of the filesystem holding path.
func diskUsage(path string) (total, used uint64, ok bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, false
	}
	total = st.Blocks * uint64(st.Bsize)
	return total, total - st.Bavail*uint64(st.Bsize), true
}
