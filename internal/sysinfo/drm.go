package sysinfo

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

// sysRoot is where sysfs and procfs are read from; tests point it at a fake tree.
var sysRoot = "/"

var cardRE = regexp.MustCompile(`^card\d+$`)

// PCI vendor IDs.
const (
	vendorAMD    = "0x1002"
	vendorIntel  = "0x8086"
	vendorNVIDIA = "0x10de"
)

// virtualVendors are display adapters that aren't compute GPUs: VM virtual
// graphics and server BMCs. They'd only clutter the list.
var virtualVendors = map[string]bool{
	"0x1af4": true, // virtio
	"0x1234": true, // QEMU/Bochs
	"0x15ad": true, // VMware
	"0x1414": true, // Hyper-V
	"0x80ee": true, // VirtualBox
	"0x1a03": true, // ASPEED BMC
	"0x102b": true, // Matrox server graphics
}

// drmGPUs lists the GPUs Linux exposes under /sys/class/drm. amdgpu reports
// VRAM and busy percentage there; other drivers (Intel, NVIDIA's) only give
// the device's identity. NVIDIA cards are skipped when nvidia-smi already
// described them.
func drmGPUs(haveNvidia bool) []GPU {
	if runtime.GOOS != "linux" {
		return nil
	}
	return scanDRM(sysRoot, haveNvidia)
}

func scanDRM(root string, skipNvidia bool) []GPU {
	base := filepath.Join(root, "sys/class/drm")
	entries, err := os.ReadDir(base)
	if err != nil {
		return nil
	}
	kernel := readTrim(filepath.Join(root, "proc/sys/kernel/osrelease"))
	var gpus []GPU
	for _, e := range entries {
		if !cardRE.MatchString(e.Name()) {
			continue // connectors (card0-DP-1) and render nodes
		}
		dev := filepath.Join(base, e.Name(), "device")
		vendor := readTrim(filepath.Join(dev, "vendor"))
		if vendor == "" || virtualVendors[vendor] || (vendor == vendorNVIDIA && skipNvidia) {
			continue
		}
		g := GPU{Util: -1, Vendor: vendorName(vendor)}

		g.Name = readTrim(filepath.Join(dev, "product_name"))
		if g.Name == "" {
			g.Name = g.Vendor + " GPU"
			if id := readTrim(filepath.Join(dev, "device")); id != "" {
				g.Name += " (device " + id + ")"
			}
		}

		if link, err := os.Readlink(filepath.Join(dev, "driver")); err == nil {
			drv := filepath.Base(link)
			g.Driver = drv
			if v := readTrim(filepath.Join(root, "sys/module", drv, "version")); v != "" {
				g.Driver += " " + v
			} else if kernel != "" {
				g.Driver += " (Linux " + kernel + ")" // in-tree driver: versioned with the kernel
			}
		}

		if n, ok := readUint(filepath.Join(dev, "mem_info_vram_total")); ok {
			g.MemTotal = n
			g.MemUsed, _ = readUint(filepath.Join(dev, "mem_info_vram_used"))
		}
		if n, ok := readUint(filepath.Join(dev, "gpu_busy_percent")); ok {
			g.Util = float64(n)
		}
		gpus = append(gpus, g)
	}
	return gpus
}

func vendorName(id string) string {
	switch id {
	case vendorAMD:
		return "AMD"
	case vendorIntel:
		return "Intel"
	case vendorNVIDIA:
		return "NVIDIA"
	}
	return "Other"
}

func readTrim(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func readUint(path string) (uint64, bool) {
	n, err := strconv.ParseUint(readTrim(path), 10, 64)
	return n, err == nil
}
