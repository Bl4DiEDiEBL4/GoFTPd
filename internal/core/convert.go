package core

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

// ConvertUserLine converts a template with format codes to actual formatted string
// Format codes (like pzs-ng):
//   %n     = rank/position (1, 2, 3...)
//   %u     = username
//   %g     = group name
//   %U     = username/group
//   %m     = size in MB
//   %f     = files
//   %p     = percent
//   %s     = speed (unit configurable: KBs, MBs, Kbit, Mbit, Gbit, B/s)
//   %9.1m  = width 9, 1 decimal (supports width.precision)
//   %-9u   = left-aligned width 9
func ConvertUserLine(template string, rank int, username string, group string, 
	sizeBytes int64, files int, percent float64, speedBs float64, speedUnit string) string {
	
	var result strings.Builder
	i := 0
	
	for i < len(template) {
		if template[i] != '%' {
			result.WriteByte(template[i])
			i++
			continue
		}
		
		i++ // skip %
		if i >= len(template) {
			result.WriteByte('%')
			break
		}
		
		// Parse width and precision: %[-]width[.precision]code
		leftAlign := false
		widthStart := i
		
		if template[i] == '-' {
			leftAlign = true
			i++
			widthStart = i
		}
		
		// Parse width
		for i < len(template) && unicode.IsDigit(rune(template[i])) {
			i++
		}
		widthStr := template[widthStart:i]
		width := 0
		if widthStr != "" {
			width, _ = strconv.Atoi(widthStr)
		}
		
		// Parse precision
		precision := -1
		if i < len(template) && template[i] == '.' {
			i++
			precStart := i
			for i < len(template) && unicode.IsDigit(rune(template[i])) {
				i++
			}
			if i > precStart {
				precision, _ = strconv.Atoi(template[precStart:i])
			}
		}
		
		// Get format code
		if i >= len(template) {
			break
		}
		code := template[i]
		i++
		
		// Format the value
		var formatted string
		switch code {
		case 'n': // rank
			formatted = fmt.Sprintf("%d", rank)
		case 'u': // username
			formatted = username
		case 'g': // group
			formatted = group
		case 'U': // username/group
			formatted = username + "/" + group
		case 'm': // size in MB with M suffix
			sizeMB := float64(sizeBytes) / 1024 / 1024
			if precision >= 0 {
				formatted = fmt.Sprintf("%.*fM", precision, sizeMB)
			} else {
				formatted = fmt.Sprintf("%.1fM", sizeMB)
			}
		case 'f': // files with F suffix
			formatted = fmt.Sprintf("%dF", files)
		case 'p': // percent with % suffix
			if precision >= 0 {
				formatted = fmt.Sprintf("%.*f%%", precision, percent)
			} else {
				formatted = fmt.Sprintf("%.1f%%", percent)
			}
		case 's': // speed with configurable unit (KB/s, MB/s, Mbit, Gbit)
			var speedVal float64
			var unit string
			
			// Default to KB/s if not specified
			if speedUnit == "" {
				speedUnit = "KB/s"
			}
			
			switch speedUnit {
			case "KB/s":
				speedVal = speedBs / 1024
				unit = "KB/s"
			case "MB/s":
				speedVal = speedBs / 1024 / 1024
				unit = "MB/s"
			case "Mbit":
				speedVal = (speedBs * 8) / 1000000
				unit = "Mbit"
			case "Gbit":
				speedVal = (speedBs * 8) / 1000000000
				unit = "Gbit"
			default:
				// Default to KB/s
				speedVal = speedBs / 1024
				unit = "KB/s"
			}
			
			if precision >= 0 {
				formatted = fmt.Sprintf("%.*f%s", precision, speedVal, unit)
			} else {
				formatted = fmt.Sprintf("%.0f%s", speedVal, unit)
			}
		default:
			formatted = string(code)
		}
		
		// Apply width formatting
		if width > 0 {
			if leftAlign {
				formatted = fmt.Sprintf("%-*s", width, formatted)
			} else {
				formatted = fmt.Sprintf("%*s", width, formatted)
			}
		}
		
		result.WriteString(formatted)
	}
	
	return result.String()
}
