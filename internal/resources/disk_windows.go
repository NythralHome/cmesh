//go:build windows

package resources

func discoverDisk(path string) (uint64, uint64) {
	return 0, 0
}
