package core

import (
	"bytes"
	"fmt"
	"io"
	"strings"
)

const (
	cpTL = byte(0xDA) // top-left corner
	cpTR = byte(0xBF) // top-right corner
	cpBL = byte(0xC0) // bottom-left corner
	cpBR = byte(0xD9) // bottom-right corner
	cpHZ = byte(0xC4) // horizontal line
	cpVT = byte(0xB3) // vertical line
	cpLT = byte(0xC3) // left-T (prongs: right + up + down)
	cpRT = byte(0xB4) // right-T (prongs: left + up + down)
	cpCR = byte(0xC5) // cross (prongs all 4 directions)
	cpTD = byte(0xC2) // top-T (prong pointing down only)
	cpBU = byte(0xC1) // bottom-T (prong pointing up only)
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

func raceSize(sizeBytes int64) string {
	if sizeBytes <= 0 {
		return "-"
	}
	value := float64(sizeBytes)
	const (
		mb = 1024 * 1024
		gb = 1024 * 1024 * 1024
		tb = 1024 * 1024 * 1024 * 1024
	)
	switch {
	case value >= tb:
		return fmt.Sprintf("%.2fT", value/tb)
	case value >= gb:
		return fmt.Sprintf("%.2fG", value/gb)
	default:
		return fmt.Sprintf("%.1fM", value/mb)
	}
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

// sep draws a row separator using the given left-edge, junction, and right-edge
// connector characters. Examples:
//
//	sep(cpLT, cpCR, cpRT, ...)   → row between two rows that both have columns
//	sep(cpLT, cpTD, cpRT, ...)   → row below a single-wide block, above columns
//	sep(cpLT, cpBU, cpRT, ...)   → row below columns, above a single-wide block
func sep(leftEdge, junction, rightEdge byte, widths ...int) []byte {
	out := []byte{leftEdge}
	for i, w := range widths {
		for j := 0; j < w; j++ {
			out = append(out, cpHZ)
		}
		if i != len(widths)-1 {
			out = append(out, junction)
		}
	}
	out = append(out, rightEdge)
	return out
}

func row(w io.Writer, cols ...string) {
	widths := []int{28, 11, 8, 11, 8}

	buf := []byte{cpVT}
	for i, c := range cols {
		cell := clean(c, widths[i])
		buf = append(buf, []byte(fmt.Sprintf("%-*s", widths[i], cell))...)
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

func HasRaceStats(users []VFSRaceUser, groups []VFSRaceGroup, totalBytes int64, present, total int) bool {
	if len(users) > 0 || len(groups) > 0 {
		return true
	}
	if totalBytes > 0 {
		return true
	}
	if present > 0 || total > 0 {
		return true
	}
	return false
}

func RenderCompactRaceStats(w io.Writer, users []VFSRaceUser, groups []VFSRaceGroup, totalBytes int64, present, total int) {
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
	if totalSpeed == 0 {
		for _, g := range groups {
			totalSpeed += g.Speed
		}
	}

	leader := "-"
	if len(users) > 0 {
		leader = clean(users[0].Name, 20)
	}

	fmt.Fprintf(w, ".-== GoFTPd Race ==------------------------------.\r\n")
	fmt.Fprintf(w, "| #1 %-20s %4dF %7s %11s %3d%% |\r\n",
		leader,
		present,
		raceSize(totalBytes),
		speed(totalSpeed),
		totalPct,
	)
	fmt.Fprintf(w, "`------------------------------[ %d/%d ]--'\r\n", present, total)
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

	// Junction = top-T: the section above is a single-wide block (ASCII art),
	// so column dividers start here and only go DOWNWARD. Using cpCR here
	// would draw little nubs sticking up above the line.
	raw(w, sep(cpLT, cpTD, cpRT, 28, 11, 8, 11, 8))
	row(w, "Users", "Files", "Size", "Speed", "%")
	raw(w, sep(cpLT, cpCR, cpRT, 28, 11, 8, 11, 8))

	for i, u := range users {
		label := fmt.Sprintf("#%-2d %s/%s",
			i+1,
			clean(u.Name, 12),
			clean(u.Group, 12),
		)

		row(
			w,
			label,
			fmt.Sprintf("%dF", u.Files),
			raceSize(u.Bytes),
			speed(u.Speed),
			fmt.Sprintf("%d%%", pct(u.Percent)),
		)
	}

	if len(groups) > 0 {
		raw(w, sep(cpLT, cpCR, cpRT, 28, 11, 8, 11, 8))
		row(w, "Groups", "Files", "Size", "Speed", "%")
		raw(w, sep(cpLT, cpCR, cpRT, 28, 11, 8, 11, 8))

		for i, g := range groups {
			label := fmt.Sprintf("#%-2d %s",
				i+1,
				clean(g.Name, 24),
			)

			row(
				w,
				label,
				fmt.Sprintf("%dF", g.Files),
				raceSize(g.Bytes),
				speed(g.Speed),
				fmt.Sprintf("%d%%", pct(g.Percent)),
			)
		}
	}

	raw(w, sep(cpLT, cpCR, cpRT, 28, 11, 8, 11, 8))
	row(
		w,
		"TOTAL",
		fmt.Sprintf("%d/%dF", present, total),
		raceSize(totalBytes),
		speed(totalSpeed),
		fmt.Sprintf("%d%%", totalPct),
	)

	// Bottom border uses bottom-T junctions so column separators from the
	// TOTAL row terminate cleanly into the bottom edge.
	raw(w, sep(cpBL, cpBU, cpBR, 28, 11, 8, 11, 8))
}

func RenderFTPReplyBlock(w io.Writer, code int, finalLine string, render func(io.Writer)) {
	var buf bytes.Buffer
	render(&buf)
	text := strings.ReplaceAll(buf.String(), "\r\n", "\n")
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	for _, line := range lines {
		if line == "" {
			fmt.Fprintf(w, "%d-\r\n", code)
			continue
		}
		fmt.Fprintf(w, "%d-%s\r\n", code, line)
	}
	fmt.Fprintf(w, "%d %s\r\n", code, finalLine)
}
