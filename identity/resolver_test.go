package identity

import (
	"testing"
	"time"

	"github.com/lechandonga/clinical-record-normalizer-go/model"
)

func attributed(v, src, rid, assumed string) model.AttributedValue {
	t, _ := time.Parse(time.RFC3339, assumed)
	return model.AttributedValue{Value: v, Source: src, RecordID: rid, AssumedAt: t}
}

func rec(source, rid string, ids []model.Identifier, name, gender, bd, assumed string) *model.Record {
	return &model.Record{
		DedupKey: source + "/" + rid,
		Source:   model.SourceRef{Source: source, RecordID: rid},
		Patient: model.Patient{
			Identifiers: ids,
			Name:        attributed(name, source, rid, assumed),
			Gender:      attributed(gender, source, rid, assumed),
			BirthDate:   attributed(bd, source, rid, assumed),
			Sources:     []string{source},
		},
		Encounter: model.Encounter{
			EncounterID: "enc-" + source + "-" + rid,
			Source:      model.SourceRef{Source: source, RecordID: rid},
		},
	}
}

func mrn(v string) []model.Identifier { return []model.Identifier{{System: "MRN", Value: v}} }

func TestMRNIsSourceScoped(t *testing.T) {
	// Same MRN value at two hospitals, no shared global id: two people.
	r := NewResolver()
	if cj := r.Resolve(rec("hospitalA", "1", mrn("MRN-9"), "A Person", "male", "1980-01-01", "2026-01-01T00:00:00Z")); cj != nil {
		t.Fatal(cj)
	}
	if cj := r.Resolve(rec("hospitalB", "1", mrn("MRN-9"), "Another Person", "female", "1990-01-01", "2026-01-01T00:00:00Z")); cj != nil {
		t.Fatal(cj)
	}
	if got := len(r.Patients()); got != 2 {
		t.Fatalf("same MRN at different sources must not merge, got %d patients", got)
	}
}

func TestGlobalIdentifierMergesAcrossSources(t *testing.T) {
	r := NewResolver()
	a := rec("hospitalA", "1",
		[]model.Identifier{{System: "MRN", Value: "M-1"}, {System: "SSN", Value: "S-1"}},
		"Old Name", "male", "1980-05-12", "2026-01-01T00:00:00Z")
	b := rec("hospitalB", "2",
		[]model.Identifier{{System: "MRN", Value: "X-9"}, {System: "SSN", Value: "S-1"}},
		"New Name", "male", "1980-05-12", "2026-06-01T00:00:00Z")
	if cj := r.Resolve(a); cj != nil {
		t.Fatal(cj)
	}
	if cj := r.Resolve(b); cj != nil {
		t.Fatal(cj)
	}
	ps := r.Patients()
	if len(ps) != 1 {
		t.Fatalf("shared SSN must merge across sources, got %d patients", len(ps))
	}
	p := ps[0]
	if len(p.Identifiers) != 3 {
		t.Fatalf("merged patient should hold the union of identifiers, got %v", p.Identifiers)
	}
	if p.Name.Value != "New Name" {
		t.Fatalf("latest AssumedAt must win, got %q", p.Name.Value)
	}
	if len(p.Conflicts) != 1 {
		t.Fatalf("expected one recorded name conflict, got %d", len(p.Conflicts))
	}
	c := p.Conflicts[0]
	if c.Chosen != "New Name" || c.Rejected != "Old Name" {
		t.Fatalf("conflict trail wrong: %+v", c)
	}
	if p.PatientID != a.Patient.PatientID {
		t.Fatalf("both records must resolve to the same stable patient ID: %s vs %s", p.PatientID, a.Patient.PatientID)
	}
}

func TestConflictTieBreakerIsDeterministic(t *testing.T) {
	// Same assumedAt: lexicographically larger (source, recordId) wins,
	// independent of arrival order.
	build := func() *Resolver {
		r := NewResolver()
		a := rec("hospitalA", "1",
			[]model.Identifier{{System: "SSN", Value: "S-2"}},
			"Name A", "female", "1970-01-01", "2026-01-01T00:00:00Z")
		b := rec("hospitalB", "1",
			[]model.Identifier{{System: "SSN", Value: "S-2"}},
			"Name B", "female", "1970-01-01", "2026-01-01T00:00:00Z")
		_ = r.Resolve(a)
		_ = r.Resolve(b)
		return r
	}
	p1 := build().Patients()[0]

	r2 := NewResolver()
	b := rec("hospitalB", "1",
		[]model.Identifier{{System: "SSN", Value: "S-2"}},
		"Name B", "female", "1970-01-01", "2026-01-01T00:00:00Z")
	a := rec("hospitalA", "1",
		[]model.Identifier{{System: "SSN", Value: "S-2"}},
		"Name A", "female", "1970-01-01", "2026-01-01T00:00:00Z")
	_ = r2.Resolve(b)
	_ = r2.Resolve(a)
	p2 := r2.Patients()[0]

	if p1.PatientID != p2.PatientID || p1.Name.Value != "Name B" || p2.Name.Value != "Name B" {
		t.Fatalf("arrival order changed the result:\n%+v\n%+v", p1, p2)
	}
}

func TestHardIdentityContradictionRejected(t *testing.T) {
	r := NewResolver()
	_ = r.Resolve(rec("hospitalB", "1",
		[]model.Identifier{{System: "SSN", Value: "S-3"}},
		"Person One", "male", "1980-05-12", "2026-01-01T00:00:00Z"))
	cj := r.Resolve(rec("hospitalA", "2",
		[]model.Identifier{{System: "SSN", Value: "S-3"}},
		"Person Two", "female", "1999-01-01", "2026-01-01T00:00:00Z"))
	if cj == nil || cj.Reason != model.RejectIdentityConflict {
		t.Fatalf("expected identity_conflict rejection, got %+v", cj)
	}
	if len(r.Patients()) != 1 {
		t.Fatal("rejected record must not alter patient state")
	}
}

func TestUnknownGenderDoesNotConflict(t *testing.T) {
	r := NewResolver()
	_ = r.Resolve(rec("hospitalA", "1",
		[]model.Identifier{{System: "SSN", Value: "S-4"}},
		"P", "unknown", "1980-05-12", "2026-01-01T00:00:00Z"))
	if cj := r.Resolve(rec("hospitalB", "2",
		[]model.Identifier{{System: "SSN", Value: "S-4"}},
		"P", "female", "1980-05-12", "2026-01-01T00:00:00Z")); cj != nil {
		t.Fatalf("unknown vs known gender should merge, got %+v", cj)
	}
	if len(r.Patients()) != 1 || r.Patients()[0].Gender.Value != "female" {
		t.Fatalf("known gender should be adopted, got %v", r.Patients())
	}
}
