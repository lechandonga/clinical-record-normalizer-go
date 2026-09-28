// Package ingest parses raw source messages and normalizes them into the
// unified model, rejecting invalid records with distinct reasons.
package ingest

import "time"

// Message is the on-the-wire shape of a source message (JSON).
// All fields are pointers/optional so we can distinguish "absent" from
// "zero value".
type Message struct {
	Source     string           `json:"source"`
	RecordID   string           `json:"recordId"`
	ReceivedAt string           `json:"receivedAt"`
	Patient    MessagePatient   `json:"patient"`
	Encounter  MessageEncounter `json:"encounter"`
}

// MessageIdentifier is an identifier in a source message.
type MessageIdentifier struct {
	System string `json:"system"`
	Value  string `json:"value"`
}

// MessagePatient carries source-side patient demographics.
type MessagePatient struct {
	Identifiers []MessageIdentifier `json:"identifiers"`
	Name        *string             `json:"name"`
	Gender      *string             `json:"gender"`
	BirthDate   *string             `json:"birthDate"`
	AssumedAt   *string             `json:"assumedAt"`
}

// MessageDiagnosis is a coded diagnosis in a source message.
type MessageDiagnosis struct {
	System *string `json:"system"`
	Code   *string `json:"code"`
	Rank   int     `json:"rank"`
}

// MessageEncounter carries source-side visit data.
type MessageEncounter struct {
	Identifiers []MessageIdentifier `json:"identifiers"`
	Start       *string             `json:"start"`
	End         *string             `json:"end"`
	TypeSystem  *string             `json:"typeSystem"`
	TypeCode    *string             `json:"typeCode"`
	Diagnoses   []MessageDiagnosis  `json:"diagnoses"`
}

// Parsed carries a parsed-but-not-yet-normalized message with the parse
// timestamp, ready for identity resolution and mapping.
type Parsed struct {
	Msg        Message
	ReceivedAt time.Time
}
