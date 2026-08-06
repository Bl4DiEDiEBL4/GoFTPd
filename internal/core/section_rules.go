package core

import (
	"fmt"
	"log"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode"

	"weaveftpd/internal/zipscript"
)

const maxSectionRulesBytes = 32 * 1024

func emitCWDSectionRules(s *Session, dirPath string) {
	if s == nil || s.Config == nil {
		return
	}
	rulesConfig := s.Config.Zipscript.SectionRules
	if !s.Config.Zipscript.Enabled || !rulesConfig.CWD {
		return
	}
	section, lines, err := loadSectionRules(rulesConfig.Directory, dirPath)
	if err != nil {
		if s.Config.Debug {
			log.Printf("[SECTION-RULES] dir=%s: %v", dirPath, err)
		}
		return
	}
	if len(lines) == 0 {
		return
	}

	userName := ""
	if s.User != nil {
		userName = s.User.Name
	}
	lines = expandSectionRules(lines, section, userName, s.Config.Version)

	for _, line := range zipscript.BuildSectionRulesBox(section, lines, s.Config.Version) {
		fmt.Fprintf(s.Conn, "250-%s\r\n", line)
	}
}

func expandSectionRules(lines []string, section, userName, version string) []string {
	replacer := strings.NewReplacer("%S", section, "%U", userName, "%V", version)
	out := make([]string, len(lines))
	for i, line := range lines {
		out[i] = replacer.Replace(line)
	}
	return out
}

func loadSectionRules(rulesDir, dirPath string) (string, []string, error) {
	rulesDir = strings.TrimSpace(rulesDir)
	section, ok := sectionRulesName(dirPath)
	if rulesDir == "" || !ok {
		return "", nil, nil
	}

	names := []string{section + ".txt"}
	upperName := strings.ToUpper(section) + ".txt"
	if upperName != names[0] {
		names = append(names, upperName)
	}

	for _, name := range names {
		filePath := filepath.Join(filepath.Clean(rulesDir), name)
		info, err := os.Stat(filePath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return section, nil, err
		}
		if !info.Mode().IsRegular() {
			return section, nil, fmt.Errorf("%s is not a regular file", filePath)
		}
		if info.Size() > maxSectionRulesBytes {
			return section, nil, fmt.Errorf("%s exceeds %d bytes", filePath, maxSectionRulesBytes)
		}
		data, err := os.ReadFile(filePath)
		if err != nil {
			return section, nil, err
		}
		if len(data) > maxSectionRulesBytes {
			return section, nil, fmt.Errorf("%s exceeds %d bytes", filePath, maxSectionRulesBytes)
		}
		text := strings.ReplaceAll(string(data), "\r\n", "\n")
		text = strings.TrimRight(text, "\r\n")
		if strings.TrimSpace(text) == "" {
			return section, nil, nil
		}
		return section, strings.Split(text, "\n"), nil
	}

	return section, nil, nil
}

func sectionRulesName(dirPath string) (string, bool) {
	cleaned := path.Clean("/" + strings.TrimSpace(dirPath))
	if cleaned == "/" || cleaned == "." {
		return "", false
	}
	name := path.Base(cleaned)
	if name == "" || name == "." || name == ".." {
		return "", false
	}
	for _, r := range name {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || r == '.' {
			continue
		}
		return "", false
	}
	return name, true
}
