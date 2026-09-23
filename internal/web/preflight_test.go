package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/mophead64/ollama-model-manager/internal/disk"
	"github.com/mophead64/ollama-model-manager/internal/downloads"
	"github.com/mophead64/ollama-model-manager/internal/sysinfo"
)

func TestQueueDownloadAsksBeforeTooBig(t *testing.T) {
	h := newTestServer(t, fakeOllama(t, 1).URL)

	// A 1 PiB model doesn't fit on the test disk: confirm first, don't queue.
	rec := do(h, "POST", "/downloads", url.Values{"model": {"huge-model:70b"}}, testSession)
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, `id="confirm-download-modal" data-dialog-autoopen`) {
		t.Fatalf("expected the confirmation dialog, got %d:\n%s", rec.Code, body)
	}
	for _, want := range []string{"Download huge-model:70b anyway?", "1.0 PB", "free where Ollama stores models", `name="confirm" value="1"`} {
		if !strings.Contains(body, want) {
			t.Errorf("dialog missing %q", want)
		}
	}
	if active, _ := testStore.ActiveDownloads(t.Context()); len(active) != 0 {
		t.Fatalf("nothing should be queued before confirming: %+v", active)
	}

	// Confirming queues it.
	rec = do(h, "POST", "/downloads", url.Values{"model": {"huge-model:70b"}, "confirm": {"1"}}, testSession)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("confirmed queue = %d\n%s", rec.Code, rec.Body)
	}
	if active, _ := testStore.ActiveDownloads(t.Context()); len(active) != 1 || active[0].Model != "huge-model:70b" {
		t.Errorf("confirmed download not queued: %+v", active)
	}

	// Models with no size concerns queue straight away.
	if rec := do(h, "POST", "/downloads", url.Values{"model": {"qwen3:8b"}}, testSession); rec.Code != http.StatusSeeOther {
		t.Errorf("normal queue = %d, want a redirect", rec.Code)
	}
}

func TestResourceConcerns(t *testing.T) {
	const gb = 1 << 30
	now := time.Now()
	nvidia := func(vramGB ...uint64) sysinfo.Snapshot {
		s := sysinfo.Snapshot{Time: now, MemTotal: 64 * gb}
		for _, v := range vramGB {
			s.GPUs = append(s.GPUs, sysinfo.GPU{MemTotal: v * gb, Util: -1})
		}
		return s
	}
	apple := sysinfo.Snapshot{Time: now, MemTotal: 24 * gb, GPUs: []sysinfo.GPU{{MemTotal: 24 * gb, Unified: true, Util: -1}}}
	noGPU := sysinfo.Snapshot{Time: now, MemTotal: 16 * gb}
	roomy := &disk.Usage{Free: 1000 * gb, Total: 2000 * gb}
	model := func(sizeGB int64) downloads.Checked { return downloads.Checked{Name: "m", Size: sizeGB * gb} }

	cases := []struct {
		name string
		c    downloads.Checked
		d    *disk.Usage
		snap sysinfo.Snapshot
		want []string // substrings, one per expected concern; nil = none
	}{
		{"fits in VRAM", model(20), roomy, nvidia(24), nil},
		{"exceeds VRAM", model(40), roomy, nvidia(24), []string{"exceeds this machine's total VRAM (24.0 GB across the GPU)"}},
		{"fits across GPUs", model(40), roomy, nvidia(24, 24), nil},
		{"exceeds several GPUs", model(60), roomy, nvidia(24, 24), []string{"48.0 GB across 2 GPUs"}},
		{"exceeds VRAM + RAM", model(100), roomy, nvidia(24), []string{"VRAM and system memory combined (24.0 GB + 64.0 GB)"}},
		{"unified memory", model(30), roomy, apple, []string{"24.0 GB of memory, which the CPU and GPU share"}},
		{"no GPU", model(20), roomy, noGPU, []string{"no GPU was detected"}},
		{"disk full too", model(40), &disk.Usage{Free: 10 * gb}, nvidia(24), []string{"only 10.0 GB is free", "total VRAM"}},
		{"already installed", downloads.Checked{Size: 100 * gb, Installed: true}, roomy, nvidia(24), nil},
		{"size unknown", downloads.Checked{}, roomy, nvidia(24), nil},
		{"hardware not sampled yet", model(100), roomy, sysinfo.Snapshot{}, nil},
	}
	for _, tc := range cases {
		got := resourceConcerns(tc.c, tc.d, tc.snap)
		if len(got) != len(tc.want) {
			t.Errorf("%s: got %q, want %d concern(s)", tc.name, got, len(tc.want))
			continue
		}
		for i, w := range tc.want {
			if !strings.Contains(got[i], w) {
				t.Errorf("%s: concern %d = %q, want it to mention %q", tc.name, i, got[i], w)
			}
		}
	}
}
