// Package model defines the unified patient and encounter model that all
// source messages are normalized into.
package model

import "time"

// SourceRef identifies the provenance of a piece of data: which feed/source
// it came from and which raw record it belongs to.
type SourceRef struct {
	Source   string `json:"source"`
	RecordID string `json:"recordId"`
}

func (s SourceRef) String() string { return s.Source + "/" + s.RecordID }

// RawMessage is one unparsed input record as delivered by a source.
type RawMessage struct {
	Source   string
	RecordID string
	// Format is the wire format, currently only "json".
	Format string
	// Body is the raw payload bytes (kept for logging).
	Body []byte
}

// Identifier is a business identifier such as an MRN or a national id.
type Identifier struct {
	System string `json:"system"`
	Value  string `json:"value"`
}

// AttributedValue is a demographic value together with its provenance and
// the time at which the source asserted it.
type AttributedValue struct {
	Value      string    `json:"value"`
	Source     string    `json:"source"`
	RecordID   string    `json:"recordId"`
	AssumedAt  time.Time `json:"assumedAt"`
	ReceivedAt time.Time `json:"receivedAt"`
}

// FieldConflict records a demographic disagreement between sources and how
// it was resolved, so the trail is preserved.
type FieldConflict struct {
	Field      string    `json:"field"`
	Chosen     string    `json:"chosen"`
	ChosenFrom SourceRef `json:"chosenFrom"`
	Rejected   string    `json:"rejected"`
	RejectedBy SourceRef `json:"rejectedBy"`
	Reason     string    `json:"reason"`
}

// Patient is the unified, source-independent patient aggregate.
type Patient struct {
	PatientID   string          `json:"patientId"`
	Identifiers []Identifier    `json:"identifiers"`
	Name        AttributedValue `json:"name"`
	Gender      AttributedValue `json:"gender"`
	BirthDate   AttributedValue `json:"birthDate"`
	Sources     []string        `json:"sources"`
	Conflicts   []FieldConflict `json:"conflicts,omitempty"`
	UpdatedAt   time.Time       `json:"updatedAt"`
}

// MappingStatus classifies the outcome of a local codeset lookup.
type MappingStatus string

const (
	// MapActive: code exists in the pinned codeset version and is active.
	MapActive MappingStatus = "active"
	// MapDeprecated: code exists but is marked deprecated in that version.
	MapDeprecated MappingStatus = "deprecated"
	// MapUnknown: code is not present in the pinned codeset version.
	MapUnknown MappingStatus = "unknown"
)

// Concept is a terminology mapping result.
type Concept struct {
	System        string        `json:"system"`
	Code          string        `json:"code"`
	Display       string        `json:"display,omitempty"`
	CanonicalCode string        `json:"canonicalCode,omitempty"`
	Status        MappingStatus `json:"status"`
	Codeset       string        `json:"codeset"`
	Version       string        `json:"version"`
}

// Diagnosis is a coded diagnosis attached to an encounter.
type Diagnosis struct {
	Concept Concept `json:"concept"`
	Rank    int     `json:"rank,omitempty"`
}

// Encounter is the unified visit/encounter aggregate.
type Encounter struct {
	EncounterID string       `json:"encounterId"`
	PatientID   string       `json:"patientId"`
	Identifiers []Identifier `json:"identifiers,omitempty"`
	Start       time.Time    `json:"start"`
	End         time.Time    `json:"end"`
	Type        Concept      `json:"type"`
	Diagnoses   []Diagnosis  `json:"diagnoses,omitempty"`
	Source      SourceRef    `json:"source"`
}

// Record is a fully normalized source record, keyed for idempotent dedup.
type Record struct {
	// DedupKey identifies the raw source record (source + recordId).
	DedupKey   string
	Source     SourceRef `json:"source"`
	CodesetVer string    `json:"codesetVersion"`
	Patient    Patient   `json:"patient"`
	Encounter  Encounter `json:"encounter"`
}

// RejectReason is a stable, machine-readable rejection category.
type RejectReason string

const (
	RejectUnreadable       RejectReason = "unreadable_payload"
	RejectMissingField     RejectReason = "missing_field"
	RejectInvalidCode      RejectReason = "invalid_code"
	RejectInvalidTime      RejectReason = "invalid_time"
	RejectAmbiguousTime    RejectReason = "ambiguous_time"
	RejectInvalidBirthDate RejectReason = "invalid_birth_date"
	RejectInvalidGender    RejectReason = "invalid_gender"
	RejectIdentityConflict RejectReason = "identity_conflict"
)

// Rejection is a record that could not be normalized, with a distinct reason.
type Rejection struct {
	Source   SourceRef    `json:"source"`
	Reason   RejectReason `json:"reason"`
	Field    string       `json:"field,omitempty"`
	Detail   string       `json:"detail,omitempty"`
	RawInput string       `json:"rawInput,omitempty"`
}

// ImportReport is the deterministic result of one batch import.
type ImportReport struct {
	BatchID          string               `json:"batchId"`
	CodesetVersion   string               `json:"codesetVersion"`
	AcceptedCount    int                  `json:"acceptedCount"`
	RejectedCount    int                  `json:"rejectedCount"`
	RejectedByReason map[RejectReason]int `json:"rejectedByReason"`
	Rejections       []Rejection          `json:"rejections,omitempty"`
	PatientCount     int                  `json:"patientCount"`
	EncounterCount   int                  `json:"encounterCount"`
	// Fingerprint is a hash over the canonical JSON snapshot of the whole
	// post-import state; equal state => equal fingerprint.
	Fingerprint string `json:"fingerprint"`
}
