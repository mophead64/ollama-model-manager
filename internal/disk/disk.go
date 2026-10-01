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

// Thresholds for Level: free space below either the size or the share is
// flagged. Models run to tens of gigabytes, so a fixed floor matters as much
// as the percentage on a big disk.
const (
	lowFreeBytes  = 10 << 30 // 10 GiB: not room for many models
	lowFreePct    = 5
	warnFreeBytes = 50 << 30 // 50 GiB: a couple of large models
	warnFreePct   = 15
)

// Level says how worrying the free space is: "low" (nearly full), "warn"
// (getting there) or "" (plenty).
func (u Usage) Level() string {
	if u.Total == 0 {
		return ""
	}
	pct := 100 * float64(u.Free) / float64(u.Total)
	switch {
	case u.Free < lowFreeBytes || pct < lowFreePct:
		return "low"
	case u.Free < warnFreeBytes || pct < warnFreePct:
		return "warn"
	}
	return ""
}
