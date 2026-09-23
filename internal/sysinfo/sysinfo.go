// Package sysinfo samples the load on the machine this app runs on: CPU,
// memory and GPUs (NVIDIA via nvidia-smi, AMD/Intel via Linux sysfs, Apple
// silicon via ioreg). A Sampler polls in the background and keeps a short
// history for the live graphs.
package sysinfo

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/mem"
)

// GPU is one graphics device's identity and current load. Fields the
// platform can't report are left zero, with Util -1.
type GPU struct {
	Index    int
	Vendor   string // NVIDIA, AMD, Intel, Apple...
	Name     string
	Driver   string
	Cores    int     // GPU cores, where the platform reports them (Apple)
	MemTotal uint64  // bytes of VRAM; for unified memory, system RAM
	MemUsed  uint64  // bytes
	Util     float64 // busy percent, 0-100; -1 if unknown
	Unified  bool    // shares system memory with the CPU (Apple silicon)
}

// HasMem reports whether the GPU's memory size is known.
func (g GPU) HasMem() bool { return g.MemTotal > 0 }

// MemPercent is VRAM used, 0-100.
func (g GPU) MemPercent() float64 { return percent(g.MemUsed, g.MemTotal) }

// HasUtil reports whether the GPU's busy percentage is known.
func (g GPU) HasUtil() bool { return g.Util >= 0 }

// Snapshot is the machine's state at one moment.
type Snapshot struct {
	Time     time.Time
	Host     string // hostname (a container ID when containerised)
	Platform string // e.g. "linux (ubuntu 24.04)", "darwin 15.6"
	CPUModel string
	CPUCores int     // logical cores
	CPU      float64 // busy percent across all cores; -1 until the second sample
	MemTotal uint64
	MemUsed  uint64
	GPUs     []GPU
	// GPUNote says why no GPUs (or only some details) could be read, e.g.
	// nvidia-smi not being on the PATH. Empty when detection went fine.
	GPUNote string
}

func (s Snapshot) HasCPU() bool        { return s.CPU >= 0 }
func (s Snapshot) MemPercent() float64 { return percent(s.MemUsed, s.MemTotal) }

// GPUUtil is the mean busy percentage of the GPUs that report one.
func (s Snapshot) GPUUtil() (float64, bool) {
	var sum float64
	n := 0
	for _, g := range s.GPUs {
		if g.HasUtil() {
			sum += g.Util
			n++
		}
	}
	if n == 0 {
		return 0, false
	}
	return sum / float64(n), true
}

// VRAM totals memory across every GPU that reports it. Unified-memory GPUs
// count once however many there are, since they all share system RAM.
func (s Snapshot) VRAM() (used, total uint64, ok bool) {
	unified := false
	for _, g := range s.GPUs {
		if !g.HasMem() {
			continue
		}
		if g.Unified {
			if unified {
				continue
			}
			unified = true
		}
		used += g.MemUsed
		total += g.MemTotal
		ok = true
	}
	return used, total, ok
}

// VRAMPercent is VRAM used across all GPUs, 0-100.
func (s Snapshot) VRAMPercent() (float64, bool) {
	used, total, ok := s.VRAM()
	return percent(used, total), ok
}

// Sample is one point of history for the graphs. Metrics that weren't
// available are nil (null in JSON).
type Sample struct {
	T    int64    `json:"t"` // Unix milliseconds
	CPU  *float64 `json:"cpu"`
	Mem  *float64 `json:"mem"`
	GPU  *float64 `json:"gpu"`
	VRAM *float64 `json:"vram"`
	// Byte counts behind the Mem and VRAM percentages, for the graphs'
	// tooltips. Omitted when unknown.
	MemUsed   uint64 `json:"mem_used,omitempty"`
	MemTotal  uint64 `json:"mem_total,omitempty"`
	VRAMUsed  uint64 `json:"vram_used,omitempty"`
	VRAMTotal uint64 `json:"vram_total,omitempty"`
}

// Sample summarises the snapshot as the four headline percentages.
func (s Snapshot) Sample() Sample {
	opt := func(v float64, ok bool) *float64 {
		if !ok {
			return nil
		}
		v = float64(int(v*10+0.5)) / 10 // one decimal place is plenty for a graph
		return &v
	}
	gpu, gpuOK := s.GPUUtil()
	vramUsed, vramTotal, vramOK := s.VRAM()
	return Sample{
		T:         s.Time.UnixMilli(),
		CPU:       opt(s.CPU, s.HasCPU()),
		Mem:       opt(s.MemPercent(), s.MemTotal > 0),
		GPU:       opt(gpu, gpuOK),
		VRAM:      opt(percent(vramUsed, vramTotal), vramOK),
		MemUsed:   s.MemUsed,
		MemTotal:  s.MemTotal,
		VRAMUsed:  vramUsed,
		VRAMTotal: vramTotal,
	}
}

// Sampler collects a Snapshot every interval and remembers the last few
// minutes of them.
type Sampler struct {
	interval time.Duration
	keep     int
	log      *slog.Logger
	run      runner

	mu      sync.RWMutex
	latest  Snapshot
	history []Sample // oldest first, at most keep long

	static struct { // details that don't change while we run
		host, platform, cpuModel string
		cores                    int
	}
}

// New returns a Sampler that samples every interval and keeps window's worth
// of history. Call Run to start it.
func New(interval, window time.Duration, log *slog.Logger) *Sampler {
	s := &Sampler{
		interval: interval,
		keep:     max(2, int(window/interval)),
		log:      log,
		run:      execRunner,
	}
	s.latest = Snapshot{CPU: -1}
	return s
}

// Interval is how often a new sample is taken.
func (s *Sampler) Interval() time.Duration { return s.interval }

// Window is how much history is kept.
func (s *Sampler) Window() time.Duration { return time.Duration(s.keep) * s.interval }

// Run samples until ctx is cancelled.
func (s *Sampler) Run(ctx context.Context) {
	s.loadStatic(ctx)
	// Primes the CPU counters: gopsutil measures busy time since its previous
	// call, so the first real sample is taken one interval later.
	cpu.PercentWithContext(ctx, 0, false)
	t := time.NewTicker(s.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.collect(ctx)
		}
	}
}

// Latest returns the most recent snapshot (with CPU -1 before the first).
func (s *Sampler) Latest() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.latest
}

// History returns the retained samples, oldest first.
func (s *Sampler) History() []Sample {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]Sample{}, s.history...) // never nil, so it encodes as []
}

func (s *Sampler) loadStatic(ctx context.Context) {
	s.static.host, _ = os.Hostname()
	s.static.platform = runtime.GOOS
	if hi, err := host.InfoWithContext(ctx); err == nil {
		s.static.platform = hi.OS
		if hi.Platform != "" && hi.Platform != hi.OS {
			s.static.platform += " (" + hi.Platform + " " + hi.PlatformVersion + ")"
		} else if hi.PlatformVersion != "" {
			s.static.platform += " " + hi.PlatformVersion
		}
	}
	if infos, err := cpu.InfoWithContext(ctx); err == nil && len(infos) > 0 {
		s.static.cpuModel = infos[0].ModelName
	}
	s.static.cores = runtime.NumCPU()
	if n, err := cpu.CountsWithContext(ctx, true); err == nil && n > 0 {
		s.static.cores = n
	}
}

func (s *Sampler) collect(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, max(s.interval, 3*time.Second))
	defer cancel()

	snap := Snapshot{
		Time:     time.Now(),
		Host:     s.static.host,
		Platform: s.static.platform,
		CPUModel: s.static.cpuModel,
		CPUCores: s.static.cores,
		CPU:      -1,
	}
	if p, err := cpu.PercentWithContext(ctx, 0, false); err == nil && len(p) == 1 {
		snap.CPU = p[0]
	}
	if vm, err := mem.VirtualMemoryWithContext(ctx); err == nil {
		snap.MemTotal, snap.MemUsed = vm.Total, vm.Used
	}
	snap.GPUs, snap.GPUNote = s.gpus(ctx, snap.MemTotal)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.latest = snap
	s.history = append(s.history, snap.Sample())
	if over := len(s.history) - s.keep; over > 0 {
		s.history = append(s.history[:0], s.history[over:]...)
	}
}

// gpus asks every probe that applies here for devices. memTotal is system RAM,
// which unified-memory GPUs report as their VRAM.
func (s *Sampler) gpus(ctx context.Context, memTotal uint64) ([]GPU, string) {
	var gpus []GPU
	var notes []string

	nv, err := nvidiaGPUs(ctx, s.run)
	if err != nil {
		notes = append(notes, err.Error())
	}
	gpus = append(gpus, nv...)
	gpus = append(gpus, drmGPUs(len(nv) > 0)...)
	gpus = append(gpus, appleGPUs(ctx, s.run, memTotal)...)

	for i := range gpus {
		gpus[i].Index = i
	}
	note := ""
	if len(gpus) == 0 {
		note = "No GPU detected."
		for _, n := range notes {
			note += " " + n
		}
	}
	return gpus, note
}

// runner runs a command and returns its stdout. Swapped out in tests.
type runner func(ctx context.Context, name string, args ...string) ([]byte, error)

func execRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

func percent(used, total uint64) float64 {
	if total == 0 {
		return 0
	}
	return min(100, 100*float64(used)/float64(total))
}
