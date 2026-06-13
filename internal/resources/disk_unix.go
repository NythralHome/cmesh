//go:build !windows

package resources

import "syscall"

func discoverDisk(path string) (uint64, uint64) {
	if path == "" {
		path = "."
	}

	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		if path == "." {
			return 0, 0
		}
		return discoverDisk(".")
	}

	blockSize := uint64(stat.Bsize)
	total := stat.Blocks * blockSize
	free := stat.Bavail * blockSize
	return total, free
}
