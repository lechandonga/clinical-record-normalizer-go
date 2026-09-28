package normalize

import "github.com/lechandonga/clinical-record-normalizer-go/internal/domain"

// RejectReason is a stable, distinguishable reason a record was refused.
type RejectReason string

const (
	RejectMalformedJSON RejectReason = "malformed_json"
	RejectMissingField  RejectReason = "missing_field"
	RejectInvalidCode   RejectReason = "invalid_code"
	RejectInvalidSex    RejectReason = "invalid_sex"
	RejectInvalidTime   RejectReason = "invalid_time"
	RejectAmbiguousTime RejectReason = "ambiguous_time"
	RejectInvalidBirth  RejectReason = "invalid_birth_date"
	RejectFutureBirth   RejectReason = "future_birth_date"
	RejectUnknownSource RejectReason = "unknown_source"
	RejectDuplicate     RejectReason = "duplicate_message"
)

// Rejection is one refused record with its distinguishable reason.
type Rejection struct {
	Reason RejectReason      `json:"reason"`
	Field  string            `json:"field,omitempty"`
	Detail string            `json:"detail,omitempty"`
	Source *domain.SourceRef `json:"source,omitempty"`
	Raw    string            `json:"raw,omitempty"`
}
