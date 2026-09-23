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

func TestElideURLQueries(t *testing.T) {
	in := `Failed: Head "https://us.aws.cdn.hf.co/xet-bridge-us/6a51/1e9b?user_id=public&Expires=1790170164&Signature=MEUCIQ": blocked redirect to a different host`
	want := `Failed: Head "https://us.aws.cdn.hf.co/xet-bridge-us/6a51/1e9b?…": blocked redirect to a different host`
	if got := elideURLQueries(in); got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	if got := elideURLQueries("no urls here"); got != "no urls here" {
		t.Errorf("plain text changed: %q", got)
	}
}
