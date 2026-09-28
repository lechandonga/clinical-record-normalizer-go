// Package terminology implements versioned local codesets and code mapping.
package terminology

import "sync"

// Mapping status values returned by LookupResult.Status.
const (
	StatusUnknown    = 0
	StatusActive     = 1
	StatusDeprecated = 2
)

// Entry is one code in a codeset version.
type Entry struct {
	Code          string `json:"code"`
	Display       string `json:"display"`
	CanonicalCode string `json:"canonicalCode,omitempty"`
	Deprecated    bool   `json:"deprecated"`
	// DeprecatedIn is the version in which the code became deprecated.
	DeprecatedIn string `json:"deprecatedIn,omitempty"`
}

// Codeset is one immutable, named version of a local code table.
type Codeset struct {
	Name    string           `json:"name"`
	Version string           `json:"version"`
	System  string           `json:"system"`
	Entries map[string]Entry `json:"entries"`
}

// LookupResult is the outcome of mapping a code.
type LookupResult struct {
	Status        int // StatusUnknown, StatusActive or StatusDeprecated
	Display       string
	CanonicalCode string
}

// Service holds loaded codeset versions and performs mappings against a
// pinned version. Results for a pinned version never change when newer
// versions are loaded.
type Service struct {
	mu        sync.RWMutex
	versions  map[string]map[string]*Codeset
	activeVer map[string]string
}
