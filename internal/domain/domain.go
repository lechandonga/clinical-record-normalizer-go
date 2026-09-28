// Package domain defines the unified patient/encounter model that all source
// messages are normalized into. Every canonical value keeps traceability back
// to the source message it came from.
package domain

import "time"

// Sex is the normalized sex value.
type Sex string

const (
	SexMale    Sex = "M"
	SexFemale  Sex = "F"
	SexUnknown Sex = "U"
)

// Source identifies where a message came from.
type Source struct {
	ID       string `json:"id"`
	Priority int    `json:"priority"`
}

// SourceRef points at a single raw message uniquely.
type SourceRef struct {
	SourceID  string `json:"source_id"`
	MessageID string `json:"message_id"`
}

// TracedValue is a canonical string value annotated with its origin.
type TracedValue struct {
	Value    string    `json:"value"`
	Source   SourceRef `json:"source"`
	Received time.Time `json:"received"`
}

// Identifier is a single identifier asserted by a source.
type Identifier struct {
	System string    `json:"system"`
	Value  string    `json:"value"`
	Source SourceRef `json:"source"`
}

// FieldConflict records a demographic value that lost the resolution rule.
type FieldConflict struct {
	Field  string      `json:"field"`
	Winner TracedValue `json:"winner"`
	Loser  TracedValue `json:"loser"`
	Rule   string      `json:"rule"`
}

// Patient is the unified patient identity.
type Patient struct {
	ID          string          `json:"patient_id"`
	Identifiers []Identifier    `json:"identifiers"`
	Sex         TracedValue     `json:"sex"`
	BirthDate   TracedValue     `json:"birth_date"`
	Conflicts   []FieldConflict `json:"conflicts,omitempty"`
	Sources     []SourceRef     `json:"sources"`
}

// MappingStatus describes the outcome of coding a source code.
type MappingStatus string

const (
	MappingActive     MappingStatus = "active"
	MappingDeprecated MappingStatus = "deprecated"
	MappingUnknown    MappingStatus = "unknown"
)

// Coding is a source code plus the local table version it was mapped with.
type Coding struct {
	System       string        `json:"system"`
	Code         string        `json:"code"`
	TableVersion string        `json:"table_version"`
	Status       MappingStatus `json:"status"`
	ReplacedBy   string        `json:"replaced_by,omitempty"`
}

// Concept is the normalized concept a coding maps to.
type Concept struct {
	System  string `json:"system"`
	Code    string `json:"code"`
	Display string `json:"display"`
}

// Diagnosis is a normalized diagnosis attached to an encounter.
type Diagnosis struct {
	Concept Concept   `json:"concept"`
	Coding  Coding    `json:"coding"`
	Source  SourceRef `json:"source"`
}

// Encounter is the unified visit model.
type Encounter struct {
	ID        string      `json:"encounter_id"`
	PatientID string      `json:"patient_id"`
	Type      string      `json:"type"`
	Start     time.Time   `json:"start"`
	End       *time.Time  `json:"end,omitempty"`
	Diagnoses []Diagnosis `json:"diagnoses"`
	Sources   []SourceRef `json:"sources"`
}
