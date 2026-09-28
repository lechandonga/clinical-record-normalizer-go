package terminology

import (
	"fmt"
	"sort"
)

type indexedTable struct {
	table  Table
	byCode map[string]TableEntry
}

type systemTables struct {
	versions       map[string]*indexedTable
	defaultVersion string
}

// Registry holds every loaded table keyed by system and version.
type Registry struct {
	systems map[string]*systemTables
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{systems: map[string]*systemTables{}}
}

// Load registers one table version. The first version loaded for a system
// becomes the default until SetDefault changes it.
func (r *Registry) Load(t Table) error {
	if t.System == "" {
		return fmt.Errorf("terminology: table has empty system")
	}
	if t.Version == "" {
		return fmt.Errorf("terminology: table for %q has empty version", t.System)
	}
	sys, ok := r.systems[t.System]
	if !ok {
		sys = &systemTables{versions: map[string]*indexedTable{}}
		r.systems[t.System] = sys
	}
	if _, dup := sys.versions[t.Version]; dup {
		return fmt.Errorf("terminology: duplicate table %s@%s", t.System, t.Version)
	}
	idx := &indexedTable{table: t, byCode: map[string]TableEntry{}}
	for _, e := range t.Entries {
		if e.Code == "" {
			return fmt.Errorf("terminology: table %s@%s has empty code", t.System, t.Version)
		}
		if _, dup := idx.byCode[e.Code]; dup {
			return fmt.Errorf("terminology: duplicate code %q in %s@%s", e.Code, t.System, t.Version)
		}
		idx.byCode[e.Code] = e
	}
	sys.versions[t.Version] = idx
	if sys.defaultVersion == "" {
		sys.defaultVersion = t.Version
	}
	return nil
}

// SetDefault pins the default version used when a message does not request one.
func (r *Registry) SetDefault(system, version string) error {
	sys, ok := r.systems[system]
	if !ok {
		return fmt.Errorf("terminology: unknown system %q", system)
	}
	if _, ok := sys.versions[version]; !ok {
		return fmt.Errorf("terminology: unknown version %q for %q", version, system)
	}
	sys.defaultVersion = version
	return nil
}

// ResolveVersion returns the concrete table version for a request. An empty
// requested version resolves to the system default.
func (r *Registry) ResolveVersion(system, requested string) (string, bool) {
	sys, ok := r.systems[system]
	if !ok {
		return "", false
	}
	if requested == "" {
		return sys.defaultVersion, sys.defaultVersion != ""
	}
	if _, ok := sys.versions[requested]; !ok {
		return "", false
	}
	return requested, true
}

// Systems returns loaded system IDs in sorted order.
func (r *Registry) Systems() []string {
	out := make([]string, 0, len(r.systems))
	for s := range r.systems {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// Versions returns loaded versions for a system in sorted order.
func (r *Registry) Versions(system string) []string {
	sys, ok := r.systems[system]
	if !ok {
		return nil
	}
	out := make([]string, 0, len(sys.versions))
	for v := range sys.versions {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}
