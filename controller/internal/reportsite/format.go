package reportsite

import (
	"fmt"
	"html/template"
	"strings"
	"time"
)

// funcs returns the template helpers. now supplies the clock for ages.
func funcs(now func() time.Time) template.FuncMap {
	return template.FuncMap{
		"ts":       func(t time.Time) string { return formatTime(t, "2006-01-02 15:04") },
		"tsSec":    func(t time.Time) string { return formatTime(t, "2006-01-02 15:04:05") },
		"rfc":      func(t time.Time) string { return t.UTC().Format(time.RFC3339) },
		"age":      func(t time.Time) string { return age(now(), t) },
		"ageShort": func(t time.Time) string { return ageShort(now(), t) },
		"dur":      formatDuration,
		"short":    shortDigest,
		"dash":     dash,
		"num":      func(v float64) string { return fmt.Sprintf("%.2f", v) },
		"signed":   func(v float64) string { return fmt.Sprintf("%+.2f", v) },
		"status":   func(kind, label string) Status { return Status{Kind: kind, Label: label} },
		"sel":      newSelect,
	}
}

// selectField is one filter drop-down.
type selectField struct {
	Name, Label, Current string
	Options              []string
}

func newSelect(name, label, current string, options []string) selectField {
	return selectField{Name: name, Label: label, Current: current, Options: options}
}

func formatTime(t time.Time, layout string) string {
	if t.IsZero() {
		return "–"
	}
	return t.UTC().Format(layout)
}

// age is the long relative form, for example "12 min ago".
func age(now, t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := now.Sub(t)
	switch {
	case d < 5*time.Second:
		return "just now"
	case d < time.Minute:
		return fmt.Sprintf("%d s ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d h ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%d d ago", int(d.Hours()/24))
	}
}

// ageShort is the compact form for table cells, for example "12m".
func ageShort(now, t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", max(int(d.Seconds()), 0))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// formatDuration renders d as "4m 12s"; zero is a dash.
func formatDuration(d time.Duration) string {
	d = d.Round(time.Second)
	switch {
	case d <= 0:
		return "–"
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm %02ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%dh %02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

// shortDigest keeps the algorithm and the first 12 hex characters.
func shortDigest(d string) string {
	algo, hex, ok := strings.Cut(d, ":")
	if !ok {
		return d
	}
	return algo + ":" + hex[:min(len(hex), 12)]
}

func dash(s string) string {
	if s == "" {
		return "–"
	}
	return s
}
