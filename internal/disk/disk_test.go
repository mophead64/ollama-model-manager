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
