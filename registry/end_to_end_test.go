package registry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/lechandonga/clinical-record-normalizer-go/model"
	"github.com/lechandonga/clinical-record-normalizer-go/terminology"
)

// loadSampleMessages reads ../../samples/messages exactly like the CLI does.
func loadSampleMessages(t *testing.T, dir string) []model.RawMessage {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(files)
	var raws []model.RawMessage
	for _, p := range files {
		body, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		source := strings.TrimSuffix(filepath.Base(p), ".json")
		trimmed := strings.TrimSpace(string(body))
		if len(trimmed) > 0 && trimmed[0] == '[' {
			var docs []json.RawMessage
			if err := json.Unmarshal(body, &docs); err != nil {
				raws = append(raws, model.RawMessage{Source: source, RecordID: "malformed", Format: "json", Body: body})
				continue
			}
			for _, d := range docs {
				var hdr struct {
					Source   string `json:"source"`
					RecordID string `json:"recordId"`
				}
				_ = json.Unmarshal(d, &hdr)
				src, rid := source, "item"
				if hdr.Source != "" {
					src = hdr.Source
				}
				if hdr.RecordID != "" {
					rid = hdr.RecordID
				}
				raws = append(raws, model.RawMessage{Source: src, RecordID: rid, Format: "json", Body: d})
			}
		} else {
			raws = append(raws, model.RawMessage{Source: source, RecordID: "doc", Format: "json", Body: body})
		}
	}
	return raws
}

func runSample(t *testing.T, version string) (model.ImportReport, *Registry) {
	t.Helper()
	root := filepath.Join("..", "samples")
	terms := terminology.NewService()
	if err := terms.LoadDir(filepath.Join(root, "codesets")); err != nil {
		t.Fatal(err)
	}
	if err := terms.SetActiveVersion("diagnosis", version); err != nil {
		t.Fatal(err)
	}
	if err := terms.SetActiveVersion("encounter-type", version); err != nil {
		t.Fatal(err)
	}
	clock, _ := time.Parse(time.RFC3339, "2026-09-28T09:00:00Z")
	reg := New(terms).WithClock(func() time.Time { return clock })
	raws := loadSampleMessages(t, filepath.Join(root, "messages"))
	rep, err := reg.ImportBatch("e2e", raws)
	if err != nil {
		t.Fatal(err)
	}
	// Second import of the identical files must change nothing.
	rep2, err := reg.ImportBatch("e2e-repeat", raws)
	if err != nil {
		t.Fatal(err)
	}
	if rep2.Fingerprint != rep.Fingerprint {
		t.Fatalf("repeated import changed fingerprint: %s -> %s", rep.Fingerprint, rep2.Fingerprint)
	}
	return rep, reg
}

func TestEndToEndSamplesV2026(t *testing.T) {
	rep, reg := runSample(t, "v2026")

	want := map[model.RejectReason]int{
		model.RejectUnreadable:       1,
		model.RejectInvalidTime:      1,
		model.RejectAmbiguousTime:    1,
		model.RejectInvalidCode:      2,
		model.RejectIdentityConflict: 1,
	}
	for reason, n := range want {
		if rep.RejectedByReason[reason] != n {
			t.Fatalf("reason %s: got %d want %d (all=%v)", reason, rep.RejectedByReason[reason], n, rep.RejectedByReason)
		}
	}
	// Every rejection must carry the original payload.
	for _, rj := range rep.Rejections {
		t.Logf("[v2026] REJECT %s/%s reason=%s field=%s detail=%s",
			rj.Source.Source, rj.Source.RecordID, rj.Reason, rj.Field, rj.Detail)
		if rj.RawInput == "" {
			t.Fatal("rejection must include the raw input")
		}
	}
	if rep.AcceptedCount != 5 || rep.PatientCount != 2 || rep.EncounterCount != 4 {
		t.Fatalf("counts wrong: %+v", rep)
	}
	patients := reg.Patients()
	for _, p := range patients {
		buf, _ := json.MarshalIndent(p, "", "  ")
		t.Logf("[v2026] NORMALIZED PATIENT:\n%s", buf)
	}
	var zhang model.Patient
	for _, p := range patients {
		for _, id := range p.Identifiers {
			if id.System == "SSN" && id.Value == "S-111" {
				zhang = p
			}
		}
	}
	if zhang.PatientID == "" {
		t.Fatal("Zhang Wei should exist as a merged cross-source patient")
	}
	if len(zhang.Sources) != 2 || zhang.Sources[0] != "hospitalA" || zhang.Sources[1] != "hospitalB" {
		t.Fatalf("expected sources [hospitalA hospitalB], got %v", zhang.Sources)
	}
	if zhang.Name.Value != "Zhang Wei (B hospital record)" || len(zhang.Conflicts) != 1 {
		t.Fatalf("name conflict not resolved/traced: name=%q conflicts=%d", zhang.Name.Value, len(zhang.Conflicts))
	}
}

func TestEndToEndV2026AndV2027DifferButEachStable(t *testing.T) {
	rep26, _ := runSample(t, "v2026")
	rep27, reg27 := runSample(t, "v2027")
	if rep26.Fingerprint == rep27.Fingerprint {
		t.Fatal("v2026 and v2027 results must differ (deprecated codes change status)")
	}
	// Under v2027: A-1001's E11.9 must be deprecated with replacement;
	// A-1004's J20.9 must be active; it was unknown under v2026.
	var foundActive, foundDeprecated bool
	for _, e := range reg27.Encounters() {
		for _, d := range e.Diagnoses {
			if d.Concept.Code == "J20.9" && d.Concept.Status == model.MapActive {
				foundActive = true
			}
			if d.Concept.Code == "E11.9" && d.Concept.Status == model.MapDeprecated &&
				d.Concept.CanonicalCode == "E11.90" {
				foundDeprecated = true
			}
		}
	}
	if !foundActive || !foundDeprecated {
		t.Fatalf("v2027 mapping expectations failed: active=%v deprecated=%v", foundActive, foundDeprecated)
	}
	// Re-running v2026 after v2027 must reproduce the original result.
	rep26Again, _ := runSample(t, "v2026")
	if rep26Again.Fingerprint != rep26.Fingerprint {
		t.Fatalf("historical v2026 result must stay reproducible: %s vs %s",
			rep26.Fingerprint, rep26Again.Fingerprint)
	}
}
