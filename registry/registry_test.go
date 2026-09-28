package registry

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/lechandonga/clinical-record-normalizer-go/model"
	"github.com/lechandonga/clinical-record-normalizer-go/terminology"
)

func testService(t *testing.T) *terminology.Service {
	t.Helper()
	s := terminology.NewService()
	if err := s.Load(&terminology.Codeset{
		Name: "diagnosis", Version: "v1", System: "ICD-10",
		Entries: map[string]terminology.Entry{
			"I10": {Code: "I10", Display: "HTN"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Load(&terminology.Codeset{
		Name: "encounter-type", Version: "v1", System: "LOCAL-ENC",
		Entries: map[string]terminology.Entry{
			"OUTPAT": {Code: "OUTPAT", Display: "outpatient"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	return s
}

func fixedClock() time.Time { return time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC) }

func msg(source, rid string) model.RawMessage {
	body := fmt.Sprintf(`{
	  "source":%q,"recordId":%q,"receivedAt":"2026-09-01T08:30:00Z",
	  "patient":{
	    "identifiers":[{"system":"SSN","value":"S-1"},{"system":"MRN","value":"%s-%s"}],
	    "name":"Person %s","gender":"male","birthDate":"1980-05-12",
	    "assumedAt":"2026-08-01T00:00:00Z"
	  },
	  "encounter":{
	    "start":"2026-09-01T08:00:00Z","end":"2026-09-01T08:45:00Z",
	    "typeSystem":"LOCAL-ENC","typeCode":"OUTPAT",
	    "diagnoses":[{"system":"ICD-10","code":"I10","rank":1}]
	  }
	}`, source, rid, source, rid, rid)
	return model.RawMessage{Source: source, RecordID: rid, Format: "json", Body: []byte(body)}
}

func sampleBatch() []model.RawMessage {
	return []model.RawMessage{
		msg("hospitalB", "B-2"), msg("hospitalA", "A-1"),
		msg("hospitalA", "A-1"), // exact duplicate inside the batch
		{Source: "hospitalA", RecordID: "BAD", Format: "json", Body: []byte(`{broken`)},
		msg("hospitalB", "B-3"),
	}
}

func serialState(t *testing.T) (string, model.ImportReport) {
	t.Helper()
	r := New(testService(t)).WithClock(fixedClock)
	rep, err := r.ImportBatch("ser", sampleBatch())
	if err != nil {
		t.Fatal(err)
	}
	snap := snapshot(t, r)
	// Importing the same batch a second time must be idempotent.
	rep2, err := r.ImportBatch("ser-again", sampleBatch())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot(t, r) != snap {
		t.Fatal("state changed on re-import")
	}
	if rep2.Fingerprint != rep.Fingerprint {
		t.Fatalf("fingerprint changed on re-import: %s vs %s", rep.Fingerprint, rep2.Fingerprint)
	}
	if rep2.PatientCount != 1 || rep2.EncounterCount != 3 {
		t.Fatalf("re-import should not add state, got patients=%d encounters=%d",
			rep2.PatientCount, rep2.EncounterCount)
	}
	return snap, rep
}

func snapshot(t *testing.T, r *Registry) string {
	t.Helper()
	buf, err := json.Marshal(struct {
		P []model.Patient   `json:"p"`
		E []model.Encounter `json:"e"`
	}{r.Patients(), r.Encounters()})
	if err != nil {
		t.Fatal(err)
	}
	return string(buf)
}

func TestConcurrentImportEqualsSerialAndIsIdempotent(t *testing.T) {
	serialSnap, serialRep := serialState(t)

	const goroutines = 16
	const inner = 4
	var wg sync.WaitGroup
	snaps := make([]string, goroutines)
	fps := make([]string, goroutines*inner)
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		i := i
		go func() {
			defer wg.Done()
			r := New(testService(t)).WithClock(fixedClock)
			var wg2 sync.WaitGroup
			// Each goroutine within one registry hammers the same batch.
			for k := 0; k < inner; k++ {
				wg2.Add(1)
				go func(k int) {
					defer wg2.Done()
					rep, err := r.ImportBatch(fmt.Sprintf("conc-%d-%d", i, k), sampleBatch())
					if err != nil {
						t.Errorf("import error: %v", err)
						return
					}
					fps[i*inner+k] = rep.Fingerprint
				}(k)
			}
			wg2.Wait()
			snaps[i] = snapshot(t, r)
		}()
	}
	wg.Wait()

	for i, s := range snaps {
		if s != serialSnap {
			t.Fatalf("concurrent state differs from serial state in worker %d:\nserial=%s\nconcur=%s",
				i, serialSnap, s)
		}
	}
	for i, f := range fps {
		if f != serialRep.Fingerprint {
			t.Fatalf("concurrent fingerprint %s != serial %s (slot %d)", f, serialRep.Fingerprint, i)
		}
	}
}

func TestRejectionCountersByReason(t *testing.T) {
	r := New(testService(t)).WithClock(fixedClock)
	batch := append(sampleBatch(),
		model.RawMessage{Source: "s", RecordID: "r1", Format: "json", Body: []byte(`{"recordId":"r1"}`)},
		model.RawMessage{Source: "s", RecordID: "r2", Format: "json", Body: []byte(`{`)},
	)
	rep, err := r.ImportBatch("counts", batch)
	if err != nil {
		t.Fatal(err)
	}
	for _, rej := range rep.Rejections {
		buf, _ := json.Marshal(rej)
		t.Logf("REJECTION reason=%s field=%s\n  raw=%s\n  record=%s",
			rej.Reason, rej.Field, rej.RawInput, buf)
	}
	if rep.RejectedByReason[model.RejectUnreadable] != 2 {
		t.Fatalf("expected 2 unreadable, got %d", rep.RejectedByReason[model.RejectUnreadable])
	}
	if rep.RejectedByReason[model.RejectMissingField] != 1 {
		t.Fatalf("expected 1 missing field, got %d", rep.RejectedByReason[model.RejectMissingField])
	}
	if len(rep.Rejections) != 3 {
		t.Fatalf("expected 3 rejected records, got %d", len(rep.Rejections))
	}
}

func TestNoCodesetLoadedFailsExplicitly(t *testing.T) {
	r := New(terminology.NewService()).WithClock(fixedClock)
	if _, err := r.ImportBatch("x", sampleBatch()); err == nil {
		t.Fatal("missing codeset versions must return an error, not silently accept")
	}
}
