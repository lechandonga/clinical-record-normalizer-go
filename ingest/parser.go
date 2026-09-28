package ingest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/lechandonga/clinical-record-normalizer-go/model"
)

// Accepted wire date/time layouts, from most to least specific.
var timeLayouts = []string{
	"2006-01-02T15:04:05Z07:00",
	"2006-01-02T15:04:05",
	"2006-01-02T15:04Z07:00",
	"2006-01-02T15:04",
	"2006-01-02",
}

// timePrecision describes how specific a time literal is.
type timePrecision int

const (
	precisionDay timePrecision = iota
	precisionMinute
	precisionSecond
)

func parseTimeLiteral(s string) (time.Time, timePrecision, error) {
	for i, layout := range timeLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			switch i {
			case 0, 1:
				return t, precisionSecond, nil
			case 2, 3:
				return t, precisionMinute, nil
			default:
				return t, precisionDay, nil
			}
		}
	}
	return time.Time{}, precisionDay, fmt.Errorf("unparseable time %q", s)
}

// Parse decodes one raw message. Format problems are reported as a typed
// model.Rejection; nothing is silently skipped.
func Parse(raw model.RawMessage) (*Parsed, *model.Rejection) {
	// Keep the raw body in the rejection for traceability.
	rawText := string(bytes.TrimSpace(raw.Body))
	if raw.Format != "" && raw.Format != "json" {
		return nil, reject(raw, model.RejectUnreadable, "",
			"unsupported format: "+raw.Format, rawText)
	}
	if rawText == "" {
		return nil, reject(raw, model.RejectUnreadable, "",
			"empty body", rawText)
	}
	var msg Message
	dec := json.NewDecoder(strings.NewReader(rawText))
	if err := dec.Decode(&msg); err != nil {
		return nil, reject(raw, model.RejectUnreadable, "",
			"invalid JSON: "+err.Error(), rawText)
	}
	// Reject trailing tokens: a message must be exactly one JSON object.
	if dec.More() {
		return nil, reject(raw, model.RejectUnreadable, "",
			"multiple JSON values", rawText)
	}

	// Structural/required-field validation happens here; value-domain
	// validation (codes, times) happens in Normalize where the pinned
	// codeset version is available.
	if strings.TrimSpace(msg.RecordID) == "" {
		return nil, reject(raw, model.RejectMissingField, "recordId",
			"recordId is required", rawText)
	}
	if len(msg.Patient.Identifiers) == 0 {
		return nil, reject(raw, model.RejectMissingField, "patient.identifiers",
			"at least one patient identifier is required", rawText)
	}
	for i, id := range msg.Patient.Identifiers {
		field := fmt.Sprintf("patient.identifiers[%d]", i)
		if strings.TrimSpace(id.System) == "" {
			return nil, reject(raw, model.RejectMissingField, field+".system",
				"identifier system is required", rawText)
		}
		if strings.TrimSpace(id.Value) == "" {
			return nil, reject(raw, model.RejectMissingField, field+".value",
				"identifier value is required", rawText)
		}
	}
	if msg.Patient.Name != nil && strings.TrimSpace(*msg.Patient.Name) == "" {
		return nil, reject(raw, model.RejectMissingField, "patient.name",
			"name must not be blank when present", rawText)
	}
	if msg.Patient.BirthDate == nil {
		return nil, reject(raw, model.RejectMissingField, "patient.birthDate",
			"birthDate is required", rawText)
	}
	if msg.Patient.Gender == nil {
		return nil, reject(raw, model.RejectMissingField, "patient.gender",
			"gender is required", rawText)
	}
	if msg.Encounter.Start == nil {
		return nil, reject(raw, model.RejectMissingField, "encounter.start",
			"encounter start is required", rawText)
	}
	if msg.Encounter.End == nil {
		return nil, reject(raw, model.RejectMissingField, "encounter.end",
			"encounter end is required", rawText)
	}
	if msg.Encounter.TypeSystem == nil || strings.TrimSpace(*msg.Encounter.TypeSystem) == "" {
		return nil, reject(raw, model.RejectMissingField, "encounter.typeSystem",
			"encounter type system is required", rawText)
	}
	if msg.Encounter.TypeCode == nil || strings.TrimSpace(*msg.Encounter.TypeCode) == "" {
		return nil, reject(raw, model.RejectMissingField, "encounter.typeCode",
			"encounter type code is required", rawText)
	}

	// receivedAt is optional ingestion-ordering context supplied by the
	// feed. When absent the normalizer uses the import time.
	var recv time.Time
	if strings.TrimSpace(msg.ReceivedAt) != "" {
		t, _, err := parseTimeLiteral(msg.ReceivedAt)
		if err != nil {
			return nil, reject(raw, model.RejectInvalidTime, "receivedAt",
				err.Error(), rawText)
		}
		recv = t
	}
	return &Parsed{Msg: msg, ReceivedAt: recv}, nil
}

func reject(raw model.RawMessage, reason model.RejectReason, field, detail, rawInput string) *model.Rejection {
	return &model.Rejection{
		Source:   model.SourceRef{Source: raw.Source, RecordID: raw.RecordID},
		Reason:   reason,
		Field:    field,
		Detail:   detail,
		RawInput: rawInput,
	}
}
