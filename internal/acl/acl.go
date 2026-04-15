package acl

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"

	"goftpd/internal/user"
)

type Rule struct {
	Type     string // privpath, upload, download, makedir, delete, nuke, etc
	Path     string // /site/*, /site/PRE/*, etc
	Required string // 1, *, "1 =SiteOP", "A =NUKERS", "=Admin", etc
}

type Engine struct {
	RulesByType map[string][]Rule
}

// LoadEngine loads ACL rules from a file
func LoadEngine(path string) (*Engine, error) {
	e := &Engine{
		RulesByType: make(map[string][]Rule),
	}

	file, err := os.Open(path)
	if err != nil {
		return e, nil
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Parse: type path required
		parts := strings.Fields(line)
		if len(parts) >= 2 {
			// Handle various formats
			ruleType := strings.ToLower(parts[0])
			rulePath := parts[1]
			required := "*" // default

			if len(parts) > 2 {
				required = strings.Join(parts[2:], " ")
			}

			rule := Rule{
				Type:     ruleType,
				Path:     rulePath,
				Required: required,
			}
			e.RulesByType[ruleType] = append(e.RulesByType[ruleType], rule)
		}
	}

	return e, scanner.Err()
}

// pathMatches checks if path matches pattern
func pathMatches(pattern, path string) bool {
	pattern = filepath.Clean(pattern)
	path = filepath.Clean(path)

	if pattern == path {
		return true
	}

	// Handle wildcards like /site/*, /site/PRE/*, /site/PRE/*/*, etc
	if strings.Contains(pattern, "*") {
		// Convert glob to regex-like matching
		parts := strings.Split(pattern, "*")
		if len(parts) == 2 {
			prefix := parts[0]
			suffix := parts[1]

			if suffix == "" {
				// /site/* matches /site/anything
				return strings.HasPrefix(path, prefix)
			} else if prefix == "" {
				// *suffix
				return strings.HasSuffix(path, suffix)
			} else {
				// prefix*suffix
				return strings.HasPrefix(path, prefix) && strings.HasSuffix(path, suffix)
			}
		}
	}

	return false
}

// checkRequired checks if user meets requirement
func checkRequired(required string, u *user.User) bool {
	required = strings.TrimSpace(required)

	// "*" = anyone
	if required == "*" {
		return true
	}

	// Single flag like "1" or "A"
	if len(required) == 1 && !strings.Contains(required, " ") {
		return u.HasFlag(required)
	}

	// Group check like "1 =SiteOP" or "A =NUKERS"
	if strings.Contains(required, "=") {
		parts := strings.Split(required, "=")
		if len(parts) == 2 {
			flagPart := strings.TrimSpace(parts[0])
			groupName := strings.TrimSpace(parts[1])

			// If flagPart is empty, just check group: "=Admin"
			if flagPart == "" {
				return u.IsInGroup(groupName)
			}

			// Otherwise check flag AND group: "1 =SiteOP"
			return u.HasFlag(flagPart) && u.IsInGroup(groupName)
		}
	}

	return false
}

// CanPerform checks if user can perform action in path
func (e *Engine) CanPerform(u *user.User, action string, vpath string) bool {
	if u == nil {
		return false
	}

	action = strings.ToLower(action)
	vpath = filepath.Clean(vpath)

	// Map FTP commands to rule types
	ruleType := action
	switch action {
	case "upload":
		ruleType = "upload"
	case "download":
		ruleType = "download"
	case "mkd":
		ruleType = "makedir"
	case "rmd":
		ruleType = "makedir"
	case "delete", "dele":
		ruleType = "delete"
	case "rnfr", "rnto":
		ruleType = "rename"
	case "nuke":
		ruleType = "nuke"
	case "unnuke":
		ruleType = "unnuke"
	}

	// Check rules for this action type only
	if rules, ok := e.RulesByType[ruleType]; ok {
		for _, rule := range rules {
			if pathMatches(rule.Path, vpath) {
				return checkRequired(rule.Required, u)
			}
		}
	}

	// Check privpath rules
	if privRules, ok := e.RulesByType["privpath"]; ok {
		for _, rule := range privRules {
			if pathMatches(rule.Path, vpath) {
				return checkRequired(rule.Required, u)
			}
		}
	}

	// Default: siteop (flag 1) always allowed
	return u.HasFlag("1")
}

