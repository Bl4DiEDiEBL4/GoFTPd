package acl

import (
	"bufio"
	"fmt"
	"os"
	pathpkg "path"
	"path/filepath"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
	"weaveftpd/internal/user"
)

// requirementCache memoizes parsed scalar requirements. checkRequired is called
// per ACL rule, per directory entry, per listing. Re-parsing the same handful
// of requirement strings thousands of times. Parsed Requirements are read-only
// after parsing, so caching them by source string is safe and cuts the
// per-listing CPU/allocation churn that slows LIST/MLSD on busy sections.
var requirementCache sync.Map // string -> *Requirement

type Rule struct {
	Type        string       `yaml:"type"`     // privpath, upload, download, makedir, delete, nuke, etc
	Path        string       `yaml:"path"`     // /site/*, /site/PRE/*, etc
	Required    string       `yaml:"required"` // 1, *, "1 =SiteOP", "A =NUKERS", "=Admin", etc
	Requirement *Requirement `yaml:"-"`
	paths       []compiledRulePath
}

type compiledRulePath struct {
	pattern       string
	parent        string
	trailingSlash bool
}

type Requirement struct {
	RoleRef   string
	Anyone    bool
	Nobody    bool
	AllFlags  []string
	AnyFlags  []string
	NotFlags  []string
	AllGroups []string
	AnyGroups []string
	NotGroups []string
	Users     []string
	NotUsers  []string
	AnyOf     []*Requirement
	AllOf     []*Requirement
}

type Engine struct {
	RulesByType map[string][]Rule
}

type yamlRulesFile struct {
	Rules []Rule `yaml:"rules"`
}

type yamlStructuredRulesFile struct {
	Roles map[string]yamlRequirementRef `yaml:"roles"`
	Rules yaml.Node                     `yaml:"rules"`
}

type yamlRuleGroupEntry struct {
	Path     string             `yaml:"path"`
	Paths    []string           `yaml:"paths"`
	Allow    []string           `yaml:"allow"`
	Deny     []string           `yaml:"deny"`
	Required yamlRequirementRef `yaml:"required"`
}

type yamlRequirementRef struct {
	RoleName string
	Spec     *Requirement
}

func (r *yamlRequirementRef) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case 0:
		return nil
	default:
		req, err := parseRequirementNode(value)
		if err != nil {
			return err
		}
		if req == nil {
			return fmt.Errorf("unsupported required format")
		}
		r.RoleName = ""
		r.Spec = req
		return nil
	}
}

func parseRequirementNode(value *yaml.Node) (*Requirement, error) {
	switch value.Kind {
	case yaml.ScalarNode:
		text := strings.TrimSpace(value.Value)
		if strings.HasPrefix(text, "$") {
			return &Requirement{RoleRef: strings.TrimSpace(strings.TrimPrefix(text, "$"))}, nil
		}
		req := &Requirement{}
		return req, parseRequirementScalar(req, text)
	case yaml.MappingNode:
		req := &Requirement{}
		for i := 0; i+1 < len(value.Content); i += 2 {
			key := strings.ToLower(strings.TrimSpace(value.Content[i].Value))
			val := value.Content[i+1]
			switch key {
			case "anyone":
				var b bool
				if err := val.Decode(&b); err != nil {
					return nil, err
				}
				req.Anyone = b
			case "nobody":
				var b bool
				if err := val.Decode(&b); err != nil {
					return nil, err
				}
				req.Nobody = b
			case "all_flags":
				if err := val.Decode(&req.AllFlags); err != nil {
					return nil, err
				}
			case "any_flags":
				if err := val.Decode(&req.AnyFlags); err != nil {
					return nil, err
				}
			case "not_flags":
				if err := val.Decode(&req.NotFlags); err != nil {
					return nil, err
				}
			case "all_groups":
				if err := val.Decode(&req.AllGroups); err != nil {
					return nil, err
				}
			case "any_groups":
				if err := val.Decode(&req.AnyGroups); err != nil {
					return nil, err
				}
			case "not_groups":
				if err := val.Decode(&req.NotGroups); err != nil {
					return nil, err
				}
			case "users":
				if err := val.Decode(&req.Users); err != nil {
					return nil, err
				}
			case "not_users":
				if err := val.Decode(&req.NotUsers); err != nil {
					return nil, err
				}
			case "any_of":
				children, err := parseRequirementList(val)
				if err != nil {
					return nil, err
				}
				req.AnyOf = children
			case "all_of":
				children, err := parseRequirementList(val)
				if err != nil {
					return nil, err
				}
				req.AllOf = children
			default:
				return nil, fmt.Errorf("unsupported required key %q", key)
			}
		}
		return req, nil
	default:
		return nil, fmt.Errorf("unsupported required format")
	}
}

func parseRequirementList(value *yaml.Node) ([]*Requirement, error) {
	if value.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("expected sequence")
	}
	var out []*Requirement
	for _, child := range value.Content {
		req, err := parseRequirementNode(child)
		if err != nil {
			return nil, err
		}
		out = append(out, req)
	}
	return out, nil
}

func parseRequirementScalar(req *Requirement, text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	if text == "*" {
		req.Anyone = true
		return nil
	}
	if text == "!*" {
		req.Nobody = true
		return nil
	}
	for _, token := range strings.Fields(text) {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		switch {
		case token == "*":
			req.Anyone = true
		case token == "!*":
			req.Nobody = true
		case strings.HasPrefix(token, "!@"):
			name := strings.TrimSpace(strings.TrimPrefix(token, "!@"))
			if name != "" {
				req.NotUsers = append(req.NotUsers, name)
			}
		case strings.HasPrefix(token, "!="):
			group := strings.TrimSpace(strings.TrimPrefix(token, "!="))
			if group != "" {
				req.NotGroups = append(req.NotGroups, group)
			}
		case strings.HasPrefix(token, "!"):
			flag := strings.TrimSpace(strings.TrimPrefix(token, "!"))
			if flag != "" {
				req.NotFlags = append(req.NotFlags, flag)
			}
		case strings.HasPrefix(token, "@"):
			name := strings.TrimSpace(strings.TrimPrefix(token, "@"))
			if name != "" {
				req.Users = append(req.Users, name)
			}
		case strings.HasPrefix(token, "="):
			group := strings.TrimSpace(strings.TrimPrefix(token, "="))
			if group != "" {
				req.AnyGroups = append(req.AnyGroups, group)
			}
		default:
			req.AllFlags = append(req.AllFlags, token)
		}
	}
	return nil
}

// LoadEngine loads ACL rules from a file
func LoadEngine(path string) (*Engine, error) {
	e := &Engine{
		RulesByType: make(map[string][]Rule),
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return e, err
	}
	if loaded, err := loadYAMLRules(e, data); loaded {
		return e, nil
	} else if err != nil && looksLikeYAMLACL(data) {
		return e, fmt.Errorf("invalid ACL YAML in %s: %w", path, err)
	}

	scanner := bufio.NewScanner(strings.NewReader(string(data)))
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
			rule = prepareRule(rule)
			e.RulesByType[ruleType] = append(e.RulesByType[ruleType], rule)
		}
	}

	return e, scanner.Err()
}

func loadYAMLRules(e *Engine, data []byte) (bool, error) {
	var file yamlRulesFile
	if err := yaml.Unmarshal(data, &file); err != nil {
		var structured yamlStructuredRulesFile
		if err2 := yaml.Unmarshal(data, &structured); err2 != nil {
			return false, err2
		}
		rules, ok := compileStructuredRules(structured)
		if !ok {
			return false, fmt.Errorf("structured ACL file did not compile any valid rules")
		}
		for _, rule := range rules {
			rule = prepareRule(rule)
			e.RulesByType[rule.Type] = append(e.RulesByType[rule.Type], rule)
		}
		return true, nil
	}
	if len(file.Rules) == 0 {
		var structured yamlStructuredRulesFile
		if err := yaml.Unmarshal(data, &structured); err != nil {
			return false, err
		}
		rules, ok := compileStructuredRules(structured)
		if !ok {
			return false, fmt.Errorf("structured ACL file did not compile any valid rules")
		}
		for _, rule := range rules {
			rule = prepareRule(rule)
			e.RulesByType[rule.Type] = append(e.RulesByType[rule.Type], rule)
		}
		return true, nil
	}
	added := 0
	for _, rule := range file.Rules {
		rule = prepareRule(rule)
		if rule.Type == "" || rule.Path == "" {
			continue
		}
		e.RulesByType[rule.Type] = append(e.RulesByType[rule.Type], rule)
		added++
	}
	if added == 0 {
		return false, fmt.Errorf("ACL YAML did not contain any valid rules")
	}
	return true, nil
}

func looksLikeYAMLACL(data []byte) bool {
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		raw := scanner.Text()
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(raw, " ") || strings.HasPrefix(raw, "\t") {
			return true
		}
		if strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "-\t") {
			return true
		}
		if strings.HasSuffix(trimmed, ":") || strings.Contains(trimmed, ": ") {
			return true
		}
	}
	return false
}

func compileStructuredRules(file yamlStructuredRulesFile) ([]Rule, bool) {
	if file.Rules.Kind == 0 {
		return nil, false
	}
	if file.Rules.Kind == yaml.SequenceNode {
		return nil, false
	}
	roles := make(map[string]*Requirement, len(file.Roles))
	for name, ref := range file.Roles {
		req, ok := resolveRequirement(ref, file.Roles, nil)
		if !ok {
			return nil, false
		}
		roles[strings.ToLower(strings.TrimSpace(name))] = req
	}
	var groups map[string][]yamlRuleGroupEntry
	if err := file.Rules.Decode(&groups); err != nil {
		return nil, false
	}
	var compiled []Rule
	for ruleType, entries := range groups {
		ruleType = strings.ToLower(strings.TrimSpace(ruleType))
		if ruleType == "" {
			continue
		}
		for _, entry := range entries {
			req, ok := resolveRequirement(entry.Required, file.Roles, roles)
			if !ok {
				return nil, false
			}
			requiredText := requirementToLegacyString(req)
			paths := expandRuleEntryPaths(entry)
			for _, path := range paths {
				path = strings.TrimSpace(path)
				if path == "" {
					continue
				}
				compiled = append(compiled, prepareRule(Rule{
					Type:        ruleType,
					Path:        path,
					Required:    requiredText,
					Requirement: cloneRequirement(req),
				}))
			}
		}
	}
	return compiled, len(compiled) > 0
}

func expandRuleEntryPaths(entry yamlRuleGroupEntry) []string {
	var paths []string
	if entry.Path != "" {
		paths = append(paths, entry.Path)
	}
	paths = append(paths, entry.Paths...)
	paths = append(paths, entry.Allow...)
	paths = append(paths, entry.Deny...)
	return paths
}

func resolveRequirement(ref yamlRequirementRef, rawRoles map[string]yamlRequirementRef, expanded map[string]*Requirement) (*Requirement, bool) {
	if ref.Spec != nil {
		if ref.Spec.RoleRef != "" {
			return resolveRoleRequirement(ref.Spec.RoleRef, rawRoles, expanded)
		}
		return cloneRequirement(ref.Spec), true
	}
	if ref.RoleName == "" {
		req := &Requirement{Anyone: true}
		return req, true
	}
	return resolveRoleRequirement(ref.RoleName, rawRoles, expanded)
}

func resolveRoleRequirement(name string, rawRoles map[string]yamlRequirementRef, expanded map[string]*Requirement) (*Requirement, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return nil, false
	}
	switch name {
	case "anyone":
		return &Requirement{Anyone: true}, true
	case "nobody":
		return &Requirement{Nobody: true}, true
	}
	if expanded != nil {
		if req, ok := expanded[name]; ok {
			return cloneRequirement(req), true
		}
	}
	roleRef, ok := rawRoles[name]
	if !ok {
		for key, candidate := range rawRoles {
			if strings.EqualFold(key, name) {
				roleRef = candidate
				ok = true
				break
			}
		}
		if !ok {
			return nil, false
		}
	}
	if roleRef.Spec != nil {
		if roleRef.Spec.RoleRef != "" {
			return resolveRoleRequirement(roleRef.Spec.RoleRef, rawRoles, expanded)
		}
		return cloneRequirement(roleRef.Spec), true
	}
	return nil, false
}

func cloneRequirement(req *Requirement) *Requirement {
	if req == nil {
		return nil
	}
	return &Requirement{
		RoleRef:   req.RoleRef,
		Anyone:    req.Anyone,
		Nobody:    req.Nobody,
		AllFlags:  append([]string(nil), req.AllFlags...),
		AnyFlags:  append([]string(nil), req.AnyFlags...),
		NotFlags:  append([]string(nil), req.NotFlags...),
		AllGroups: append([]string(nil), req.AllGroups...),
		AnyGroups: append([]string(nil), req.AnyGroups...),
		NotGroups: append([]string(nil), req.NotGroups...),
		Users:     append([]string(nil), req.Users...),
		NotUsers:  append([]string(nil), req.NotUsers...),
		AnyOf:     cloneRequirementList(req.AnyOf),
		AllOf:     cloneRequirementList(req.AllOf),
	}
}

func cloneRequirementList(reqs []*Requirement) []*Requirement {
	if len(reqs) == 0 {
		return nil
	}
	out := make([]*Requirement, 0, len(reqs))
	for _, req := range reqs {
		out = append(out, cloneRequirement(req))
	}
	return out
}

func requirementToLegacyString(req *Requirement) string {
	if req == nil {
		return "*"
	}
	if req.Nobody {
		return "!*"
	}
	var parts []string
	if req.Anyone {
		parts = append(parts, "*")
	}
	parts = append(parts, req.AllFlags...)
	for _, flag := range req.AnyFlags {
		parts = append(parts, "{anyflag:"+flag+"}")
	}
	for _, flag := range req.NotFlags {
		parts = append(parts, "!"+flag)
	}
	for _, group := range req.AllGroups {
		parts = append(parts, "{allgroup:"+group+"}")
	}
	for _, group := range req.AnyGroups {
		parts = append(parts, "="+group)
	}
	for _, group := range req.NotGroups {
		parts = append(parts, "!="+group)
	}
	for _, userName := range req.Users {
		parts = append(parts, "@"+userName)
	}
	for _, userName := range req.NotUsers {
		parts = append(parts, "!@"+userName)
	}
	if len(req.AnyOf) > 0 || len(req.AllOf) > 0 {
		parts = append(parts, "{structured}")
	}
	if len(parts) == 0 {
		return "*"
	}
	return strings.Join(parts, " ")
}

func expandBracePatterns(pattern string) []string {
	pattern = strings.TrimSpace(pattern)
	start := strings.Index(pattern, "{")
	if start < 0 {
		return []string{pattern}
	}
	end := strings.Index(pattern[start:], "}")
	if end < 0 {
		return []string{pattern}
	}
	end += start
	body := pattern[start+1 : end]
	parts := strings.Split(body, ",")
	if len(parts) == 0 {
		return []string{pattern}
	}
	out := make([]string, 0, len(parts))
	prefix := pattern[:start]
	suffix := pattern[end+1:]
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		for _, expanded := range expandBracePatterns(prefix + part + suffix) {
			out = append(out, expanded)
		}
	}
	if len(out) == 0 {
		return []string{pattern}
	}
	return out
}

func prepareRule(rule Rule) Rule {
	rule.Type = strings.ToLower(strings.TrimSpace(rule.Type))
	rule.Path = strings.TrimSpace(rule.Path)
	rule.Required = strings.TrimSpace(rule.Required)
	if rule.Required == "" {
		rule.Required = "*"
	}
	if rule.Requirement == nil {
		rule.Requirement = &Requirement{}
		_ = parseRequirementScalar(rule.Requirement, rule.Required)
	}
	rule.paths = compileRulePaths(rule.Path)
	return rule
}

func compileRulePaths(pattern string) []compiledRulePath {
	expanded := expandBracePatterns(pattern)
	out := make([]compiledRulePath, 0, len(expanded))
	seen := make(map[string]struct{}, len(expanded))
	for _, expandedPattern := range expanded {
		compiled, ok := compileRulePath(expandedPattern)
		if !ok {
			continue
		}
		key := compiled.pattern + "\x00" + compiled.parent
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, compiled)
	}
	return out
}

func compileRulePath(pattern string) (compiledRulePath, bool) {
	rawPattern := strings.ReplaceAll(strings.TrimSpace(pattern), "\\", "/")
	if rawPattern == "" {
		return compiledRulePath{}, false
	}
	trailingSlash := strings.HasSuffix(rawPattern, "/")
	matchPattern := rawPattern
	if !trailingSlash {
		matchPattern = filepath.ToSlash(filepath.Clean(rawPattern))
	}
	return compiledRulePath{
		pattern:       matchPattern,
		parent:        filepath.ToSlash(filepath.Clean(rawPattern)),
		trailingSlash: trailingSlash,
	}, true
}

func compiledPathsForRule(rule Rule) []compiledRulePath {
	if len(rule.paths) > 0 {
		return rule.paths
	}
	return compileRulePaths(rule.Path)
}

func normalizeCheckPath(vpath string) string {
	return filepath.ToSlash(filepath.Clean(strings.ReplaceAll(strings.TrimSpace(vpath), "\\", "/")))
}

func pathMatchesCompiled(pattern compiledRulePath, vpath string) bool {
	if pattern.trailingSlash {
		matchPath := strings.TrimRight(vpath, "/") + "/"
		if ok, _ := pathpkg.Match(pattern.pattern, matchPath); ok {
			return true
		}
		return false
	}

	if pattern.pattern == vpath {
		return true
	}
	if ok, _ := pathpkg.Match(pattern.pattern, vpath); ok {
		return true
	}

	// Handle wildcards like /site/*, /site/PRE/*, /site/PRE/*/*, etc
	if strings.Contains(pattern.pattern, "*") {
		// Convert glob to regex-like matching
		parts := strings.Split(pattern.pattern, "*")
		if len(parts) == 2 {
			prefix := parts[0]
			suffix := parts[1]

			if suffix == "" {
				// /site/* matches /site/anything
				return strings.HasPrefix(vpath, prefix)
			} else if prefix == "" {
				// *suffix
				return strings.HasSuffix(vpath, suffix)
			} else {
				// prefix*suffix
				return strings.HasPrefix(vpath, prefix) && strings.HasSuffix(vpath, suffix)
			}
		}
	}

	return false
}

func ruleMatchesPath(rule Rule, vpath string) bool {
	for _, rulePath := range compiledPathsForRule(rule) {
		if pathMatchesCompiled(rulePath, vpath) || pathIsBelow(vpath, rulePath.parent) {
			return true
		}
	}
	return false
}

// checkRequired checks if user meets requirement. The parsed requirement is
// memoized so repeated ACL checks (every entry in a listing) don't re-parse.
func checkRequired(required string, u *user.User) bool {
	if cached, ok := requirementCache.Load(required); ok {
		return checkRequirement(cached.(*Requirement), u)
	}
	req := &Requirement{}
	_ = parseRequirementScalar(req, required)
	requirementCache.Store(required, req)
	return checkRequirement(req, u)
}

func checkRequirement(req *Requirement, u *user.User) bool {
	if req == nil {
		return true
	}
	if req.Nobody {
		return false
	}
	if u == nil {
		return false
	}

	for _, flag := range req.NotFlags {
		if u.HasFlag(flag) {
			return false
		}
	}
	for _, group := range req.NotGroups {
		if u.IsInGroup(group) {
			return false
		}
	}
	for _, name := range req.NotUsers {
		if strings.EqualFold(u.Name, name) {
			return false
		}
	}
	if !hasAllFlags(u, req.AllFlags) {
		return false
	}
	if len(req.AnyFlags) > 0 {
		matched := false
		for _, flag := range req.AnyFlags {
			if u.HasFlag(flag) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	for _, group := range req.AllGroups {
		if !u.IsInGroup(group) {
			return false
		}
	}
	if len(req.AnyGroups) > 0 {
		matched := false
		for _, group := range req.AnyGroups {
			if u.IsInGroup(group) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	if len(req.Users) > 0 {
		matched := false
		for _, name := range req.Users {
			if strings.EqualFold(u.Name, name) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	for _, child := range req.AllOf {
		if !checkRequirement(child, u) {
			return false
		}
	}
	if len(req.AnyOf) > 0 {
		matched := false
		for _, child := range req.AnyOf {
			if checkRequirement(child, u) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

func ruleAllows(rule Rule, u *user.User) bool {
	if rule.Requirement != nil {
		return checkRequirement(rule.Requirement, u)
	}
	return checkRequired(rule.Required, u)
}

func ruleTypeForAction(action string) string {
	action = strings.ToLower(action)
	switch action {
	case "upload":
		return "upload"
	case "download":
		return "download"
	case "mkd":
		return "makedir"
	case "rmd":
		return "makedir"
	case "delete", "dele":
		return "delete"
	case "rnfr", "rnto":
		return "rename"
	case "nuke":
		return "nuke"
	case "unnuke":
		return "unnuke"
	default:
		return action
	}
}

func hasAllFlags(u *user.User, flags []string) bool {
	if len(flags) == 0 {
		return true
	}
	for _, flag := range flags {
		if flag == "" {
			continue
		}
		if !u.HasFlag(flag) {
			return false
		}
	}
	return true
}

// CanPerform checks if user can perform action in path
func (e *Engine) CanPerform(u *user.User, action string, vpath string) bool {
	if u == nil {
		return false
	}

	action = strings.ToLower(action)
	vpath = normalizeCheckPath(vpath)

	// Private-path rules are an override gate. If a path is marked private,
	// the user must satisfy that rule before any broader action allow-list
	// is considered.
	if privRules, ok := e.RulesByType["privpath"]; ok {
		for _, rule := range privRules {
			if ruleMatchesPath(rule, vpath) {
				return ruleAllows(rule, u)
			}
		}
	}

	// Map FTP commands to rule types
	ruleType := ruleTypeForAction(action)

	// Check rules for this action type only
	if rules, ok := e.RulesByType[ruleType]; ok {
		for _, rule := range rules {
			if ruleMatchesPath(rule, vpath) {
				return ruleAllows(rule, u)
			}
		}
	}

	// Default: siteop (flag 1) always allowed
	return u.HasFlag("1")
}

// CanPerformRuleOnly checks only rules for the requested action. Unlike
// CanPerform, it does not fall back to privpath or default siteop behavior.
// Use it for non-permission modifiers such as nodupecheck.
func (e *Engine) CanPerformRuleOnly(u *user.User, action string, vpath string) bool {
	if u == nil {
		return false
	}
	ruleType := ruleTypeForAction(action)
	vpath = normalizeCheckPath(vpath)
	if rules, ok := e.RulesByType[ruleType]; ok {
		for _, rule := range rules {
			if ruleMatchesPath(rule, vpath) {
				return ruleAllows(rule, u)
			}
		}
	}
	return false
}

// pathMatches checks if path matches pattern
func pathMatches(pattern, vpath string) bool {
	vpath = normalizeCheckPath(vpath)
	for _, compiled := range compileRulePaths(pattern) {
		if pathMatchesCompiled(compiled, vpath) {
			return true
		}
	}
	return false
}

func (e *Engine) HasRuleType(ruleType string) bool {
	if e == nil {
		return false
	}
	_, ok := e.RulesByType[strings.ToLower(strings.TrimSpace(ruleType))]
	return ok
}

// MatchesRulePath reports whether vpath is covered by any rule of ruleType.
// Exact path rules also cover descendants, so a privpath for /site/PRE/GROUP
// covers /site/PRE/GROUP/Release/file.rar.
func (e *Engine) MatchesRulePath(ruleType, vpath string) bool {
	if e == nil {
		return false
	}
	rules, ok := e.RulesByType[strings.ToLower(strings.TrimSpace(ruleType))]
	if !ok {
		return false
	}
	vpath = normalizeCheckPath(vpath)
	for _, rule := range rules {
		if ruleMatchesPath(rule, vpath) {
			return true
		}
	}
	return false
}

func pathIsBelow(vpath, parent string) bool {
	if parent == "" || parent == "." || strings.Contains(parent, "*") {
		return false
	}
	parent = strings.TrimRight(parent, "/")
	return vpath == parent || strings.HasPrefix(vpath, parent+"/")
}
