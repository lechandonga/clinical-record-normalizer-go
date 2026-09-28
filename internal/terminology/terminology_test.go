package terminology

import (
	"testing"

	"github.com/lechandonga/clinical-record-normalizer-go/internal/domain"
)

func v1Table() Table {
	return Table{
		System:  "diag",
		Version: "diag-v1",
		Entries: []TableEntry{
			{Code: "I10", Status: domain.MappingActive, ConceptSys: "DIAG", Concept: "HTN", Display: "高血压"},
			{Code: "OLD", Status: domain.MappingActive, ConceptSys: "DIAG", Concept: "X", Display: "旧"},
		},
	}
}

func v2Table() Table {
	return Table{
		System:  "diag",
		Version: "diag-v2",
		Entries: []TableEntry{
			{Code: "I10", Status: domain.MappingActive, ConceptSys: "DIAG", Concept: "HTN", Display: "高血压"},
			{Code: "OLD", Status: domain.MappingDeprecated, ConceptSys: "DIAG", Concept: "X", Display: "旧", ReplacedBy: "NEW"},
			{Code: "NEW", Status: domain.MappingActive, ConceptSys: "DIAG", Concept: "X", Display: "新"},
		},
	}
}

func TestMapStatusesAndPinnedVersion(t *testing.T) {
	r := NewRegistry()
	if err := r.Load(v1Table()); err != nil {
		t.Fatal(err)
	}
	if err := r.Load(v2Table()); err != nil {
		t.Fatal(err)
	}
	if err := r.SetDefault("diag", "diag-v1"); err != nil {
		t.Fatal(err)
	}

	// Active mapping.
	out, ok := r.Map("diag", "I10", "diag-v1")
	if !ok || out.Coding.Status != domain.MappingActive || out.Concept.Code != "HTN" {
		t.Fatalf("active mapping wrong: %+v", out)
	}

	// A code deprecated in v2 stays active when evaluated against pinned v1:
	// table updates must not change historical conclusions.
	out1, _ := r.Map("diag", "OLD", "diag-v1")
	out2, _ := r.Map("diag", "OLD", "diag-v2")
	if out1.Coding.Status != domain.MappingActive {
		t.Fatalf("v1 historical result changed: %+v", out1.Coding)
	}
	if out2.Coding.Status != domain.MappingDeprecated || out2.Coding.ReplacedBy != "NEW" {
		t.Fatalf("v2 deprecated mapping wrong: %+v", out2.Coding)
	}
	if out2.Coding.TableVersion != "diag-v2" {
		t.Fatalf("version not recorded on coding: %+v", out2.Coding)
	}

	// Well-formed but unknown code is marked, not dropped.
	unk, _ := r.Map("diag", "ZZZ", "diag-v1")
	if unk.Coding.Status != domain.MappingUnknown || unk.Concept != nil {
		t.Fatalf("unknown mapping wrong: %+v", unk)
	}

	// Empty requested version resolves to default v1.
	def, _ := r.Map("diag", "OLD", "")
	if def.Coding.TableVersion != "diag-v1" {
		t.Fatalf("default version wrong: %+v", def.Coding)
	}

	// Unknown requested version fails closed.
	if _, ok := r.Map("diag", "I10", "diag-v9"); ok {
		t.Fatal("expected unknown version to fail")
	}
}

func TestIsWellFormedCode(t *testing.T) {
	good := []string{"I10", "I21.9", "A-B_C"}
	bad := []string{"", "bad code!", "中文", "A\nB"}
	for _, c := range good {
		if !IsWellFormedCode(c) {
			t.Errorf("expected well-formed: %q", c)
		}
	}
	for _, c := range bad {
		if IsWellFormedCode(c) {
			t.Errorf("expected ill-formed: %q", c)
		}
	}
}

func TestLoadTableRejectsBadStatusAndShape(t *testing.T) {
	if _, err := LoadTable([]byte(`{not json`)); err == nil {
		t.Fatal("expected json error")
	}
	tbl, err := LoadTable([]byte(`{"system":"diag","version":"v","entries":[{"code":"A","status":"bogus"}]}`))
	if err == nil {
		t.Fatalf("expected bad status error, got %+v", tbl)
	}
	tbl, err = LoadTable([]byte(`{"system":"diag","version":"v","entries":[{"code":"A"}]}`))
	if err != nil || tbl.Entries[0].Status != domain.MappingActive {
		t.Fatalf("missing status should default to active, got %+v err=%v", tbl, err)
	}
}

func TestRegistryRejectsDuplicates(t *testing.T) {
	r := NewRegistry()
	if err := r.Load(v1Table()); err != nil {
		t.Fatal(err)
	}
	if err := r.Load(v1Table()); err == nil {
		t.Fatal("expected duplicate version error")
	}
	dup := Table{System: "diag", Version: "v3",
		Entries: []TableEntry{{Code: "A"}, {Code: "A"}}}
	if err := r.Load(dup); err == nil {
		t.Fatal("expected duplicate code error")
	}
}
