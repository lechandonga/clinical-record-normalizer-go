package registry

import (
	"sort"

	"github.com/lechandonga/clinical-record-normalizer-go/model"
)

// Encounters returns imported encounters ordered by encounter ID.
func (r *Registry) Encounters() []model.Encounter {
	r.mu <- struct{}{}
	defer func() { <-r.mu }()
	return r.EncountersLocked()
}

// EncountersLocked returns sorted encounters; caller must hold the import
// permit (used internally while computing the fingerprint).
func (r *Registry) EncountersLocked() []model.Encounter {
	out := make([]model.Encounter, 0, len(r.encounters))
	for _, e := range r.encounters {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].PatientID != out[j].PatientID {
			return out[i].PatientID < out[j].PatientID
		}
		return out[i].EncounterID < out[j].EncounterID
	})
	return out
}
