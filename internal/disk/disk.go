// Package disk reports free space on the filesystem holding a path.
package disk

// Usage is the space on one filesystem, in bytes. Free is what an ordinary
// (non-root) user can still write, matching the "Avail" column of df.
type Usage struct {
	Free  uint64
	Total uint64
}

// UsedPercent is the share of the filesystem that isn't available, 0-100.
func (u Usage) UsedPercent() float64 {
	if u.Total == 0 {
		return 0
	}
	return 100 * float64(u.Total-min(u.Free, u.Total)) / float64(u.Total)
}
