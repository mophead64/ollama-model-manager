package sysinfo

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseNvidiaSMI(t *testing.T) {
	out := "0, NVIDIA GeForce RTX 4090, 550.54.14, 24564, 20001, 87\n" +
		"1, NVIDIA A100-SXM4-80GB, 550.54.14, 81920, [N/A], [Not Supported]\n"
	gpus, err := parseNvidiaSMI([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	if len(gpus) != 2 {
		t.Fatalf("got %d GPUs, want 2", len(gpus))
	}
	g := gpus[0]
	if g.Name != "NVIDIA GeForce RTX 4090" || g.Driver != "NVIDIA 550.54.14" || g.Util != 87 ||
		g.MemTotal != 24564<<20 || g.MemUsed != 20001<<20 {
		t.Errorf("gpu 0 = %+v", g)
	}
	if g := gpus[1]; g.HasUtil() || g.MemUsed != 0 || g.MemTotal != 81920<<20 {
		t.Errorf("unsupported fields should be unknown: %+v", g)
	}
}

func TestParseIOReg(t *testing.T) {
	out := `+-o AGXAcceleratorG16G  <class AGXAcceleratorG16G, id 0x100000abc>
    {
      "PerformanceStatistics" = {"In use system memory (driver)"=0,"Alloc system memory"=3655057408,"Device Utilization %"=42,"In use system memory"=449658880}
      "model" = "Apple M4"
      "gpu-core-count" = 10
    }
`
	gpus := parseIOReg(out, "Metal", 24<<30)
	if len(gpus) != 1 {
		t.Fatalf("got %d GPUs, want 1", len(gpus))
	}
	g := gpus[0]
	if g.Name != "Apple M4" || g.Vendor != "Apple" || g.Cores != 10 || g.Util != 42 ||
		g.MemUsed != 449658880 || g.MemTotal != 24<<30 || !g.Unified {
		t.Errorf("gpu = %+v", g)
	}
}

func TestScanDRM(t *testing.T) {
	root := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("proc/sys/kernel/osrelease", "6.8.0-45-generic")
	// An AMD card with amdgpu's counters.
	write("sys/class/drm/card0/device/vendor", "0x1002")
	write("sys/class/drm/card0/device/device", "0x744c")
	write("sys/class/drm/card0/device/mem_info_vram_total", "25753026560")
	write("sys/class/drm/card0/device/mem_info_vram_used", "12876513280")
	write("sys/class/drm/card0/device/gpu_busy_percent", "63")
	os.MkdirAll(filepath.Join(root, "sys/bus/pci/drivers/amdgpu"), 0o755)
	if err := os.Symlink("../../../bus/pci/drivers/amdgpu", filepath.Join(root, "sys/class/drm/card0/device/driver")); err != nil {
		t.Fatal(err)
	}
	// A connector, a VM display adapter and an NVIDIA card: all skipped.
	write("sys/class/drm/card0-DP-1/status", "connected")
	write("sys/class/drm/card1/device/vendor", "0x1af4")
	write("sys/class/drm/card2/device/vendor", "0x10de")

	gpus := scanDRM(root, true)
	if len(gpus) != 1 {
		t.Fatalf("got %d GPUs, want 1: %+v", len(gpus), gpus)
	}
	g := gpus[0]
	if g.Vendor != "AMD" || g.Name != "AMD GPU (device 0x744c)" || g.Driver != "amdgpu (Linux 6.8.0-45-generic)" ||
		g.Util != 63 || g.MemPercent() != 50 {
		t.Errorf("gpu = %+v", g)
	}

	// Without nvidia-smi, the NVIDIA card is still listed by identity.
	if gpus := scanDRM(root, false); len(gpus) != 2 || gpus[1].Vendor != "NVIDIA" || gpus[1].HasUtil() {
		t.Errorf("NVIDIA card should be listed without stats: %+v", gpus)
	}
}

func TestSnapshotAggregates(t *testing.T) {
	s := Snapshot{GPUs: []GPU{
		{MemTotal: 100, MemUsed: 50, Util: 20},
		{MemTotal: 300, MemUsed: 50, Util: 60},
		{Util: -1}, // identity only
	}}
	if u, ok := s.GPUUtil(); !ok || u != 40 {
		t.Errorf("GPUUtil = %v, %v; want 40", u, ok)
	}
	if p, ok := s.VRAMPercent(); !ok || p != 25 {
		t.Errorf("VRAMPercent = %v, %v; want 25", p, ok)
	}

	// Unified-memory GPUs share RAM, so it's only counted once.
	s = Snapshot{GPUs: []GPU{{MemTotal: 100, MemUsed: 10, Unified: true}, {MemTotal: 100, MemUsed: 10, Unified: true}}}
	if used, total, _ := s.VRAM(); used != 10 || total != 100 {
		t.Errorf("unified VRAM = %d/%d, want 10/100", used, total)
	}

	if _, ok := (Snapshot{}).GPUUtil(); ok {
		t.Error("no GPUs should mean no utilisation")
	}
	s = Snapshot{MemTotal: 400, MemUsed: 100, GPUs: []GPU{{MemTotal: 200, MemUsed: 50, Util: -1}}}
	if smp := s.Sample(); smp.MemUsed != 100 || smp.MemTotal != 400 || smp.VRAMUsed != 50 || smp.VRAMTotal != 200 || *smp.VRAM != 25 {
		t.Errorf("sample bytes = %+v", smp)
	}
	if smp := (Snapshot{CPU: -1}).Sample(); smp.CPU != nil || smp.GPU != nil || smp.Mem != nil {
		t.Errorf("unknown metrics should be nil: %+v", smp)
	}
}

func TestSamplerKeepsWindow(t *testing.T) {
	s := New(time.Second, 3*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	s.run = func(context.Context, string, ...string) ([]byte, error) { return nil, os.ErrNotExist }
	for range 5 {
		s.collect(context.Background())
	}
	if n := len(s.History()); n != 3 {
		t.Errorf("history length = %d, want 3", n)
	}
	if s.Latest().Time.IsZero() {
		t.Error("latest snapshot not recorded")
	}
}
