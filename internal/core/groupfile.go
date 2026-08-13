package core

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// groupConfigMu serializes writes to etc/groups/<name> so a concurrent SITE edit
// can't interleave with a save and produce a truncated file.
var groupConfigMu sync.Mutex

type GroupFile struct {
	Name           string
	Slots          int
	LeechSlots     int
	AllotmentSlots int
	MaxAllotment   int64
	GroupNFO       string
	Simult         int
}

func LoadGroupConfig(name string) (*GroupFile, error) {
	var err error
	name, err = normalizeSiteGroupName(name)
	if err != nil {
		return nil, err
	}
	path := filepath.Join("etc", "groups", name)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	g := &GroupFile{
		Name:           name,
		Slots:          -1,
		LeechSlots:     0,
		AllotmentSlots: 0,
		MaxAllotment:   0,
		Simult:         0,
	}
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) == 0 {
			continue
		}
		switch strings.ToUpper(parts[0]) {
		case "GROUP":
			if len(parts) > 1 {
				g.Name = parts[1]
			}
		case "SLOTS":
			if len(parts) > 1 {
				fmt.Sscanf(parts[1], "%d", &g.Slots)
			}
			if len(parts) > 2 {
				fmt.Sscanf(parts[2], "%d", &g.LeechSlots)
			}
			if len(parts) > 3 {
				fmt.Sscanf(parts[3], "%d", &g.AllotmentSlots)
			}
			if len(parts) > 4 {
				fmt.Sscanf(parts[4], "%d", &g.MaxAllotment)
			}
		case "GROUPNFO":
			if len(parts) > 1 {
				g.GroupNFO = strings.Join(parts[1:], " ")
			}
		case "SIMULT":
			if len(parts) > 1 {
				fmt.Sscanf(parts[1], "%d", &g.Simult)
			}
		}
	}
	return g, nil
}

func (g *GroupFile) Save() error {
	if g == nil {
		return fmt.Errorf("group config is nil")
	}
	name, err := normalizeSiteGroupName(g.Name)
	if err != nil {
		return err
	}
	g.Name = name
	path := filepath.Join("etc", "groups", g.Name)

	mode := os.FileMode(0644)
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	} else if !os.IsNotExist(err) {
		return err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "GROUP %s\n", g.Name)
	fmt.Fprintf(&b, "SLOTS %d %d %d %d\n", g.Slots, g.LeechSlots, g.AllotmentSlots, g.MaxAllotment)
	fmt.Fprintf(&b, "GROUPNFO %s\n", strings.TrimSpace(g.GroupNFO))
	fmt.Fprintf(&b, "SIMULT %d\n", g.Simult)

	groupConfigMu.Lock()
	defer groupConfigMu.Unlock()
	return atomicWriteFile(path, []byte(b.String()), mode)
}
