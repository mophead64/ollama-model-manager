//go:build windows

package disk

import "golang.org/x/sys/windows"

// Stat returns the usage of the volume path lives on. Free is what's
// available to this user (quotas included), as with Bavail on Unix.
func Stat(path string) (Usage, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return Usage{}, err
	}
	var free, total, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(p, &free, &total, &totalFree); err != nil {
		return Usage{}, err
	}
	return Usage{Free: free, Total: total}, nil
}
