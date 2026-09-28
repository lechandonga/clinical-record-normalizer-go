package terminology

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// codesetFile is the on-disk JSON layout of a codeset version.
type codesetFile struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	System  string `json:"system"`
	Entries []struct {
		Code          string `json:"code"`
		Display       string `json:"display"`
		CanonicalCode string `json:"canonicalCode,omitempty"`
		Deprecated    bool   `json:"deprecated"`
		DeprecatedIn  string `json:"deprecatedIn,omitempty"`
	} `json:"entries"`
}

// LoadFile reads and registers one codeset version from a JSON file.
func (s *Service) LoadFile(path string) (*Codeset, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read codeset %s: %w", path, err)
	}
	var f codesetFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse codeset %s: %w", path, err)
	}
	cs := &Codeset{Name: f.Name, Version: f.Version, System: f.System,
		Entries: make(map[string]Entry, len(f.Entries))}
	for _, e := range f.Entries {
		if e.Code == "" {
			return nil, fmt.Errorf("codeset %s/%s contains empty code", f.Name, f.Version)
		}
		if _, dup := cs.Entries[e.Code]; dup {
			return nil, fmt.Errorf("codeset %s/%s contains duplicate code %s",
				f.Name, f.Version, e.Code)
		}
		cs.Entries[e.Code] = Entry{
			Code:          e.Code,
			Display:       e.Display,
			CanonicalCode: e.CanonicalCode,
			Deprecated:    e.Deprecated,
			DeprecatedIn:  e.DeprecatedIn,
		}
	}
	if err := s.Load(cs); err != nil {
		return nil, err
	}
	return cs, nil
}

// LoadDir loads every *.json codeset file in dir (sorted by filename for
// deterministic "first version becomes active" behavior).
func (s *Service) LoadDir(dir string) error {
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return err
	}
	sort.Strings(files)
	for _, p := range files {
		if _, err := s.LoadFile(p); err != nil {
			return err
		}
	}
	return nil
}
