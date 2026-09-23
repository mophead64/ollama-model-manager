package web

import (
	"fmt"
	"slices"
	"strings"

	"github.com/mophead64/ollama-model-manager/internal/library"
	"github.com/mophead64/ollama-model-manager/internal/sysinfo"
)

// Rough allowances for estimating whether a model will run here.
const (
	// Memory a loaded model takes beyond its file: KV cache for a default
	// context window, compute buffers and the runtime.
	fitOverhead = 1.2
	// Bytes per parameter for the quantisations Ollama's default tags use
	// (Q4_K_M is ~4.8 bits per weight).
	bytesPerParam = 0.6
	// Share of unified memory macOS lets the GPU use by default.
	unifiedGPUShare = 0.75
)

// fit is a verdict on whether a model of some size will run on this machine.
type fit struct {
	Level  string // "gpu", "partial", "cpu", "no", "cloud" or "unknown"
	Label  string // short, for a badge
	Detail string // a sentence, for a tooltip
}

// Runs reports whether the model should load at all (maybe slowly).
func (f fit) Runs() bool { return f.Level == "gpu" || f.Level == "partial" || f.Level == "cpu" }

// Symbol marks the level without relying on colour.
func (f fit) Symbol() string {
	switch f.Level {
	case "gpu":
		return "✓"
	case "partial", "cpu":
		return "◐"
	case "no":
		return "✗"
	case "cloud":
		return "☁"
	}
	return "?"
}

// Badge is the CSS badge class for the level.
func (f fit) Badge() string {
	switch f.Level {
	case "gpu":
		return "on"
	case "partial", "cpu":
		return "warn"
	case "no":
		return "error"
	case "cloud":
		return "cap"
	}
	return "info"
}

// estimateFit judges a model whose file is size bytes against snap.
func estimateFit(size int64, snap sysinfo.Snapshot) fit {
	if size <= 0 {
		return fit{Level: "unknown", Label: "Size unknown", Detail: "ollama.com doesn't list a size for this."}
	}
	if snap.Time.IsZero() || snap.MemTotal == 0 {
		return fit{Level: "unknown", Label: "Hardware unknown", Detail: "This machine's memory hasn't been read yet."}
	}
	need := uint64(float64(size) * fitOverhead)
	needS := "~" + formatBytes(int64(need))
	ram := snap.MemTotal
	_, vram, hasVRAM := snap.VRAM()
	unified := slices.ContainsFunc(snap.GPUs, func(g sysinfo.GPU) bool { return g.Unified })

	switch {
	case unified:
		gpuMem := uint64(float64(ram) * unifiedGPUShare)
		switch {
		case need <= gpuMem:
			return fit{"gpu", "Fits", fmt.Sprintf("Needs %s; the GPU can use about %s of this machine's %s shared memory.", needS, formatBytes(int64(gpuMem)), formatBytes(int64(ram)))}
		case need <= ram:
			return fit{"partial", "Tight fit", fmt.Sprintf("Needs %s, more than the ~%s the GPU can use by default, so part of it runs on the CPU and leaves little memory for anything else.", needS, formatBytes(int64(gpuMem)))}
		}
		return fit{"no", "Too big", fmt.Sprintf("Needs %s, more than this machine's %s of memory.", needS, formatBytes(int64(ram)))}
	case !hasVRAM:
		if need <= ram {
			what := "no GPU was detected"
			if len(snap.GPUs) > 0 {
				what = "the GPU's memory couldn't be read"
			}
			return fit{"cpu", "CPU only", fmt.Sprintf("Needs %s of the %s memory; %s, so expect it to run on the CPU, slowly.", needS, formatBytes(int64(ram)), what)}
		}
		return fit{"no", "Too big", fmt.Sprintf("Needs %s, more than this machine's %s of memory.", needS, formatBytes(int64(ram)))}
	case need <= vram:
		return fit{"gpu", "Fits in VRAM", fmt.Sprintf("Needs %s of the %s VRAM, so it runs fully on the GPU.", needS, formatBytes(int64(vram)))}
	case need <= vram+ram:
		return fit{"partial", "Partly on CPU", fmt.Sprintf("Needs %s, more than the %s VRAM, so Ollama runs part of it on the CPU, which is much slower.", needS, formatBytes(int64(vram)))}
	}
	return fit{"no", "Too big", fmt.Sprintf("Needs %s, more than this machine's VRAM and memory combined (%s + %s).", needS, formatBytes(int64(vram)), formatBytes(int64(ram)))}
}

// tagFit is estimateFit for a library tag, accounting for builds that only
// run on some hardware.
func tagFit(t library.Tag, snap sysinfo.Snapshot) fit {
	if t.Cloud() {
		return fit{Level: "cloud", Label: "Cloud", Detail: "Runs on Ollama's cloud, not this machine; needs an ollama.com account."}
	}
	_, tag, _ := strings.Cut(t.Name, ":")
	if strings.Contains(tag, "mlx") && !slices.ContainsFunc(snap.GPUs, func(g sysinfo.GPU) bool { return g.Unified }) {
		return fit{Level: "no", Label: "Apple silicon only", Detail: "An MLX build, which runs only on Macs with Apple silicon."}
	}
	return estimateFit(t.Size, snap)
}

// sizeFit estimates for a search result's parameter-count label ("8b"),
// assuming the default 4-bit quantisation.
func sizeFit(label string, snap sysinfo.Snapshot) fit {
	f := estimateFit(int64(library.ParamCount(label)*bytesPerParam), snap)
	if f.Level != "unknown" {
		f.Detail = "Estimated for the default 4-bit download. " + f.Detail
	}
	return f
}
