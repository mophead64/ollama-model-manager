package web

import (
	"encoding/json"
	"fmt"
	"html/template"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/mophead64/ollama-model-manager/internal/ollama"
	"github.com/mophead64/ollama-model-manager/internal/version"
)

var templateFuncs = template.FuncMap{
	"bytes":     formatBytes,
	"time":      formatTime,
	"shorthash": shortHash,
	"count":     formatCount,
	"sortcol":   func(label string, h sortHeader) map[string]any { return map[string]any{"Label": label, "H": h} },
	"int64":     func(n uint64) int64 { return int64(n) },
	"speed":     formatSpeed,
	"elideurls": elideURLQueries,
	"hasprefix": strings.HasPrefix,
	"contains":  strings.Contains,
	"eta":       humanDuration,
	"took":      took,
	"pct":       func(f float64) string { return fmt.Sprintf("%.1f", f) },
	"progress": func(completed, total int64) float64 {
		if total <= 0 {
			return 0
		}
		return min(100, 100*float64(completed)/float64(total))
	},
	"metric": func(label string, v *float64) map[string]any { return map[string]any{"Label": label, "V": v} },
	"chartcard": func(key, label, sub string) map[string]string {
		return map[string]string{"Key": key, "Label": label, "Sub": sub}
	},
	"sev":        severity,
	"keepalives": func() []keepAliveOption { return keepAliveOptions },
	"until":      formatUntil,
	"unloads":    unloadsPhrase,
	"ago":        timeAgo,
	"lastused":   lastUsedOf,
	"activefor":  formatActive,
	"loadsof":    func(loads map[string]int, name string) int { return loads[name] },
	"canchat":    canChat,
	"pickeritem": func(m ollama.Model, loaded bool) map[string]any { return map[string]any{"Model": m, "Loaded": loaded} },
	"rowactions": func(m ollama.Model, loaded, allowDelete bool, ret string, st *libraryStorage) map[string]any {
		return map[string]any{"Model": m, "Loaded": loaded, "AllowDelete": allowDelete, "Return": ret, "Storage": st}
	},
	"ms": formatMS,
	"copycmd": func(id, cmd string) map[string]string {
		return map[string]string{"ID": id, "Cmd": cmd}
	},
	"modelsrc": sourceOf,
	"quantsopener": func(name string, src modelSource, class ...string) map[string]any {
		return map[string]any{"Name": name, "Src": src, "Class": strings.Join(class, " ")}
	},
	"modeltabs": func(tab string, activeTests any) map[string]any {
		return map[string]any{"Tab": tab, "ActiveTests": activeTests}
	},
	"has":     func(list []string, s string) bool { return slices.Contains(list, s) },
	"add":     func(a, b int) int { return a + b },
	"compact": formatCompact,
	"tokens":  formatTokens,
	"sub":     func(a, b int) int { return a - b },
	"static":  staticURL,
	"navlogo": func() template.URL { return navLogo },
	// A build that isn't a release (a local or "dev" build) has nothing to
	// compare with the latest release.
	"devbuild": func(v string) bool { return !version.IsRelease(v) },
	"reply":    renderReply,
	// For a data- attribute a script reads; html/template escapes it.
	"json": func(v any) (string, error) {
		b, err := json.Marshal(v)
		return string(b), err
	},
}

// formatActive renders roughly how long a model was in use, from the usage
// tracker's seconds: "≈ 45 s", "≈ 12 min", "≈ 2.5 h".
func formatActive(seconds int) string {
	switch {
	case seconds < 60:
		return fmt.Sprintf("≈ %d s", seconds)
	case seconds < 3600:
		return fmt.Sprintf("≈ %d min", (seconds+30)/60)
	}
	return fmt.Sprintf("≈ %.1f h", float64(seconds)/3600)
}

// formatMS renders a duration in milliseconds (an int64 or float64, e.g. an
// average) briefly: "850 ms", "12.3s".
func formatMS(v any) string {
	var ms float64
	switch x := v.(type) {
	case int64:
		ms = float64(x)
	case float64:
		ms = x
	}
	if ms < 1000 {
		return fmt.Sprintf("%.0f ms", ms)
	}
	return fmt.Sprintf("%.1fs", ms/1000)
}

func formatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for n2 := n / unit; n2 >= unit; n2 /= unit {
		div *= unit
		exp++
	}
	units := "KMGTPE"
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), units[exp])
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Local().Format("2006-01-02 15:04:05")
}

func shortHash(h string) string {
	if len(h) <= 12 {
		return h
	}
	return h[:12]
}

// formatCount renders large integers with thousands separators (262144 ->
// 262,144), for context lengths and the like.
func formatCount(n int) string {
	s := strconv.Itoa(n)
	if n < 0 {
		return "-" + formatCount(-n)
	}
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// severity is the meter fill class for a usage percentage: amber when it's
// getting full, red when it nearly is.
func severity(pct float64) string {
	switch {
	case pct >= 90:
		return "low"
	case pct >= 75:
		return "warn"
	}
	return ""
}

// formatUntil describes when a loaded model will be unloaded. Ollama reports
// a date centuries away for models kept loaded indefinitely (keep_alive -1).
func formatUntil(t time.Time) string {
	d := time.Until(t)
	switch {
	case t.IsZero():
		return "-"
	case d > 100*365*24*time.Hour:
		return "never (kept loaded)"
	case d <= 0:
		return "now"
	}
	return "in " + humanDuration(d)
}

// timeAgo is a rough relative time, "just now" to "3 months ago": model usage
// is only known to within a poll, so more precision would mislead.
func timeAgo(t time.Time) string {
	d := time.Since(t)
	unit := func(n int, name string) string {
		if n == 1 {
			return "1 " + name + " ago"
		}
		return fmt.Sprintf("%d %ss ago", n, name)
	}
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return unit(int(d/time.Minute), "minute")
	case d < 24*time.Hour:
		return unit(int(d/time.Hour), "hour")
	case d < 30*24*time.Hour:
		return unit(int(d/(24*time.Hour)), "day")
	case d < 365*24*time.Hour:
		return unit(int(d/(30*24*time.Hour)), "month")
	}
	return unit(int(d/(365*24*time.Hour)), "year")
}

// lastUsedOf looks a model up in the last-used times, nil if never seen used
// (a missing map key would otherwise be a zero time, which templates treat as set).
func lastUsedOf(lastUsed map[string]time.Time, name string) *time.Time {
	t, ok := lastUsed[name]
	if !ok {
		return nil
	}
	return &t
}

// unloadsPhrase is formatUntil for running text: "unloads in 4m 0s",
// "stays loaded until unloaded", "unloading now".
func unloadsPhrase(t time.Time) string {
	switch s := formatUntil(t); s {
	case "never (kept loaded)":
		return "stays loaded until unloaded"
	case "now":
		return "unloading now"
	case "-":
		return ""
	default:
		return "unloads " + s
	}
}

func formatSpeed(bps float64) string {
	if bps <= 0 {
		return "-"
	}
	return formatBytes(int64(bps)) + "/s"
}

// took is how long a download ran: start to finish, or start to now while running.
func took(start, end *time.Time) string {
	if start == nil {
		return "-"
	}
	e := time.Now()
	if end != nil {
		e = *end
	}
	return humanDuration(e.Sub(*start))
}

// humanDuration renders 45s, 3m 12s, 2h 5m 12s, or 1d 4h 5m 12s.
func humanDuration(d time.Duration) string {
	s := int64(d.Round(time.Second) / time.Second)
	if s < 0 {
		s = 0
	}
	days, hours, mins, secs := s/86400, s%86400/3600, s%3600/60, s%60
	switch {
	case days > 0:
		return fmt.Sprintf("%dd %dh %dm %ds", days, hours, mins, secs)
	case hours > 0:
		return fmt.Sprintf("%dh %dm %ds", hours, mins, secs)
	case mins > 0:
		return fmt.Sprintf("%dm %ds", mins, secs)
	default:
		return fmt.Sprintf("%ds", secs)
	}
}

// urlQueryRE matches a URL's query string, stopping at whitespace or a quote.
var urlQueryRE = regexp.MustCompile(`(https?://[^\s"'?]+)\?[^\s"']+`)

// elideURLQueries shortens URLs in a message to scheme://host/path?…. Errors
// from Ollama can quote pre-signed CDN URLs whose query strings run to
// hundreds of characters of signatures and tokens; the host and path are the
// useful part.
func elideURLQueries(s string) string {
	return urlQueryRE.ReplaceAllString(s, "$1?…")
}
