package core

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// LoadTemplate loads a message template file and substitutes variables
func LoadTemplate(templatePath string, vars map[string]string, config *Config) (string, error) {
	data, err := os.ReadFile(templatePath)
	if err != nil {
		if config.Debug {
			log.Printf("[TEMPLATE-DEBUG] Failed to load %s: %v", templatePath, err)
		}
		return "", err
	}

	content := string(data)
	
	// Replace format specs like %2n, %31u, %9.1m, etc.
	// Pattern: %[flags][width][.precision]varname
	// Then format the raw variable value according to the spec
	re := regexp.MustCompile(`%(-?)(\d*)(?:\.(\d+))?([a-zA-Z])`)
	content = re.ReplaceAllStringFunc(content, func(match string) string {
		// Parse: %[-][width][.precision][varname]
		parts := re.FindStringSubmatch(match)
		if len(parts) != 5 {
			return match // couldn't parse, keep original
		}
		
		flags := parts[1]      // "-" for left-align
		width := parts[2]      // e.g., "31", "9"
		precision := parts[3]  // e.g., "1" from %.1f
		varname := parts[4]    // e.g., "n", "u", "m"
		
		rawValue, ok := vars[varname]
		if !ok {
			return match // variable not found
		}
		
		// Try to format the value
		// First try to parse as float for decimal formatting
		if precision != "" {
			if f, err := strconv.ParseFloat(rawValue, 64); err == nil {
				prec, _ := strconv.Atoi(precision)
				w, _ := strconv.Atoi(width)
				if flags == "-" {
					return fmt.Sprintf("%-*.*f", w, prec, f)
				}
				return fmt.Sprintf("%*.*f", w, prec, f)
			}
		}
		
		// Otherwise format as string
		if width != "" {
			w, _ := strconv.Atoi(width)
			if flags == "-" {
				return fmt.Sprintf("%-*s", w, rawValue)
			}
			return fmt.Sprintf("%*s", w, rawValue)
		}
		
		// No formatting
		return rawValue
	})

	// Convert %% to literal %
	content = strings.ReplaceAll(content, "%%", "%")

	return content, nil
}

// LoadMessageTemplate loads from msgs folder
func LoadMessageTemplate(filename string, vars map[string]string, config *Config) (string, error) {
	templatePath := filepath.Join(config.MsgPath, filename)
	return LoadTemplate(templatePath, vars, config)
}
