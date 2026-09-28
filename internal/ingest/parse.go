package ingest

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Envelope is the common wrapper around every source message.
type Envelope struct {
	MessageID string          `json:"message_id"`
	SourceID  string          `json:"source_id"`
	SentAt    string          `json:"sent_at"`
	Version   string          `json:"version"`
	Payload   json.RawMessage `json:"payload"`
}

// ParseError is a typed envelope-level parse failure. Reason is either
// "malformed_json" (not parseable / wrong shape) or "missing_field".
type ParseError struct {
	Reason string
	Field  string
	Detail string
}

func (e *ParseError) Error() string {
	if e.Field == "" {
		return e.Reason + ": " + e.Detail
	}
	return fmt.Sprintf("%s: %s (%s)", e.Reason, e.Field, e.Detail)
}

// Parse decodes and shape-checks one raw message envelope.
func Parse(raw []byte) (Envelope, *ParseError) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return Envelope{}, &ParseError{Reason: "malformed_json", Detail: "empty input"}
	}

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return Envelope{}, &ParseError{Reason: "malformed_json", Detail: err.Error()}
	}

	if env.MessageID == "" {
		return Envelope{}, &ParseError{Reason: "missing_field", Field: "message_id"}
	}
	if env.SourceID == "" {
		return Envelope{}, &ParseError{Reason: "missing_field", Field: "source_id"}
	}
	if env.SentAt == "" {
		return Envelope{}, &ParseError{Reason: "missing_field", Field: "sent_at"}
	}
	if len(bytes.TrimSpace(env.Payload)) == 0 ||
		string(bytes.TrimSpace(env.Payload)) == "null" {
		return Envelope{}, &ParseError{Reason: "missing_field", Field: "payload"}
	}

	// Payload must be a JSON object; arrays/strings are malformed shapes.
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(env.Payload, &probe); err != nil {
		return Envelope{}, &ParseError{
			Reason: "malformed_json",
			Field:  "payload",
			Detail: "payload must be a JSON object",
		}
	}

	return env, nil
}
