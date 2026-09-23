package web

import "testing"

func TestFormatCount(t *testing.T) {
	cases := map[int]string{0: "0", 999: "999", 1000: "1,000", 262144: "262,144", 1234567: "1,234,567", -4096: "-4,096"}
	for in, want := range cases {
		if got := formatCount(in); got != want {
			t.Errorf("formatCount(%d) = %q, want %q", in, got, want)
		}
	}
}
