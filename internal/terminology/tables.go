package terminology

import (
	"encoding/json"
	"fmt"

	"github.com/lechandonga/clinical-record-normalizer-go/internal/domain"
)

// Table is one versioned code table for a single coding system.
type Table struct {
	System  string       `json:"system"`
	Version string       `json:"version"`
	Entries []TableEntry `json:"entries"`
}

// TableEntry maps one source code to a normalized concept.
type TableEntry struct {
	Code       string               `json:"code"`
	Status     domain.MappingStatus `json:"status"`
	ConceptSys string               `json:"concept_system"`
	Concept    string               `json:"concept"`
	Display    string               `json:"display"`
	ReplacedBy string               `json:"replaced_by,omitempty"`
}

// LoadTable decodes one code table JSON document.
func LoadTable(raw []byte) (Table, error) {
	var t Table
	if err := json.Unmarshal(raw, &t); err != nil {
		return Table{}, fmt.Errorf("terminology: invalid table json: %w", err)
	}
	for i := range t.Entries {
		switch t.Entries[i].Status {
		case domain.MappingActive, domain.MappingDeprecated, "":
		default:
			return Table{}, fmt.Errorf("terminology: entry %q has invalid status %q",
				t.Entries[i].Code, t.Entries[i].Status)
		}
		if t.Entries[i].Status == "" {
			t.Entries[i].Status = domain.MappingActive
		}
	}
	return t, nil
}

// IsWellFormedCode reports whether a raw code has a legal shape. This is
// deliberately separate from table lookup: a syntactically illegal code is
// rejected (invalid_code), while a well-formed code absent from the pinned
// table maps to a marked unknown rather than being dropped.
func IsWellFormedCode(code string) bool {
	if len(code) == 0 || len(code) > 64 {
		return false
	}
	for i := 0; i < len(code); i++ {
		c := code[i]
		ok := c >= 'A' && c <= 'Z' ||
			c >= 'a' && c <= 'z' ||
			c >= '0' && c <= '9' ||
			c == '.' || c == '-' || c == '_'
		if !ok {
			return false
		}
	}
	return true
}
