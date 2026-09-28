package terminology

import (
	"errors"
	"fmt"
	"sort"
	"sync"
)

// NewService creates an empty codeset service.
func NewService() *Service {
	return &Service{
		versions:  make(map[string]map[string]*Codeset),
		activeVer: make(map[string]string),
	}
}

var (
	// ErrEmptyCodesetName is returned when a codeset has no name.
	ErrEmptyCodesetName = errors.New("codeset name is empty")
	// ErrEmptyVersion is returned when a codeset has no version.
	ErrEmptyVersion = errors.New("codeset version is empty")
	// ErrVersionNotFound is returned when a referenced version is unknown.
	ErrVersionNotFound = errors.New("codeset version not found")
)

// Load registers an immutable codeset version. Loading never mutates or
// removes previously loaded versions: historical mappings taken against an
// old version stay reproducible after newer versions are loaded.
func (s *Service) Load(c *Codeset) error {
	if c == nil {
		return errors.New("nil codeset")
	}
	if c.Name == "" {
		return ErrEmptyCodesetName
	}
	if c.Version == "" {
		return ErrEmptyVersion
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.versions[c.Name]; !ok {
		s.versions[c.Name] = make(map[string]*Codeset)
	}
	if _, exists := s.versions[c.Name][c.Version]; exists {
		// Re-loading the exact same (name, version) is a no-op-friendly
		// conflict: reject so callers cannot silently change history.
		return fmt.Errorf("codeset %s version %s already loaded", c.Name, c.Version)
	}
	// Defensive copy so callers cannot mutate a registered codeset.
	cp := &Codeset{Name: c.Name, Version: c.Version, System: c.System,
		Entries: make(map[string]Entry, len(c.Entries))}
	for k, v := range c.Entries {
		cp.Entries[k] = v
	}
	s.versions[c.Name][c.Version] = cp
	// First version loaded becomes the active one deterministically;
	// callers may switch explicitly with SetActiveVersion.
	if _, ok := s.activeVer[c.Name]; !ok {
		s.activeVer[c.Name] = c.Version
	}
	return nil
}

// SetActiveVersion selects the version used for new imports. It does not
// affect records already normalized with an older pinned version.
func (s *Service) SetActiveVersion(name, version string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	vers, ok := s.versions[name]
	if !ok {
		return fmt.Errorf("%w: %s", ErrVersionNotFound, name)
	}
	if _, ok := vers[version]; !ok {
		return fmt.Errorf("%w: %s/%s", ErrVersionNotFound, name, version)
	}
	s.activeVer[name] = version
	return nil
}

// ActiveVersion reports the currently selected version for a codeset.
func (s *Service) ActiveVersion(name string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.activeVer[name]
	return v, ok
}

// Versions returns the sorted, loaded version strings for a codeset.
func (s *Service) Versions(name string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []string
	for v := range s.versions[name] {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

// Map maps a code against a specific (pinned) codeset version. The bool is
// false only when the codeset/version itself is unknown. A known version
// always returns a result whose Status is Active/Deprecated/Unknown.
func (s *Service) Map(name, version, code string) (LookupResult, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	vers, ok := s.versions[name]
	if !ok {
		return LookupResult{}, false
	}
	cs, ok := vers[version]
	if !ok {
		return LookupResult{}, false
	}
	e, ok := cs.Entries[code]
	if !ok {
		return LookupResult{Status: StatusUnknown}, true
	}
	if e.Deprecated {
		return LookupResult{
			Status:        StatusDeprecated,
			Display:       e.Display,
			CanonicalCode: e.CanonicalCode,
		}, true
	}
	return LookupResult{
		Status:        StatusActive,
		Display:       e.Display,
		CanonicalCode: e.CanonicalCode,
	}, true
}

// SystemOf returns the code system declared by a codeset version.
func (s *Service) SystemOf(name, version string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if vers, ok := s.versions[name]; ok {
		if cs, ok := vers[version]; ok {
			return cs.System, true
		}
	}
	return "", false
}

var _ sync.Locker = (*sync.Mutex)(nil)
