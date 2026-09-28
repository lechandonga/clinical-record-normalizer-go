// Package identity resolves source records to unified patients, merges the
// same person across sources, and resolves demographic conflicts by a fixed
// deterministic rule while preserving the source trail.
package identity

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"

	"github.com/lechandonga/clinical-record-normalizer-go/model"
)

// sourceScopedSystems are identifier systems whose numbering is only
// meaningful within the assigning source. The same MRN value at two
// different hospitals MUST NOT merge two different people, so these keys
// are namespaced by the source feed.
var sourceScopedSystems = map[string]bool{
	"MRN":  true,
	"HOSP": true,
}

// globalSystems are identifier systems issued by a single authority and
// therefore comparable across all source feeds.
var globalSystems = map[string]bool{
	"SSN":      true,
	"PASSPORT": true,
	"NHID":     true, // national health id
}

// Resolver maps match keys to unified patient IDs and holds merged patients.
// All methods are serial; concurrency is coordinated by the registry.
type Resolver struct {
	// keyToPatient maps a match key to the surviving patient ID.
	keyToPatient map[string]string
	patients     map[string]*model.Patient
}

// NewResolver creates an empty identity resolver.
func NewResolver() *Resolver {
	return &Resolver{
		keyToPatient: make(map[string]string),
		patients:     make(map[string]*model.Patient),
	}
}

// MatchKey returns the deterministic key by which two identifiers refer to
// the same person. MRN-like identifiers are scoped by source/assigner so
// that two hospitals reusing the same MRN number do not get merged.
func MatchKey(source string, id model.Identifier) string {
	sys := strings.ToUpper(strings.TrimSpace(id.System))
	val := strings.TrimSpace(id.Value)
	if sourceScopedSystems[sys] {
		return "src:" + source + "|" + sys + "|" + val
	}
	// Unknown systems are treated as source-scoped too: never merge on an
	// identifier whose issuing authority we cannot establish.
	return "g:" + sys + "|" + val
}

// Resolve assigns (or finds) the unified patient ID for a normalized
// record and merges its demographics into the patient aggregate.
// A hard identity contradiction (same identifier claimed by two
// demonstrably different people) yields RejectIdentityConflict.
func (r *Resolver) Resolve(rec *model.Record) *model.Rejection {
	src := rec.Source
	keys := make([]string, 0, len(rec.Patient.Identifiers))
	for _, id := range rec.Patient.Identifiers {
		keys = append(keys, MatchKey(src.Source, id))
	}
	sort.Strings(keys)

	// Find existing patients referenced by any of these keys.
	existing := make(map[string]bool)
	for _, k := range keys {
		if pid, ok := r.keyToPatient[k]; ok {
			existing[pid] = true
		}
	}
	pids := make([]string, 0, len(existing))
	for pid := range existing {
		pids = append(pids, pid)
	}
	sort.Strings(pids)

	// Hard conflict check: each claimed key must be compatible with every
	// patient it references.
	for _, k := range keys {
		pid, ok := r.keyToPatient[k]
		if !ok {
			continue
		}
		if cj := incompatible(r.patients[pid], rec.Patient, k); cj != nil {
			return cj
		}
	}

	// Merge all referenced patients; the lexicographically smallest ID
	// survives so the outcome is independent of processing order.
	target := ""
	if len(pids) == 0 {
		target = newPatientID(keys)
		r.patients[target] = &model.Patient{PatientID: target}
	} else {
		target = pids[0]
		for _, other := range pids[1:] {
			r.mergeInto(target, other)
		}
	}

	// Bind all keys to the surviving patient.
	for _, k := range keys {
		r.keyToPatient[k] = target
	}

	// Merge the incoming demographics.
	r.mergeDemographics(target, rec.Patient)
	rec.Patient = *r.copyPatient(target)
	rec.Patient.PatientID = target
	rec.Encounter.PatientID = target
	return nil
}

func incompatible(existing *model.Patient, incoming model.Patient, key string) *model.Rejection {
	if existing.BirthDate.Value != "" && incoming.BirthDate.Value != "" &&
		existing.BirthDate.Value != incoming.BirthDate.Value {
		return &model.Rejection{
			Source: model.SourceRef{
				Source:   incoming.Sources[0],
				RecordID: incoming.BirthDate.RecordID,
			},
			Reason: model.RejectIdentityConflict,
			Field:  "patient.birthDate",
			Detail: "identifier " + key + " is already linked to a patient with birthDate " +
				existing.BirthDate.Value + "; refusing to merge",
		}
	}
	if existing.Gender.Value != "" && incoming.Gender.Value != "" &&
		existing.Gender.Value != "unknown" && incoming.Gender.Value != "unknown" &&
		existing.Gender.Value != incoming.Gender.Value {
		return &model.Rejection{
			Source: model.SourceRef{
				Source:   incoming.Sources[0],
				RecordID: incoming.Gender.RecordID,
			},
			Reason: model.RejectIdentityConflict,
			Field:  "patient.gender",
			Detail: "identifier " + key + " is already linked to a patient with gender " +
				existing.Gender.Value + "; refusing to merge",
		}
	}
	return nil
}

// mergeInto folds patient other into dst (including key bindings).
func (r *Resolver) mergeInto(dst, other string) {
	if dst == other {
		return
	}
	a := r.patients[dst]
	b := r.patients[other]
	for k, pid := range r.keyToPatient {
		if pid == other {
			r.keyToPatient[k] = dst
		}
	}
	// Union identifiers.
	for _, id := range b.Identifiers {
		if !hasIdentifier(a.Identifiers, id) {
			a.Identifiers = append(a.Identifiers, id)
		}
	}
	a.Identifiers = sortIDs(a.Identifiers)
	a.Sources = unionSorted(a.Sources, b.Sources)
	a.Conflicts = append(a.Conflicts, b.Conflicts...)
	r.mergeAttrs(a, b)
	if b.UpdatedAt.After(a.UpdatedAt) {
		a.UpdatedAt = b.UpdatedAt
	}
	delete(r.patients, other)
}

func (r *Resolver) mergeDemographics(pid string, in model.Patient) {
	p := r.patients[pid]
	for _, id := range in.Identifiers {
		if !hasIdentifier(p.Identifiers, id) {
			p.Identifiers = append(p.Identifiers, id)
		}
	}
	p.Identifiers = sortIDs(p.Identifiers)
	p.Sources = unionSorted(p.Sources, in.Sources)
	r.mergeAttrs(p, &in)
	if in.UpdatedAt.After(p.UpdatedAt) {
		p.UpdatedAt = in.UpdatedAt
	}
	sort.Slice(p.Conflicts, func(i, j int) bool {
		return conflictLess(p.Conflicts[i], p.Conflicts[j])
	})
}

// mergeAttrs folds the attributed demographic fields of b into a, applying
// the deterministic conflict-resolution rule and recording every resolved
// disagreement.
func (r *Resolver) mergeAttrs(a *model.Patient, b *model.Patient) {
	a.Name = choose(a, a.Name, b.Name, "patient.name")
	a.Gender = choose(a, a.Gender, b.Gender, "patient.gender")
	a.BirthDate = choose(a, a.BirthDate, b.BirthDate, "patient.birthDate")
}

// choose applies the precedence rule:
//  1. the value asserted at the latest assumedAt time wins;
//  2. on equal timestamps, the assertion from the lexicographically larger
//     (source, recordId) wins — a fixed total order with no wall-clock or
//     scheduling dependency;
//  3. an empty incoming value never overwrites an existing one.
func choose(p *model.Patient, old, cand model.AttributedValue, field string) model.AttributedValue {
	if cand.Value == "" {
		return old
	}
	if old.Value == "" {
		return cand
	}
	if old.Value == cand.Value {
		return old
	}
	winner, loser := old, cand
	if candValueWins(cand, old) {
		winner, loser = cand, old
	}
	p.Conflicts = append(p.Conflicts, model.FieldConflict{
		Field:      field,
		Chosen:     winner.Value,
		ChosenFrom: model.SourceRef{Source: winner.Source, RecordID: winner.RecordID},
		Rejected:   loser.Value,
		RejectedBy: model.SourceRef{Source: loser.Source, RecordID: loser.RecordID},
		Reason:     "latest AssumedAt wins; ties broken by (source,recordId) order",
	})
	return winner
}

func candValueWins(cand, old model.AttributedValue) bool {
	if !cand.AssumedAt.Equal(old.AssumedAt) {
		return cand.AssumedAt.After(old.AssumedAt)
	}
	if cand.Source != old.Source {
		return cand.Source > old.Source
	}
	return cand.RecordID > old.RecordID
}

// Patient returns a copy of the merged patient aggregate.
func (r *Resolver) Patient(id string) (model.Patient, bool) {
	if _, ok := r.patients[id]; !ok {
		return model.Patient{}, false
	}
	return *r.copyPatient(id), true
}

func (r *Resolver) copyPatient(id string) *model.Patient {
	p := r.patients[id]
	cp := *p
	cp.Identifiers = append([]model.Identifier(nil), p.Identifiers...)
	cp.Sources = append([]string(nil), p.Sources...)
	cp.Conflicts = append([]model.FieldConflict(nil), p.Conflicts...)
	return &cp
}

// Patients returns all merged patients ordered by patient ID.
func (r *Resolver) Patients() []model.Patient {
	ids := make([]string, 0, len(r.patients))
	for id := range r.patients {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]model.Patient, 0, len(ids))
	for _, id := range ids {
		out = append(out, *r.copyPatient(id))
	}
	return out
}

func newPatientID(keys []string) string {
	sum := sha256.Sum256([]byte(strings.Join(keys, "\n")))
	return "pat-" + hex.EncodeToString(sum[:])[:20]
}

func hasIdentifier(ids []model.Identifier, id model.Identifier) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

func sortIDs(ids []model.Identifier) []model.Identifier {
	sort.Slice(ids, func(i, j int) bool {
		if ids[i].System != ids[j].System {
			return ids[i].System < ids[j].System
		}
		return ids[i].Value < ids[j].Value
	})
	return ids
}

func unionSorted(a, b []string) []string {
	seen := make(map[string]bool, len(a)+len(b))
	for _, s := range append(append([]string{}, a...), b...) {
		seen[s] = true
	}
	out := make([]string, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

func conflictLess(a, b model.FieldConflict) bool {
	if a.Field != b.Field {
		return a.Field < b.Field
	}
	if a.ChosenFrom != b.ChosenFrom {
		return a.ChosenFrom.Source < b.ChosenFrom.Source
	}
	return a.ChosenFrom.RecordID < b.ChosenFrom.RecordID
}
