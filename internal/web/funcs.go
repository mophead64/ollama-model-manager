package web

import (
	"fmt"
	"html/template"
	"regexp"
	"strconv"
	"strings"
	"time"
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
	"sev":   severity,
	"until": formatUntil,
	"add":   func(a, b int) int { return a + b },
	"sub":   func(a, b int) int { return a - b },
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

// humanDuration renders 45s, 3m 12s, 2h 5m 12s, or 1d 4h 5m 12s (from Deduper).
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
