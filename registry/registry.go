// Package registry is the concurrency-safe, idempotent entry point for
// importing batches of raw clinical messages.
package registry

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"time"

	"github.com/lechandonga/clinical-record-normalizer-go/identity"
	"github.com/lechandonga/clinical-record-normalizer-go/ingest"
	"github.com/lechandonga/clinical-record-normalizer-go/model"
	"github.com/lechandonga/clinical-record-normalizer-go/terminology"
)

// Registry holds codesets, the identity resolver and already-imported
// source records, and produces deterministic import reports.
type Registry struct {
	terms    *terminology.Service
	resolver *identity.Resolver
	clock    func() time.Time

	mu         chan struct{} // serializes whole imports (one permit)
	seen       map[string]bool
	encounters map[string]model.Encounter
}

// New creates a Registry.
func New(terms *terminology.Service) *Registry {
	return &Registry{
		terms:      terms,
		resolver:   identity.NewResolver(),
		clock:      time.Now,
		mu:         make(chan struct{}, 1),
		seen:       make(map[string]bool),
		encounters: make(map[string]model.Encounter),
	}
}

// WithClock overrides the clock (used for records without an explicit
// receivedAt and for future-time checks). Mainly used for reproducible
// tests and local sample verification.
func (r *Registry) WithClock(c func() time.Time) *Registry {
	r.clock = c
	return r
}

// ImportBatch normalizes a batch of raw messages and merges them.
//
// Importing the same batch (or the same records) repeatedly yields exactly
// the same report and final state as importing it once. Concurrent batch
// imports are serialized through a single permit and therefore produce the
// same result as serial imports.
func (r *Registry) ImportBatch(batchID string, raws []model.RawMessage) (model.ImportReport, error) {
	r.mu <- struct{}{}
	defer func() { <-r.mu }()

	// Pin the codeset versions that are active for THIS batch. Switching
	// the active versions afterwards can never alter these results.
	versions := make(map[string]string)
	for _, name := range []string{ingest.CodesetDiagnosis, ingest.CodesetEncounterType} {
		v, ok := r.terms.ActiveVersion(name)
		if !ok {
			return model.ImportReport{}, &VersionError{Codeset: name}
		}
		versions[name] = v
	}
	normalizer := ingest.NewNormalizer(r.terms, versions, r.clock)

	// Deterministic processing order, independent of arrival order:
	// sort by (source, recordId). Duplicate raw bodies in the batch are
	// de-duplicated by that same key.
	ordered := make([]model.RawMessage, len(raws))
	copy(ordered, raws)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Source != ordered[j].Source {
			return ordered[i].Source < ordered[j].Source
		}
		return ordered[i].RecordID < ordered[j].RecordID
	})

	report := model.ImportReport{
		BatchID:          batchID,
		CodesetVersion:   normalizer.PinnedVersion(),
		RejectedByReason: make(map[model.RejectReason]int),
	}

	acceptedKeys := make(map[string]bool)
	var rejections []model.Rejection
	for _, raw := range ordered {
		key := raw.Source + "/" + raw.RecordID
		norm, rej := normalizer.Normalize(raw)
		if rej != nil {
			report.RejectedCount++
			report.RejectedByReason[rej.Reason]++
			rejections = append(rejections, *rej)
			continue
		}
		report.AcceptedCount++
		// Idempotency: the batch report counts the record as accepted,
		// but the identity/encounter state only absorbs it once.
		if r.seen[key] || acceptedKeys[key] {
			continue
		}
		acceptedKeys[key] = true
		if cj := r.resolver.Resolve(&norm.Partial); cj != nil {
			// Identity-level hard contradiction: distinct reason, record
			// not applied to state.
			report.AcceptedCount--
			report.RejectedCount++
			report.RejectedByReason[cj.Reason]++
			cj.RawInput = string(raw.Body)
			rejections = append(rejections, *cj)
			continue
		}
		r.seen[key] = true
		r.encounters[norm.Partial.Encounter.EncounterID] = norm.Partial.Encounter
	}

	sort.Slice(rejections, func(i, j int) bool {
		if rejections[i].Source != rejections[j].Source {
			return srcLess(rejections[i].Source, rejections[j].Source)
		}
		return rejections[i].Field < rejections[j].Field
	})
	if len(rejections) > 0 {
		report.Rejections = rejections
	}
	report.PatientCount = len(r.resolver.Patients())
	report.EncounterCount = len(r.encounters)
	report.Fingerprint = r.fingerprint()
	return report, nil
}

// Patients returns the current merged patients ordered by ID.
func (r *Registry) Patients() []model.Patient {
	r.mu <- struct{}{}
	defer func() { <-r.mu }()
	return r.resolver.Patients()
}

// Fingerprint hashes the canonical JSON snapshot of the full merged state.
// Equal state always yields the same fingerprint, regardless of import
// order or concurrency.
func (r *Registry) fingerprint() string {
	type snapshot struct {
		Patients   []model.Patient   `json:"patients"`
		Encounters []model.Encounter `json:"encounters"`
	}
	encs := r.EncountersLocked()
	buf, err := json.Marshal(snapshot{Patients: r.resolver.Patients(), Encounters: encs})
	if err != nil {
		panic(err) // all fields are plain JSON-able types
	}
	sum := sha256.Sum256(buf)
	return hex.EncodeToString(sum[:])
}

func srcLess(a, b model.SourceRef) bool {
	if a.Source != b.Source {
		return a.Source < b.Source
	}
	return a.RecordID < b.RecordID
}

// VersionError indicates that a required codeset has no active version.
type VersionError struct{ Codeset string }

func (e *VersionError) Error() string { return "no active codeset version for " + e.Codeset }
