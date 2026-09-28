package pipeline

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"

	"github.com/lechandonga/clinical-record-normalizer-go/internal/ingest"
	"github.com/lechandonga/clinical-record-normalizer-go/internal/normalize"
	"github.com/lechandonga/clinical-record-normalizer-go/internal/terminology"
)

// repoRoot resolves the repository root regardless of package test CWD.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs("../../")
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

type testLogger struct{ t *testing.T }

func (l testLogger) LogRecord(name string, raw []byte, c *normalize.Candidate, rj *normalize.Rejection) {
	l.t.Logf("---- %s\nraw: %s", name, string(raw))
	if rj != nil {
		l.t.Logf("normalized: REJECTED %s (%s) %s", rj.Reason, rj.Field, rj.Detail)
	} else {
		b, _ := json.Marshal(c)
		l.t.Logf("normalized: %s", string(b))
	}
}

func loadFixture(t *testing.T) ([]Input, *ingest.Catalog, *terminology.Registry, map[string]int, string) {
	t.Helper()
	root := repoRoot(t)

	registry := terminology.NewRegistry()
	dir := filepath.Join(root, "tables", "diag")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0)
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".json" {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		tbl, err := terminology.LoadTable(raw)
		if err != nil {
			t.Fatal(err)
		}
		if err := registry.Load(tbl); err != nil {
			t.Fatal(err)
		}
	}

	profilesRaw, err := os.ReadFile(filepath.Join(root, "samples", "sources.json"))
	if err != nil {
		t.Fatal(err)
	}
	profiles, err := ingest.LoadProfiles(profilesRaw)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := ingest.NewCatalog(profiles)
	if err != nil {
		t.Fatal(err)
	}
	priorities := map[string]int{}
	for _, p := range profiles {
		priorities[p.ID] = p.Priority
	}

	sampleDir := filepath.Join(root, "samples", "messages")
	sentries, err := os.ReadDir(sampleDir)
	if err != nil {
		t.Fatal(err)
	}
	snames := make([]string, 0)
	for _, e := range sentries {
		if filepath.Ext(e.Name()) == ".json" {
			snames = append(snames, e.Name())
		}
	}
	sort.Strings(snames)
	inputs := make([]Input, 0, len(snames))
	for _, name := range snames {
		raw, err := os.ReadFile(filepath.Join(sampleDir, name))
		if err != nil {
			t.Fatal(err)
		}
		inputs = append(inputs, Input{Name: name, Raw: raw})
	}
	return inputs, catalog, registry, priorities, root
}

// reasonCounts summarizes rejected records by distinguishable reason.
func reasonCounts(rep Report) map[normalize.RejectReason]int {
	m := map[normalize.RejectReason]int{}
	for _, r := range rep.Rejections {
		m[r.Reason]++
	}
	return m
}

func TestConcurrentEqualsSerial(t *testing.T) {
	inputs, catalog, registry, priorities, _ := loadFixture(t)
	batch := Batch{ID: "BATCH-TEST-1", DiagVersion: "diag-v1"}

	serial := New(catalog, registry, priorities, testLogger{t}).RunSerial(inputs, batch)
	concurrent := New(catalog, registry, priorities, testLogger{t}).
		RunConcurrent(inputs, batch, 8)

	if !serial.Equal(&concurrent) {
		sj, _ := json.MarshalIndent(serial, "", "  ")
		cj, _ := json.MarshalIndent(concurrent, "", "  ")
		t.Fatalf("serial and concurrent reports differ\nserial:\n%s\nconcurrent:\n%s", sj, cj)
	}

	if serial.Accepted != 7 || serial.Rejected != 8 {
		t.Fatalf("unexpected counts: accepted=%d rejected=%d", serial.Accepted, serial.Rejected)
	}
	rc := reasonCounts(serial)
	want := map[normalize.RejectReason]int{
		normalize.RejectMalformedJSON: 1,
		normalize.RejectMissingField:  1,
		normalize.RejectInvalidCode:   1,
		normalize.RejectInvalidSex:    1,
		normalize.RejectAmbiguousTime: 1,
		normalize.RejectFutureBirth:   1,
		normalize.RejectUnknownSource: 1,
		normalize.RejectDuplicate:     1,
	}
	for reason, n := range want {
		if rc[reason] != n {
			t.Fatalf("reason %s: got %d want %d (all=%v)", reason, rc[reason], n, rc)
		}
	}

	// Cross-source merge: exactly 3 patients, one for Zhang, one for Li...
	if len(serial.Patients) != 3 {
		t.Fatalf("expected 3 merged patients, got %d", len(serial.Patients))
	}
}

func TestConcurrentImportIsStableAcrossRuns(t *testing.T) {
	inputs, catalog, registry, priorities, _ := loadFixture(t)
	batch := Batch{ID: "BATCH-TEST-2", DiagVersion: "diag-v1"}
	p := New(catalog, registry, priorities, nil)

	var first Report
	for run := 0; run < 20; run++ {
		rep := p.RunConcurrent(inputs, batch, 1+run%7)
		if run == 0 {
			first = rep
			continue
		}
		if !first.Equal(&rep) {
			t.Fatalf("concurrent run %d differs from run 0", run)
		}
	}
}

func TestShuffledInputOrderGivesSameResult(t *testing.T) {
	inputs, catalog, registry, priorities, _ := loadFixture(t)
	batch := Batch{ID: "BATCH-TEST-3", DiagVersion: "diag-v1"}
	p := New(catalog, registry, priorities, nil)

	base := p.RunSerial(inputs, batch)
	shuffled := append([]Input(nil), inputs...)
	reverse(shuffled)
	got := p.RunSerial(shuffled, batch)

	// Duplicate detection is "first in input order wins", so reverse order
	// changes which physical record is flagged; patients/encounters must be
	// identical regardless of ordering.
	if len(got.Patients) != len(base.Patients) {
		t.Fatalf("patient count changed with order: %d vs %d", len(got.Patients), len(base.Patients))
	}
	for i := range base.Patients {
		if base.Patients[i].ID != got.Patients[i].ID {
			t.Fatalf("patient ordering/content differs: %s vs %s",
				base.Patients[i].ID, got.Patients[i].ID)
		}
	}
}

func reverse[T any](s []T) {
	for i, j := 0, len(s)-1; i < j; i, j = i+1, j-1 {
		s[i], s[j] = s[j], s[i]
	}
}

func TestStoreIdempotentAndConcurrencySafe(t *testing.T) {
	inputs, catalog, registry, priorities, _ := loadFixture(t)
	batch := Batch{ID: "BATCH-STORE-1", DiagVersion: "diag-v1"}
	p := New(catalog, registry, priorities, nil)
	rep := p.RunConcurrent(inputs, batch, 8)

	store := NewStore()
	first := store.Apply(rep)

	// Re-applying the same batch is a no-op replay.
	second := store.Apply(rep)
	third := store.Apply(rep)
	if !first.Equal(&second) || !second.Equal(&third) {
		t.Fatal("store apply is not idempotent")
	}

	// Concurrent applies of the same batch must converge to the same state.
	var wg sync.WaitGroup
	snaps := make([]Report, 12)
	for i := range snaps {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			snaps[i] = NewStore().Apply(rep)
		}(i)
	}
	wg.Wait()
	for i := 1; i < len(snaps); i++ {
		if !snaps[0].Equal(&snaps[i]) {
			t.Fatalf("concurrent store apply %d diverged", i)
		}
	}

	if !snaps[0].Equal(&first) {
		t.Fatal("fresh concurrent store differs from serial store")
	}
	if len(first.Patients) != 3 || len(first.Encounters) != 7 {
		t.Fatalf("unexpected store state: patients=%d encounters=%d",
			len(first.Patients), len(first.Encounters))
	}
}

func TestUnknownAndDeprecatedCodingsSurviveInReport(t *testing.T) {
	inputs, catalog, registry, priorities, _ := loadFixture(t)
	p := New(catalog, registry, priorities, testLogger{t})

	v1 := p.RunSerial(inputs, Batch{ID: "BATCH-V1", DiagVersion: "diag-v1"})
	v2 := p.RunSerial(inputs, Batch{ID: "BATCH-V2", DiagVersion: "diag-v2"})

	findCodings := func(rep Report, code string) map[string]bool {
		st := map[string]bool{}
		for _, e := range rep.Encounters {
			for _, d := range e.Diagnoses {
				if d.Coding.Code == code {
					st[string(d.Coding.Status)] = true
				}
			}
		}
		return st
	}
	has := func(m map[string]bool, want string) bool { return m[want] }

	// MSG-0006 (Z99.99) declares no version of its own and inherits the
	// batch baseline: unknown stays unknown regardless of baseline version.
	z1 := findCodings(v1, "Z99.99")
	if !has(z1, "unknown") {
		t.Fatalf("unknown code not marked in v1 batch: %v", z1)
	}
	z2 := findCodings(v2, "Z99.99")
	if !has(z2, "unknown") {
		t.Fatalf("unknown code must stay unknown under v2 baseline: %v", z2)
	}
	// MSG-0001 explicit v1 and MSG-0002 explicit v2 coexist: each keeps its
	// own historically pinned conclusion even within the same batch.
	i1 := findCodings(v1, "I21.9")
	if !has(i1, "active") {
		t.Fatalf("I21.9 must include active (explicit v1 records): %v", i1)
	}
	i2 := findCodings(v2, "I21.9")
	if !has(i2, "active") || !has(i2, "deprecated") {
		t.Fatalf("v2 batch must keep v1 active records AND mark v2 deprecated: %v", i2)
	}
	if lc := findCodings(v2, "LC-OLD"); !has(lc, "deprecated") {
		t.Fatalf("explicit-v2 LC-OLD must be deprecated: %v", lc)
	}
	// Re-running the same batch after v2 is loaded reproduces byte-identical
	// conclusions: per-message pinned versions never drift.
	v1again := p.RunSerial(inputs, Batch{ID: "BATCH-V1", DiagVersion: "diag-v1"})
	if !v1.Equal(&v1again) {
		t.Fatal("v1 batch result changed after v2 table was loaded")
	}
	v2again := p.RunSerial(inputs, Batch{ID: "BATCH-V2", DiagVersion: "diag-v2"})
	if !v2.Equal(&v2again) {
		t.Fatal("v2 batch result is not reproducible")
	}
}
