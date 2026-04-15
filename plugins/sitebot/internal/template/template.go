package template

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

// IRC color codes
const (
	ColorBlack   = "\x033"
	ColorBlue    = "\x032"
	ColorGreen   = "\x033"
	ColorRed     = "\x034"
	ColorBrown   = "\x035"
	ColorPurple  = "\x036"
	ColorOrange  = "\x037"
	ColorYellow  = "\x038"
	ColorLGreen  = "\x039"
	ColorTeal    = "\x0310"
	ColorLBlue   = "\x0311"
	ColorPink    = "\x0313"
	ColorGrey    = "\x0314"
	ColorLGrey   = "\x0315"
	ColorWhite   = "\x0316"
	ColorReset   = "\x03"
	BoldChar     = "\x02"
	UnderlineChar = "\x1f"
)

// ColorMap maps %c1, %c2, etc to IRC colors
var ColorMap = map[string]string{
	"1": ColorRed,
	"2": ColorBlue,
	"3": ColorGreen,
	"4": ColorYellow,
	"5": ColorBrown,
	"6": ColorPurple,
	"7": ColorOrange,
	"8": ColorLGreen,
	"9": ColorTeal,
	"10": ColorLBlue,
	"11": ColorPink,
	"12": ColorGrey,
	"13": ColorLGrey,
	"14": ColorWhite,
	"15": ColorBlack,
}

// Template represents a template string
type Template struct {
	Raw      string
	Filename string
}

// LoadTemplate loads a .zpt template file
func LoadTemplate(filename string) (*Template, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}
	
	// Find active template (not commented)
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "#") && !strings.HasPrefix(line, "//") && strings.Contains(line, "=") {
			// This is the active template
			parts := strings.SplitN(line, "=", 2)
			if len(parts) == 2 {
				return &Template{
					Raw:      strings.TrimSpace(parts[1]),
					Filename: filename,
				}, nil
			}
		}
	}
	
	return nil, fmt.Errorf("no active template found in %s", filename)
}

// Render renders the template with variables
func (t *Template) Render(vars map[string]string) string {
	result := t.Raw
	
	// Replace variables: %variable_name
	varRegex := regexp.MustCompile(`%(\w+)`)
	result = varRegex.ReplaceAllStringFunc(result, func(match string) string {
		varName := match[1:] // Remove %
		if val, ok := vars[varName]; ok {
			return val
		}
		return match // Return original if not found
	})
	
	// Replace colors: %c1{text}, %c2{text}, etc
	colorRegex := regexp.MustCompile(`%c(\d+)\{([^}]*)\}`)
	result = colorRegex.ReplaceAllStringFunc(result, func(match string) string {
		re := regexp.MustCompile(`%c(\d+)\{([^}]*)\}`)
		parts := re.FindStringSubmatch(match)
		if len(parts) == 3 {
			colorNum := parts[1]
			text := parts[2]
			if color, ok := ColorMap[colorNum]; ok {
				return color + text + ColorReset
			}
		}
		return match
	})
	
	// Replace bold: %b{text}
	boldRegex := regexp.MustCompile(`%b\{([^}]*)\}`)
	result = boldRegex.ReplaceAllString(result, BoldChar+"$1"+BoldChar)
	
	// Replace newlines: \n
	result = strings.ReplaceAll(result, "\\n", "\n")
	
	return result
}
