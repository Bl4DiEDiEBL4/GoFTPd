package template

import (
	"bufio"
	"os"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

type Theme struct {
	Vars      map[string]string
	Announces map[string]string
}

func LoadTheme(filename string) (*Theme, error) {
	f, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	th := &Theme{Vars: map[string]string{}, Announces: map[string]string{}}
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])
		val = strings.Trim(val, " \t\r\n\"")
		if strings.HasPrefix(key, "announce.") {
			th.Announces[strings.TrimPrefix(key, "announce.")] = val
		} else {
			th.Vars[key] = val
		}
	}
	return th, s.Err()
}

var varRE = regexp.MustCompile(`%\{?([A-Za-z0-9_\-]+)\}?`)

func Render(raw string, vars map[string]string) string {
	out := renderExpr(raw, vars)
	out = strings.ReplaceAll(out, `\n`, "\n")
	out = stripControls(out)
	return strings.TrimSpace(out)
}

func renderExpr(s string, vars map[string]string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] != '%' {
			b.WriteByte(s[i])
			i++
			continue
		}
		if i+1 >= len(s) {
			b.WriteByte(s[i])
			i++
			continue
		}
		switch s[i+1] {
		case 'b':
			if body, next, ok := parseWrapped(s, i+2); ok {
				b.WriteByte(0x02) // mIRC bold
				b.WriteString(renderExpr(body, vars))
				b.WriteByte(0x02)
				i = next
				continue
			}
		case 'u':
			if body, next, ok := parseWrapped(s, i+2); ok {
				b.WriteByte(0x1f) // underline
				b.WriteString(renderExpr(body, vars))
				b.WriteByte(0x1f)
				i = next
				continue
			}
		case 'c':
			j := i + 2
			colorCode := ""
			if j < len(s) && s[j] == '%' {
				k := j + 1
				for k < len(s) && isVarChar(rune(s[k])) {
					k++
				}
				if k > j+1 {
					key := s[j+1 : k]
					colorCode = strings.TrimLeft(strings.TrimSpace(vars[key]), "cC")
					j = k
				}
			} else {
				for j < len(s) && s[j] >= '0' && s[j] <= '9' {
					j++
				}
				colorCode = s[i+2 : j]
			}
			if colorCode != "" && j < len(s) && s[j] == '{' {
				if body, next, ok := parseBodyFrom(s, j); ok {
					b.WriteByte(0x03) // mIRC color
					b.WriteString(colorCode)
					b.WriteString(renderExpr(body, vars))
					b.WriteByte(0x03)
					i = next
					continue
				}
			}
		case '{':
			if body, next, ok := parseBodyFrom(s, i+1); ok {
				key := strings.TrimSpace(body)
				if val, ok := vars[key]; ok {
					b.WriteString(val)
				}
				i = next
				continue
			}
		default:
			j := i + 1
			for j < len(s) && isVarChar(rune(s[j])) {
				j++
			}
			if j > i+1 {
				key := s[i+1 : j]
				if val, ok := vars[key]; ok {
					b.WriteString(val)
				}
				i = j
				continue
			}
		}
		b.WriteByte(s[i])
		i++
	}

	out := b.String()
	// final variable replacement pass for any simple leftovers
	keys := make([]string, 0, len(vars))
	for k := range vars {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
	for _, k := range keys {
		out = strings.ReplaceAll(out, "%{"+k+"}", vars[k])
		out = strings.ReplaceAll(out, "%"+k, vars[k])
	}
	out = varRE.ReplaceAllStringFunc(out, func(m string) string { return "" })
	return out
}

func parseWrapped(s string, idx int) (string, int, bool) {
	if idx >= len(s) || s[idx] != '{' {
		return "", idx, false
	}
	return parseBodyFrom(s, idx)
}

func parseBodyFrom(s string, brace int) (string, int, bool) {
	if brace >= len(s) || s[brace] != '{' {
		return "", brace, false
	}
	depth := 0
	start := brace + 1
	for i := brace; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start:i], i + 1, true
			}
		}
	}
	return "", brace, false
}

func isVarChar(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-'
}

func stripControls(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		// Keep IRC formatting: bold, color, reverse, italic, underline, reset, plain
		case r == '\x02' || r == '\x03' || r == '\x0f' || r == '\x16' || r == '\x1d' || r == '\x1f':
			b.WriteRune(r)
		case r == '\n' || r == '\r' || r == '\t':
			b.WriteRune(r)
		case r < 32 || r == 127:
			// strip other unprintables
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
