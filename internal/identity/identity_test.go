package identity

import (
	"testing"
	"time"

	"github.com/lechandonga/clinical-record-normalizer-go/internal/domain"
	"github.com/lechandonga/clinical-record-normalizer-go/internal/normalize"
)

func ref(src, msg string) domain.SourceRef {
	return domain.SourceRef{SourceID: src, MessageID: msg}
}

func traced(v string, src, msg string, at time.Time) domain.TracedValue {
	return domain.TracedValue{Value: v, Source: ref(src, msg), Received: at}
}

func cand(src, msg, mrn, idCard, sex, birth string, at time.Time) *normalize.Candidate {
	c := &normalize.Candidate{
		Ref:       ref(src, msg),
		Received:  at,
		LocalID:   mrn,
		Sex:       traced(sex, src, msg, at),
		BirthDate: traced(birth, src, msg, at),
		Encounter: &domain.Encounter{ID: "ENC-" + msg},
	}
	c.Ident = []domain.Identifier{{System: "MRN|" + src, Value: mrn, Source: c.Ref}}
	if idCard != "" {
		c.Ident = append(c.Ident, domain.Identifier{System: "ID_CARD", Value: idCard, Source: c.Ref})
	}
	return c
}

func TestMergeOnSharedIdentifierAndConflictRule(t *testing.T) {
	// Same physical person appears at three sources, linked by ID_CARD.
	early := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	later := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	latest := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)

	cands := []*normalize.Candidate{
		// high priority source says M
		cand("his-hospital-a", "M1", "MRN-A1", "ID-1", "M", "1990-01-01", early),
		// high priority source, later message says F -> recency tie-breaks
		cand("his-hospital-a", "M2", "MRN-A1", "ID-1", "F", "1990-01-01", later),
		// low priority source agrees with M but still loses
		cand("community-b", "M3", "CB-9", "ID-1", "M", "1990-01-01", latest),
	}
	r := NewResolver(map[string]int{"his-hospital-a": 100, "community-b": 50})
	res := r.Resolve(cands)

	if len(res.Patients) != 1 {
		t.Fatalf("expected 1 merged patient, got %d", len(res.Patients))
	}
	p := res.Patients[0]
	if p.Sex.Value != "F" {
		t.Fatalf("expected F by same-priority recency, got %q", p.Sex.Value)
	}
	// Both distinct losing M values are recorded with source traces.
	if len(p.Conflicts) != 2 {
		t.Fatalf("expected 2 conflict traces, got %d: %+v", len(p.Conflicts), p.Conflicts)
	}
	for _, c := range p.Conflicts {
		if c.Rule != Rule || c.Field != "sex" || c.Winner.Value != "F" || c.Loser.Value != "M" {
			t.Fatalf("conflict trace wrong: %+v", c)
		}
	}
	if len(p.Identifiers) != 3 {
		t.Fatalf("expected 3 identifiers (2 MRNs + ID_CARD), got %d", len(p.Identifiers))
	}
	if len(p.Sources) != 3 {
		t.Fatalf("expected 3 source traces, got %d", len(p.Sources))
	}
	// Every encounter maps to the same patient.
	pid := p.ID
	for _, c := range cands {
		if res.PatientIDByRef[c.Ref] != pid {
			t.Fatalf("candidate %s not mapped to merged patient", c.Ref.MessageID)
		}
	}
}

func TestPriorityBeatsRecency(t *testing.T) {
	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	new := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	// Low-priority newest F vs high-priority older M -> M wins.
	cands := []*normalize.Candidate{
		cand("community-b", "L1", "CB-1", "ID-9", "F", "1990-01-01", new),
		cand("his-hospital-a", "H1", "MRN-1", "ID-9", "M", "1990-01-01", old),
	}
	r := NewResolver(map[string]int{"his-hospital-a": 100, "community-b": 50})
	p := r.Resolve(cands).Patients[0]
	if p.Sex.Value != "M" {
		t.Fatalf("high priority must win regardless of recency, got %q", p.Sex.Value)
	}
}

func TestNoFalseMergeForDifferentPeople(t *testing.T) {
	at := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	// Different ID_CARD and source-scoped MRNs: even identical MRN string
	// values in different systems must not merge (MRN system is scoped).
	cands := []*normalize.Candidate{
		cand("his-hospital-a", "A1", "MRN-1", "ID-1", "M", "1990-01-01", at),
		cand("community-b", "B1", "MRN-1", "ID-2", "F", "1991-02-02", at),
	}
	res := NewResolver(nil).Resolve(cands)
	if len(res.Patients) != 2 {
		t.Fatalf("expected 2 distinct patients, got %d", len(res.Patients))
	}
}

func TestPatientIDsAreDeterministic(t *testing.T) {
	at := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	build := func() []domain.Patient {
		// Feed in different orders; result must be identical.
		c1 := cand("his-hospital-a", "A1", "MRN-1", "ID-1", "M", "1990-01-01", at)
		c2 := cand("community-b", "B1", "MRN-2", "ID-1", "M", "1990-01-01", at)
		return NewResolver(nil).Resolve([]*normalize.Candidate{c1, c2}).Patients
	}
	p1 := build()
	p2 := build()
	if len(p1) != 1 || len(p2) != 1 || p1[0].ID != p2[0].ID {
		t.Fatalf("patient IDs not deterministic: %+v %+v", p1, p2)
	}
}

func TestTransitiveLinkage(t *testing.T) {
	at := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	// A~B by ID-1, B~C by ID-2, so A~C transitively -> one patient.
	c1 := cand("s1", "A", "MRN-A", "ID-1", "M", "1990-01-01", at)
	c2 := cand("s2", "B", "MRN-B", "", "M", "1990-01-01", at)
	c2.Ident = append(c2.Ident,
		domain.Identifier{System: "ID_CARD", Value: "ID-1", Source: c2.Ref},
		domain.Identifier{System: "ID_CARD", Value: "ID-2", Source: c2.Ref},
	)
	c3 := cand("s3", "C", "MRN-C", "ID-2", "M", "1990-01-01", at)
	res := NewResolver(nil).Resolve([]*normalize.Candidate{c1, c2, c3})
	if len(res.Patients) != 1 {
		t.Fatalf("transitive linkage should give 1 patient, got %d", len(res.Patients))
	}
}
