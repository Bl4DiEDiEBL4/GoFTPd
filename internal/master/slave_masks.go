package master

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Per-slave IP mask allowlist, used as the fallback authentication mechanism
// when the master-slave link isn't using mTLS (SlaveManager.slaveCACert
// unset). Unlike the global authDenyEntries/authAllowNets list, this is
// keyed by slave name (drftpd-style "site slave <name> addmask <mask>"), and
// intentionally lives here rather than on RemoteSlave: masks must be
// checked before a RemoteSlave exists (pre-auth) and must work for slave
// names that have never connected.
type slaveMasks struct {
	mu     sync.RWMutex
	file   string
	byName map[string][]string
}

// ConfigureSlaveMasksFile sets the path used to persist per-slave IP masks
// and loads any existing entries from it.
func (sm *SlaveManager) ConfigureSlaveMasksFile(path string) error {
	path = strings.TrimSpace(path)
	sm.masks.mu.Lock()
	defer sm.masks.mu.Unlock()
	loaded, err := loadSlaveMasksFile(path)
	if err != nil {
		return err
	}
	sm.masks.file = path
	sm.masks.byName = loaded
	return nil
}

func loadSlaveMasksFile(path string) (map[string][]string, error) {
	loaded := make(map[string][]string)
	if path == "" {
		return loaded, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return loaded, nil
		}
		return nil, err
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return nil, fmt.Errorf("%s: invalid slave mask line %q (expected \"<name> <mask>\")", path, line)
		}
		name, mask := normalizeSlaveMaskName(fields[0]), fields[1]
		if err := validateMaskSyntax(mask); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		duplicate := false
		for _, existing := range loaded[name] {
			if strings.EqualFold(existing, mask) {
				duplicate = true
				break
			}
		}
		if !duplicate {
			loaded[name] = append(loaded[name], mask)
		}
	}
	for name := range loaded {
		sort.Strings(loaded[name])
	}
	return loaded, nil
}

func (sm *SlaveManager) saveSlaveMasksLocked() error {
	if sm.masks.file == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(sm.masks.file), 0755); err != nil {
		return err
	}
	names := make([]string, 0, len(sm.masks.byName))
	for name := range sm.masks.byName {
		names = append(names, name)
	}
	sort.Strings(names)

	lines := []string{"# Per-slave IP mask allowlist: \"<slave name> <ip|cidr|wildcard>\", one per line."}
	for _, name := range names {
		for _, mask := range sm.masks.byName[name] {
			lines = append(lines, name+" "+mask)
		}
	}
	content := strings.Join(lines, "\n") + "\n"
	return os.WriteFile(sm.masks.file, []byte(content), 0644)
}

// AddSlaveMask registers an allowed IP/CIDR/wildcard mask for the given slave
// name and persists it.
func (sm *SlaveManager) AddSlaveMask(name, mask string) error {
	name = normalizeSlaveMaskName(name)
	mask = strings.TrimSpace(mask)
	if name == "" {
		return fmt.Errorf("slave name required")
	}
	if strings.ContainsAny(name, " \t\r\n") {
		return fmt.Errorf("slave name %q must not contain whitespace", name)
	}
	if err := validateMaskSyntax(mask); err != nil {
		return err
	}
	sm.masks.mu.Lock()
	defer sm.masks.mu.Unlock()
	if sm.masks.byName == nil {
		sm.masks.byName = make(map[string][]string)
	}
	for _, existing := range sm.masks.byName[name] {
		if strings.EqualFold(existing, mask) {
			return nil
		}
	}
	previous := append([]string(nil), sm.masks.byName[name]...)
	sm.masks.byName[name] = append(sm.masks.byName[name], mask)
	sort.Strings(sm.masks.byName[name])
	if err := sm.saveSlaveMasksLocked(); err != nil {
		if len(previous) == 0 {
			delete(sm.masks.byName, name)
		} else {
			sm.masks.byName[name] = previous
		}
		return err
	}
	return nil
}

// RemoveSlaveMask removes a previously registered mask for the given slave
// name. Returns false if the mask wasn't present.
func (sm *SlaveManager) RemoveSlaveMask(name, mask string) (bool, error) {
	name = normalizeSlaveMaskName(name)
	mask = strings.TrimSpace(mask)
	sm.masks.mu.Lock()
	defer sm.masks.mu.Unlock()
	existing := sm.masks.byName[name]
	previous := append([]string(nil), existing...)
	out := existing[:0]
	removed := false
	for _, m := range existing {
		if strings.EqualFold(m, mask) {
			removed = true
			continue
		}
		out = append(out, m)
	}
	if !removed {
		return false, nil
	}
	if len(out) == 0 {
		delete(sm.masks.byName, name)
	} else {
		sm.masks.byName[name] = out
	}
	if err := sm.saveSlaveMasksLocked(); err != nil {
		sm.masks.byName[name] = previous
		return false, err
	}
	return true, nil
}

// ListSlaveMasks returns the masks registered for a single slave name.
func (sm *SlaveManager) ListSlaveMasks(name string) []string {
	name = normalizeSlaveMaskName(name)
	sm.masks.mu.RLock()
	defer sm.masks.mu.RUnlock()
	out := make([]string, len(sm.masks.byName[name]))
	copy(out, sm.masks.byName[name])
	return out
}

// ListAllSlaveMasks returns every slave name's registered masks.
func (sm *SlaveManager) ListAllSlaveMasks() map[string][]string {
	sm.masks.mu.RLock()
	defer sm.masks.mu.RUnlock()
	out := make(map[string][]string, len(sm.masks.byName))
	for name, masks := range sm.masks.byName {
		cp := make([]string, len(masks))
		copy(cp, masks)
		out[name] = cp
	}
	return out
}

// slaveMaskAllows reports whether ip is permitted to register as the given
// slave name under the currently configured masks. A name with zero
// registered masks is denied (fail closed) - this is the whole point of the
// fallback when mTLS isn't configured.
func (sm *SlaveManager) slaveMaskAllows(name, ip string) bool {
	name = normalizeSlaveMaskName(name)
	sm.masks.mu.RLock()
	defer sm.masks.mu.RUnlock()
	masks := sm.masks.byName[name]
	if len(masks) == 0 {
		return false
	}
	for _, mask := range masks {
		if maskMatchesIP(mask, ip) {
			return true
		}
	}
	return false
}

func normalizeSlaveMaskName(name string) string {
	return strings.ToUpper(strings.TrimSpace(name))
}

func validateMaskSyntax(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fmt.Errorf("empty mask")
	}
	if strings.Contains(raw, "/") {
		if _, _, err := net.ParseCIDR(raw); err != nil {
			return fmt.Errorf("invalid CIDR mask %q: %w", raw, err)
		}
		return nil
	}
	if strings.Contains(raw, "*") {
		parts := strings.Split(raw, ".")
		if len(parts) != 4 {
			return fmt.Errorf("invalid wildcard mask %q: expected 4 dot-separated octets", raw)
		}
		for _, p := range parts {
			if p == "*" {
				continue
			}
			n, err := strconv.Atoi(p)
			if err != nil || n < 0 || n > 255 {
				return fmt.Errorf("invalid wildcard mask %q: bad octet %q", raw, p)
			}
		}
		return nil
	}
	if net.ParseIP(raw) == nil {
		return fmt.Errorf("invalid mask %q: not an IP, CIDR, or wildcard pattern", raw)
	}
	return nil
}

// maskMatchesIP checks ip (bare IP string) against a single mask entry,
// which may be a CIDR ("1.2.3.4/32"), a drftpd-style IPv4 wildcard
// ("1.2.3.*"), or a bare IP (exact match).
func maskMatchesIP(mask, ip string) bool {
	mask = strings.TrimSpace(mask)
	parsedIP := net.ParseIP(strings.TrimSpace(ip))
	if mask == "" || parsedIP == nil {
		return false
	}
	if strings.Contains(mask, "/") {
		_, network, err := net.ParseCIDR(mask)
		if err != nil {
			return false
		}
		return network.Contains(parsedIP)
	}
	if strings.Contains(mask, "*") {
		return wildcardMatches(mask, parsedIP)
	}
	maskIP := net.ParseIP(mask)
	return maskIP != nil && maskIP.Equal(parsedIP)
}

func wildcardMatches(mask string, ip net.IP) bool {
	ip4 := ip.To4()
	if ip4 == nil {
		return false
	}
	maskParts := strings.Split(mask, ".")
	if len(maskParts) != 4 {
		return false
	}
	ipParts := strings.Split(ip4.String(), ".")
	for i, part := range maskParts {
		if part == "*" {
			continue
		}
		if part != ipParts[i] {
			return false
		}
	}
	return true
}
