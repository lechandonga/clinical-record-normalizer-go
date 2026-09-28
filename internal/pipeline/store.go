package pipeline

import (
	"sort"
	"sync"

	"github.com/lechandonga/clinical-record-normalizer-go/internal/domain"
)

// Store is the concurrency-safe accumulation of imported batches. Applying
// the same batch twice (or concurrently) yields the same resulting state.
type Store struct {
	mu         sync.Mutex
	patients   map[string]domain.Patient
	encounters map[string]domain.Encounter
	sourceSeen map[domain.SourceRef]string // message ref -> batch id
	batchSeen  map[string]struct{}
}

// NewStore returns an empty store.
func NewStore() *Store {
	return &Store{
		patients:   map[string]domain.Patient{},
		encounters: map[string]domain.Encounter{},
		sourceSeen: map[domain.SourceRef]string{},
		batchSeen:  map[string]struct{}{},
	}
}

// Apply atomically merges one batch. A batch whose ID was already applied is
// a no-op replay: the current merged state is returned unchanged.
func (s *Store) Apply(rep Report) Report {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.batchSeen[rep.Batch.ID]; !ok {
		s.batchSeen[rep.Batch.ID] = struct{}{}
		s.applyLocked(rep)
	}
	return s.snapshotLocked(rep.Batch)
}

func (s *Store) applyLocked(rep Report) {
	for _, enc := range rep.Encounters {
		// Idempotency key: encounter content hash already includes source ref.
		if _, exists := s.encounters[enc.ID]; !exists {
			s.encounters[enc.ID] = enc
		}
	}

	for _, pat := range rep.Patients {
		cur, exists := s.patients[pat.ID]
		if !exists {
			s.patients[pat.ID] = pat
			continue
		}
		s.patients[pat.ID] = mergePatient(cur, pat)
	}

	for _, ref := range patientSourceRefs(rep) {
		if _, ok := s.sourceSeen[ref]; !ok {
			s.sourceSeen[ref] = rep.Batch.ID
		}
	}
}

// mergePatient unions sources/identifiers and keeps conflict traces from both.
// Re-running the same batch produces identical input, hence identical output.
func mergePatient(a, b domain.Patient) domain.Patient {
	out := a

	idSeen := map[string]bool{}
	for _, id := range out.Identifiers {
		idSeen[id.System+"|"+id.Value] = true
	}
	for _, id := range b.Identifiers {
		if !idSeen[id.System+"|"+id.Value] {
			idSeen[id.System+"|"+id.Value] = true
			out.Identifiers = append(out.Identifiers, id)
		}
	}

	srcSeen := map[domain.SourceRef]bool{}
	for _, ref := range out.Sources {
		srcSeen[ref] = true
	}
	for _, ref := range b.Sources {
		if !srcSeen[ref] {
			srcSeen[ref] = true
			out.Sources = append(out.Sources, ref)
		}
	}

	confSeen := map[string]bool{}
	for _, c := range out.Conflicts {
		confSeen[conflictKey(c)] = true
	}
	for _, c := range b.Conflicts {
		if !confSeen[conflictKey(c)] {
			confSeen[conflictKey(c)] = true
			out.Conflicts = append(out.Conflicts, c)
		}
	}
	return out
}

func conflictKey(c domain.FieldConflict) string {
	return c.Field + "|" +
		c.Winner.Source.SourceID + "|" + c.Winner.Source.MessageID + "|" + c.Winner.Value + "|" +
		c.Loser.Source.SourceID + "|" + c.Loser.Source.MessageID + "|" + c.Loser.Value
}

func patientSourceRefs(rep Report) []domain.SourceRef {
	var refs []domain.SourceRef
	for _, p := range rep.Patients {
		refs = append(refs, p.Sources...)
	}
	return refs
}

func (s *Store) snapshotLocked(batch Batch) Report {
	patients := make([]domain.Patient, 0, len(s.patients))
	for _, p := range s.patients {
		// deterministic ordering of nested slices
		sortIdentifiers(p.Identifiers)
		sortRefs(p.Sources)
		sortConflicts(p.Conflicts)
		patients = append(patients, p)
	}
	sort.Slice(patients, func(i, j int) bool { return patients[i].ID < patients[j].ID })

	encounters := make([]domain.Encounter, 0, len(s.encounters))
	for _, e := range s.encounters {
		sortRefs(e.Sources)
		sortDiagnoses(e.Diagnoses)
		encounters = append(encounters, e)
	}
	sort.Slice(encounters, func(i, j int) bool { return encounters[i].ID < encounters[j].ID })

	return Report{
		Batch:      batch,
		Accepted:   len(s.sourceSeen),
		Rejected:   0,
		Patients:   patients,
		Encounters: encounters,
	}
}

func sortRefs(refs []domain.SourceRef) {
	sort.Slice(refs, func(i, j int) bool { return lessRef(refs[i], refs[j]) })
}

func sortIdentifiers(ids []domain.Identifier) {
	sort.Slice(ids, func(i, j int) bool {
		if ids[i].System != ids[j].System {
			return ids[i].System < ids[j].System
		}
		return ids[i].Value < ids[j].Value
	})
}

func sortConflicts(cs []domain.FieldConflict) {
	sort.SliceStable(cs, func(i, j int) bool {
		return conflictKey(cs[i]) < conflictKey(cs[j])
	})
}

func sortDiagnoses(ds []domain.Diagnosis) {
	sort.SliceStable(ds, func(i, j int) bool {
		if ds[i].Coding.Code != ds[j].Coding.Code {
			return ds[i].Coding.Code < ds[j].Coding.Code
		}
		return lessRef(ds[i].Source, ds[j].Source)
	})
}
