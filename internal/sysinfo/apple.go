package sysinfo

import (
	"context"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
)

var (
	iorModelRE = regexp.MustCompile(`"model" = "([^"]*)"`)
	iorCoresRE = regexp.MustCompile(`"gpu-core-count" = (\d+)`)
	iorUtilRE  = regexp.MustCompile(`"Device Utilization %"=(\d+)`)
	iorMemRE   = regexp.MustCompile(`"In use system memory"=(\d+)`)
)

var macVersion = sync.OnceValue(func() string {
	out, err := execRunner(context.Background(), "sw_vers", "-productVersion")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
})

// appleGPUs reads the GPU's live statistics from the IOAccelerator registry
// entry, which needs no special privileges. Only works running natively on
// macOS: Docker on a Mac runs Linux in a VM with no GPU access.
func appleGPUs(ctx context.Context, run runner, memTotal uint64) []GPU {
	if runtime.GOOS != "darwin" {
		return nil
	}
	out, err := run(ctx, "ioreg", "-r", "-d", "1", "-w", "0", "-c", "IOAccelerator")
	if err != nil {
		return nil
	}
	driver := "Metal"
	if v := macVersion(); v != "" {
		driver += " (macOS " + v + ")"
	}
	return parseIOReg(string(out), driver, memTotal)
}

// parseIOReg splits ioreg output into one block per accelerator ("+-o" lines
// start each) and pulls the model, core count and performance counters from
// each. Unified memory: the GPU's "VRAM" is system RAM.
func parseIOReg(out, driver string, memTotal uint64) []GPU {
	var gpus []GPU
	for _, block := range strings.Split(out, "+-o ") {
		m := iorModelRE.FindStringSubmatch(block)
		if m == nil {
			continue
		}
		g := GPU{Vendor: "Apple", Name: m[1], Driver: driver, Util: -1, Unified: true, MemTotal: memTotal}
		if !strings.HasPrefix(g.Name, "Apple") {
			g.Vendor = vendorFromName(g.Name) // Intel Macs: AMD/Intel GPUs, not unified
			g.Unified = false
			g.MemTotal = 0
		}
		if c := iorCoresRE.FindStringSubmatch(block); c != nil {
			g.Cores, _ = strconv.Atoi(c[1])
		}
		if u := iorUtilRE.FindStringSubmatch(block); u != nil {
			g.Util, _ = strconv.ParseFloat(u[1], 64)
		}
		if g.Unified {
			if mu := iorMemRE.FindStringSubmatch(block); mu != nil {
				g.MemUsed, _ = strconv.ParseUint(mu[1], 10, 64)
			}
		}
		gpus = append(gpus, g)
	}
	return gpus
}

func vendorFromName(name string) string {
	for _, v := range []string{"AMD", "Intel", "NVIDIA"} {
		if strings.Contains(strings.ToUpper(name), strings.ToUpper(v)) {
			return v
		}
	}
	return "Other"
}
