package terminology

import "testing"

func TestVersionedMapIsReproducible(t *testing.T) {
	s := NewService()
	v1 := &Codeset{Name: "diagnosis", Version: "v1", System: "ICD-10", Entries: map[string]Entry{
		"E11.9": {Code: "E11.9", Display: "DM2", CanonicalCode: "E11.9"},
	}}
	v2 := &Codeset{Name: "diagnosis", Version: "v2", System: "ICD-10", Entries: map[string]Entry{
		"E11.9":  {Code: "E11.9", Display: "DM2 old", CanonicalCode: "E11.90", Deprecated: true, DeprecatedIn: "v2"},
		"E11.90": {Code: "E11.90", Display: "DM2", CanonicalCode: "E11.90"},
	}}
	if err := s.Load(v1); err != nil {
		t.Fatal(err)
	}
	if err := s.Load(v2); err != nil {
		t.Fatal(err)
	}

	// A mapping taken against v1 must keep saying "active" after v2 loads.
	r1, ok := s.Map("diagnosis", "v1", "E11.9")
	if !ok || r1.Status != StatusActive || r1.CanonicalCode != "E11.9" {
		t.Fatalf("v1 mapping changed after v2 load: %+v ok=%v", r1, ok)
	}
	// The same code against v2 is deprecated with a replacement code.
	r2, _ := s.Map("diagnosis", "v2", "E11.9")
	if r2.Status != StatusDeprecated || r2.CanonicalCode != "E11.90" {
		t.Fatalf("expected deprecated+remap in v2, got %+v", r2)
	}
	// New code in v2 is unknown in v1.
	ru, _ := s.Map("diagnosis", "v1", "E11.90")
	if ru.Status != StatusUnknown {
		t.Fatalf("expected unknown in v1, got %+v", ru)
	}
	rn, _ := s.Map("diagnosis", "v2", "E11.90")
	if rn.Status != StatusActive {
		t.Fatalf("expected active in v2, got %+v", rn)
	}
}

func TestActiveVersionSwitch(t *testing.T) {
	s := NewService()
	_ = s.Load(&Codeset{Name: "c", Version: "v1", Entries: map[string]Entry{}})
	_ = s.Load(&Codeset{Name: "c", Version: "v2", Entries: map[string]Entry{}})
	if v, _ := s.ActiveVersion("c"); v != "v1" {
		t.Fatalf("first loaded version should be active, got %s", v)
	}
	if err := s.SetActiveVersion("c", "v2"); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.ActiveVersion("c"); v != "v2" {
		t.Fatalf("expected v2 active, got %s", v)
	}
	if err := s.SetActiveVersion("c", "v9"); err == nil {
		t.Fatal("expected error switching to a missing version")
	}
}

func TestReloadSameVersionRejected(t *testing.T) {
	s := NewService()
	cs := &Codeset{Name: "c", Version: "v1", Entries: map[string]Entry{
		"A": {Code: "A", Display: "a"},
	}}
	if err := s.Load(cs); err != nil {
		t.Fatal(err)
	}
	cs.Entries["A"] = Entry{Code: "A", Display: "MUTATED"}
	if err := s.Load(cs); err == nil {
		t.Fatal("re-loading the same (name, version) must be rejected to protect history")
	}
	// And the registered copy must be isolated from caller mutation.
	r, _ := s.Map("c", "v1", "A")
	if r.Display != "a" {
		t.Fatalf("registered codeset was mutated: %q", r.Display)
	}
}
