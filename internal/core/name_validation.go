package core

import (
	"fmt"
	"path/filepath"
	"strings"
)

func normalizeSiteGroupName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("empty group name")
	}
	if name == "." || name == ".." ||
		filepath.Base(name) != name ||
		strings.ContainsAny(name, "/\\: \t\r\n") {
		return "", fmt.Errorf("invalid group name %q", name)
	}
	return name, nil
}
