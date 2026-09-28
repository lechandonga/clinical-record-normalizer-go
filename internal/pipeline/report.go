// Package pipeline wires parsing, normalization, identity resolution and the
// idempotent store into deterministic serial and concurrent imports.
package pipeline

import (
	"encoding/json"

	"github.com/lechandonga/clinical-record-normalizer-go/internal/domain"
	"github.com/lechandonga/clinical-record-normalizer-go/internal/normalize"
)

// Input is one raw file/record plus its origin label for logging.
type Input struct {
	Name string
	Raw  []byte
}

// Batch captures the table versions pinned for one import run.
type Batch struct {
	ID          string `json:"batch_id"`
	DiagVersion string `json:"diag_table_version"`
}

// Report is the fully traceable outcome of importing one batch.
type Report struct {
	Batch      Batch                 `json:"batch"`
	Accepted   int                   `json:"accepted"`
	Rejected   int                   `json:"rejected"`
	Patients   []domain.Patient      `json:"patients"`
	Encounters []domain.Encounter    `json:"encounters"`
	Rejections []normalize.Rejection `json:"rejections"`
}

// Equal reports whether two reports are structurally identical via canonical
// JSON comparison (used to assert serial == concurrent and idempotency).
func (a *Report) Equal(b *Report) bool {
	ja, err := json.Marshal(a)
	if err != nil {
		return false
	}
	jb, err := json.Marshal(b)
	if err != nil {
		return false
	}
	return string(ja) == string(jb)
}
