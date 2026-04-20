package plugin

import (
	"fmt"
	"sort"

	"goftpd/sitebot/internal/event"
)

type Handler interface {
	Name() string
	Initialize(config map[string]interface{}) error
	OnEvent(evt *event.Event) ([]Output, error)
	Close() error
}

type Output struct {
	Type   string
	Text   string
	Target string
	Notice bool
}

// toStringSlice coerces a YAML-decoded config value into []string.
// YAML lists come back as []interface{} — not []string — so a direct type
// assertion fails. This handles both forms plus a comma-separated string.
// Returns fallback if raw is nil/empty/unrecognized.
func toStringSlice(raw interface{}, fallback []string) []string {
	switch v := raw.(type) {
	case []string:
		if len(v) > 0 {
			return v
		}
	case []interface{}:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		if len(out) > 0 {
			return out
		}
	case string:
		if v != "" {
			out := []string{}
			for _, s := range splitCSV(v) {
				if s != "" {
					out = append(out, s)
				}
			}
			if len(out) > 0 {
				return out
			}
		}
	}
	return fallback
}

func ToStringSlice(raw interface{}, fallback []string) []string {
	return toStringSlice(raw, fallback)
}

func ConfigSection(config map[string]interface{}, name string) map[string]interface{} {
	raw, ok := config[name]
	if !ok {
		return map[string]interface{}{}
	}
	if section, ok := raw.(map[string]interface{}); ok {
		return section
	}
	if section, ok := raw.(map[interface{}]interface{}); ok {
		out := map[string]interface{}{}
		for k, v := range section {
			if key, ok := k.(string); ok {
				out[key] = v
			}
		}
		return out
	}
	return map[string]interface{}{}
}

func splitCSV(s string) []string {
	parts := []string{}
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ',' {
			p := s[start:i]
			for len(p) > 0 && (p[0] == ' ' || p[0] == '\t') {
				p = p[1:]
			}
			for len(p) > 0 && (p[len(p)-1] == ' ' || p[len(p)-1] == '\t') {
				p = p[:len(p)-1]
			}
			parts = append(parts, p)
			start = i + 1
		}
	}
	return parts
}

type Manager struct{ plugins map[string]Handler }

func NewManager() *Manager                  { return &Manager{plugins: map[string]Handler{}} }
func (m *Manager) Register(p Handler) error { m.plugins[p.Name()] = p; return nil }
func (m *Manager) List() []string {
	names := []string{}
	for n := range m.plugins {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
func (m *Manager) ProcessEvent(evt *event.Event) ([]Output, error) {
	var outs []Output
	var errs []error
	for _, name := range m.List() {
		o, err := callPlugin(m.plugins[name], evt)
		if err == nil {
			outs = append(outs, o...)
		} else {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
		}
	}
	if len(errs) > 0 {
		return outs, fmt.Errorf("%v", errs)
	}
	return outs, nil
}

func callPlugin(p Handler, evt *event.Event) (outs []Output, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return p.OnEvent(evt)
}
func (m *Manager) Close() error {
	for _, name := range m.List() {
		_ = m.plugins[name].Close()
	}
	return nil
}
