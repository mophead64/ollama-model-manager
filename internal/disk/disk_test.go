package disk

import "testing"

func TestStat(t *testing.T) {
	u, err := Stat(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if u.Total == 0 || u.Free > u.Total {
		t.Fatalf("implausible usage: %+v", u)
	}
	if _, err := Stat("/does/not/exist"); err == nil {
		t.Error("expected an error for a missing path")
	}
}

func TestUsedPercent(t *testing.T) {
	if got := (Usage{Free: 25, Total: 100}).UsedPercent(); got != 75 {
		t.Errorf("UsedPercent = %v, want 75", got)
	}
	if got := (Usage{}).UsedPercent(); got != 0 {
		t.Errorf("empty UsedPercent = %v, want 0", got)
	}
}

func TestLevel(t *testing.T) {
	const gb = 1 << 30
	for _, tc := range []struct {
		free, total uint64
		want        string
	}{
		{500 * gb, 1000 * gb, ""},
		{100 * gb, 1000 * gb, "warn"}, // 10% free
		{40 * gb, 200 * gb, "warn"},   // 20% free, but under 50 GB
		{40 * gb, 1000 * gb, "low"},   // 4% free
		{8 * gb, 100 * gb, "low"},     // under 10 GB
		{0, 0, ""},                    // unknown
	} {
		if got := (Usage{Free: tc.free, Total: tc.total}).Level(); got != tc.want {
			t.Errorf("Level(%d GB free of %d GB) = %q, want %q", tc.free/gb, tc.total/gb, got, tc.want)
		}
	}
}
