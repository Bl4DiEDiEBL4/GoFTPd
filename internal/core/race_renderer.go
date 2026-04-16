package core

import (
	"fmt"
	"io"
	"strings"
)

const (
	cpTL = byte(0xDA)
	cpTR = byte(0xBF)
	cpBL = byte(0xC0)
	cpBR = byte(0xD9)
	cpHZ = byte(0xC4)
	cpVT = byte(0xB3)
	cpLT = byte(0xC3)
	cpRT = byte(0xB4)
	cpCR = byte(0xC5)
)

func clean(s string, max int) string {
	s = strings.TrimSpace(s)
	if s == "" {
		s = "-"
	}
	if len(s) > max {
		s = s[:max]
	}
	return s
}

func pct(p int) int {
	if p < 0 {
		return 0
	}
	if p > 100 {
		return 100
	}
	return p
}

func speed(v float64) string {
	if v <= 0 {
		return "-"
	}
	return fmt.Sprintf("%.1fMB/s", v/1024.0/1024.0)
}

func line(l, fill, r byte, n int) []byte {
	b := make([]byte, 0, n+2)
	b = append(b, l)
	for i := 0; i < n; i++ {
		b = append(b, fill)
	}
	b = append(b, r)
	return b
}

func sep(widths ...int) []byte {
	out := []byte{cpLT}
	for i, w := range widths {
		for j := 0; j < w; j++ {
			out = append(out, cpHZ)
		}
		if i != len(widths)-1 {
			out = append(out, cpCR)
		}
	}
	out = append(out, cpRT)
	return out
}

func row(w io.Writer, cols ...string) {
	widths := []int{32, 7, 8, 11, 8}

	buf := []byte{cpVT}
	for i, c := range cols {
		buf = append(buf, []byte(fmt.Sprintf("%-*s", widths[i], c))...)
		buf = append(buf, cpVT)
	}
	buf = append(buf, '\r', '\n')
	w.Write(buf)
}

func raw(w io.Writer, b []byte) {
	w.Write(append(b, '\r', '\n'))
}

func text(w io.Writer, s string, width int) {
	buf := []byte{cpVT}
	buf = append(buf, []byte(fmt.Sprintf("%-*s", width, s))...)
	buf = append(buf, cpVT, '\r', '\n')
	w.Write(buf)
}

func RenderRaceStats(w io.Writer, users []VFSRaceUser, groups []VFSRaceGroup, totalBytes int64, present, total int, version string) {

	if present < 0 {
		present = 0
	}
	if total < 0 {
		total = 0
	}
	if total > 0 && present > total {
		present = total
	}

	totalPct := 0
	if total > 0 {
		totalPct = pct((present * 100) / total)
	}

	totalSpeed := 0.0
	for _, u := range users {
		totalSpeed += u.Speed
	}

	totalMB := float64(totalBytes) / (1024 * 1024)

	width := 70

	raw(w, line(cpTL, cpHZ, cpTR, width))
	text(w, fmt.Sprintf(" GoFTPd %s :: Race Stats ", version), width)
	text(w, "", width)

text(w, "   ____       _____ _____ ____     _  ", width)
text(w, "  / ___| ___ |  ___|_   _|  _ \\ __| | ", width)
text(w, " | |  _ / _ \\| |_    | | | |_) / _` | ", width)
text(w, " | |_| | (_) |  _|   | | |  __/ (_| | ", width)
text(w, "  \\____|\\___/|_|     |_| |_|   \\__,_| ", width)
text(w, "", width)                


	raw(w, sep(32, 7, 8, 11, 8))
	row(w, "Users", "Files", "Size", "Speed", "%")
	raw(w, sep(32, 7, 8, 11, 8))

	for i, u := range users {
		label := fmt.Sprintf("#%-2d %-10s/%-10s",
			i+1,
			clean(u.Name, 14),
			clean(u.Group, 11),
		)

		row(
			w,
			label,
			fmt.Sprintf("%dF", u.Files),
			fmt.Sprintf("%.1fM", float64(u.Bytes)/(1024*1024)),
			speed(u.Speed),
			fmt.Sprintf("%d%%", pct(u.Percent)),
		)
	}

	if len(groups) > 0 {
		raw(w, sep(32, 7, 8, 11, 8))
		row(w, "Groups", "Files", "Size", "Speed", "%")
		raw(w, sep(32, 7, 8, 11, 8))

		for i, g := range groups {
			label := fmt.Sprintf("#%-2d %-28s",
				i+1,
				clean(g.Name, 28),
			)

			row(
				w,
				label,
				fmt.Sprintf("%dF", g.Files),
				fmt.Sprintf("%.1fM", float64(g.Bytes)/(1024*1024)),
				speed(g.Speed),
				fmt.Sprintf("%d%%", pct(g.Percent)),
			)
		}
	}

	raw(w, sep(32, 7, 8, 11, 8))
	row(
		w,
		"TOTAL",
		fmt.Sprintf("%d/%dF", present, total),
		fmt.Sprintf("%.1fM", totalMB),
		speed(totalSpeed),
		fmt.Sprintf("%d%%", totalPct),
	)

	raw(w, line(cpBL, cpHZ, cpBR, width))
}