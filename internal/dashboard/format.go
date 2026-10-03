package dashboard

import (
	"fmt"
	"html/template"
	"math"
	"strconv"
	"strings"
)

var funcs = template.FuncMap{
	"num":   formatInt,
	"float": formatFloat,
	"pctw":  func(p float64) string { return strconv.FormatFloat(p, 'f', 1, 64) },
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// formatInt adds thousands separators: 12345 becomes "12,345".
func formatInt(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

// formatFloat shows whole numbers without decimals and others with up to two.
func formatFloat(f float64) string {
	if f == math.Trunc(f) && math.Abs(f) < 1e15 {
		return formatInt(int64(f))
	}
	whole := math.Trunc(f)
	frac := strings.TrimRight(strings.TrimPrefix(strconv.FormatFloat(math.Abs(f-whole), 'f', 2, 64), "0"), "0")
	return formatInt(int64(whole)) + strings.TrimSuffix(frac, ".")
}

// formatDuration renders seconds as "45s", "3m 05s" or "1h 02m".
func formatDuration(seconds float64) string {
	s := int(seconds + 0.5)
	switch {
	case s < 60:
		return fmt.Sprintf("%ds", s)
	case s < 3600:
		return fmt.Sprintf("%dm %02ds", s/60, s%60)
	}
	return fmt.Sprintf("%dh %02dm", s/3600, s%3600/60)
}

// sparkline draws a small trend line. It has no axis: the overview shows the
// numbers beside it, and the site page has the full chart.
func sparkline(values []int64) template.HTML {
	const w, h, pad = 120.0, 28.0, 4.0
	var peak int64
	for _, v := range values {
		if v > peak {
			peak = v
		}
	}
	var pts strings.Builder
	var lastX, lastY float64
	for i, v := range values {
		x := pad + float64(i)/float64(len(values)-1)*(w-2*pad)
		y := h - pad
		if peak > 0 {
			y = h - pad - float64(v)/float64(peak)*(h-2*pad)
		}
		fmt.Fprintf(&pts, "%.1f,%.1f ", x, y)
		lastX, lastY = x, y
	}
	return template.HTML(fmt.Sprintf(
		`<svg class="spark" viewBox="0 0 %.0f %.0f" width="%.0f" height="%.0f" aria-hidden="true">`+
			`<polyline points="%s" fill="none"/><circle cx="%.1f" cy="%.1f" r="4"/></svg>`,
		w, h, w, h, strings.TrimSpace(pts.String()), lastX, lastY))
}
