package sysinfo

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
)

// nvidiaQuery is the field list asked of nvidia-smi, in the order parseNvidiaSMI reads them.
const nvidiaQuery = "index,name,driver_version,memory.total,memory.used,utilization.gpu"

// nvidiaGPUs lists NVIDIA GPUs via nvidia-smi. In a container it's only
// there when the NVIDIA Container Toolkit injects it (docker run --gpus all).
func nvidiaGPUs(ctx context.Context, run runner) ([]GPU, error) {
	if runtime.GOOS == "darwin" {
		return nil, nil // no NVIDIA driver for Apple hardware
	}
	if _, err := exec.LookPath("nvidia-smi"); err != nil {
		return nil, errors.New("For NVIDIA GPUs, nvidia-smi must be on the PATH (in Docker, run the container with --gpus all).")
	}
	out, err := run(ctx, "nvidia-smi", "--query-gpu="+nvidiaQuery, "--format=csv,noheader,nounits")
	if err != nil {
		return nil, fmt.Errorf("nvidia-smi failed: %v.", err)
	}
	return parseNvidiaSMI(out)
}

// parseNvidiaSMI reads nvidia-smi's CSV output (nounits: memory in MiB,
// utilisation in percent). Fields a GPU doesn't support read "[N/A]" or
// "[Not Supported]" and are left unknown.
func parseNvidiaSMI(out []byte) ([]GPU, error) {
	r := csv.NewReader(strings.NewReader(string(out)))
	r.TrimLeadingSpace = true
	rows, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("unexpected nvidia-smi output: %v.", err)
	}
	var gpus []GPU
	for _, f := range rows {
		if len(f) < 6 {
			continue
		}
		g := GPU{Vendor: "NVIDIA", Name: f[1], Driver: "NVIDIA " + f[2], Util: -1}
		if mib, ok := num(f[3]); ok {
			g.MemTotal = uint64(mib) << 20
		}
		if mib, ok := num(f[4]); ok {
			g.MemUsed = uint64(mib) << 20
		}
		if u, ok := num(f[5]); ok {
			g.Util = u
		}
		gpus = append(gpus, g)
	}
	return gpus, nil
}

func num(s string) (float64, bool) {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return v, err == nil
}
