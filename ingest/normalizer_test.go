package ingest

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/lechandonga/clinical-record-normalizer-go/model"
	"github.com/lechandonga/clinical-record-normalizer-go/terminology"
)

// stubMapper is an in-memory mapper used by unit tests.
type stubMapper struct {
	systems map[string]string // codeset/version -> system
	entries map[string]map[string]terminology.LookupResult
}

func (m *stubMapper) key(name, ver string) string { return name + "/" + ver }

func (m *stubMapper) SystemOf(name, ver string) (string, bool) {
	s, ok := m.systems[m.key(name, ver)]
	return s, ok
}

func (m *stubMapper) Map(name, ver, code string) (terminology.LookupResult, bool) {
	row, ok := m.entries[m.key(name, ver)]
	if !ok {
		return terminology.LookupResult{}, false
	}
	r, ok := row[code]
	if !ok {
		return terminology.LookupResult{Status: terminology.StatusUnknown}, true
	}
	return r, true
}

func testMapper() *stubMapper {
	return &stubMapper{
		systems: map[string]string{
			"diagnosis/v1":      "ICD-10",
			"encounter-type/v1": "LOCAL-ENC",
		},
		entries: map[string]map[string]terminology.LookupResult{
			"diagnosis/v1": {
				"I10": {Status: terminology.StatusActive, Display: "HTN"},
				"Z00": {Status: terminology.StatusDeprecated, Display: "old exam", CanonicalCode: "Z00.00"},
			},
			"encounter-type/v1": {
				"OUTPAT": {Status: terminology.StatusActive, Display: "outpatient"},
			},
		},
	}
}

func fixedClock() time.Time { return time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC) }

const validBody = `{
  "source":"hospitalA","recordId":"A-1",
  "receivedAt":"2026-09-01T08:30:00Z",
  "patient":{
    "identifiers":[{"system":"MRN","value":"M-1"}],
    "name":"Zhang","gender":"male","birthDate":"1980-05-12",
    "assumedAt":"2026-08-01T00:00:00Z"
  },
  "encounter":{
    "start":"2026-09-01T08:00:00Z","end":"2026-09-01T08:45:00Z",
    "typeSystem":"LOCAL-ENC","typeCode":"OUTPAT",
    "diagnoses":[
      {"system":"ICD-10","code":"I10","rank":1},
      {"system":"ICD-10","code":"R51","rank":2},
      {"system":"ICD-10","code":"Z00","rank":3}
    ]
  }
}`

// logPair prints the original input and the normalized result for every
// test case, as required for auditability in unit-test logs.
func logPair(t *testing.T, raw string, result any) {
	t.Helper()
	t.Logf("RAW INPUT: %s", raw)
	buf, _ := json.MarshalIndent(result, "", "  ")
	t.Logf("NORMALIZED:\n%s", buf)
}

func TestNormalizeHappyPathMarksMappingStatus(t *testing.T) {
	n := NewNormalizer(testMapper(),
		map[string]string{CodesetDiagnosis: "v1", CodesetEncounterType: "v1"},
		fixedClock)
	out, rej := n.Normalize(model.RawMessage{Source: "hospitalA", RecordID: "A-1", Format: "json", Body: []byte(validBody)})
	if rej != nil {
		t.Fatalf("unexpected rejection: %+v", rej)
	}
	defer logPair(t, validBody, out.Partial)

	if out.Partial.DedupKey != "hospitalA/A-1" {
		t.Fatalf("bad dedup key: %s", out.Partial.DedupKey)
	}
	dxs := out.Partial.Encounter.Diagnoses
	if dxs[0].Concept.Status != model.MapActive {
		t.Fatalf("I10 should be active, got %s", dxs[0].Concept.Status)
	}
	if dxs[1].Concept.Status != model.MapUnknown {
		t.Fatalf("R51 should be marked unknown, not dropped/rejected, got %s", dxs[1].Concept.Status)
	}
	if dxs[2].Concept.Status != model.MapDeprecated || dxs[2].Concept.CanonicalCode != "Z00.00" {
		t.Fatalf("Z00 should be deprecated with replacement, got %+v", dxs[2].Concept)
	}
	if out.Partial.CodesetVer != "diagnosis:v1/encounter-type:v1" {
		t.Fatalf("unexpected pinned version token: %s", out.Partial.CodesetVer)
	}
}

func TestRejectionReasonsAreDistinct(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		reason model.RejectReason
	}{
		{"unreadable", `{not json`, model.RejectUnreadable},
		{"missing recordId", `{"patient":{"identifiers":[{"system":"MRN","value":"x"}]}}`, model.RejectMissingField},
		{"missing identifiers", `{"recordId":"r","patient":{}}`, model.RejectMissingField},
		{"missing birthDate", patch(validBody, `"birthDate":"1980-05-12",`, ``), model.RejectMissingField},
		{"malformed birthDate", patch(validBody, `"1980-05-12"`, `"1980/05/12"`), model.RejectInvalidBirthDate},
		{"future birthDate", patch(validBody, `"1980-05-12"`, `"2999-01-01"`), model.RejectInvalidBirthDate},
		{"bad gender", patch(validBody, `"gender":"male"`, `"gender":"helicopter"`), model.RejectInvalidGender},
		{"day precision encounter", patch(validBody, `"start":"2026-09-01T08:00:00Z"`, `"start":"2026-09-01"`), model.RejectAmbiguousTime},
		{"future encounter", patch(validBody, `"end":"2026-09-01T08:45:00Z"`, `"end":"2026-09-30T08:45:00Z"`), model.RejectAmbiguousTime},
		{"end before start", patch(validBody, `"end":"2026-09-01T08:45:00Z"`, `"end":"2026-08-31T08:45:00Z"`), model.RejectInvalidTime},
		{"malformed code syntax", patch(validBody, `"code":"I10","rank":1`, `"code":"BAD!!","rank":1`), model.RejectInvalidCode},
		{"wrong coding system", patch(validBody, `"typeSystem":"LOCAL-ENC"`, `"typeSystem":"SNOMED"`), model.RejectInvalidCode},
		{"missing diagnosis code", patch(validBody, `{"system":"ICD-10","code":"I10","rank":1}`, `{"system":"ICD-10","rank":1}`), model.RejectMissingField},
	}
	n := NewNormalizer(testMapper(),
		map[string]string{CodesetDiagnosis: "v1", CodesetEncounterType: "v1"},
		fixedClock)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, rej := n.Normalize(model.RawMessage{Source: "s", RecordID: "r", Format: "json", Body: []byte(tc.body)})
			if rej == nil {
				logPair(t, tc.body, out.Partial)
				t.Fatalf("expected rejection %s, got success", tc.reason)
			}
			logPair(t, tc.body, rej)
			if rej.Reason != tc.reason {
				t.Fatalf("reason = %s, want %s (detail: %s)", rej.Reason, tc.reason, rej.Detail)
			}
			if rej.RawInput == "" {
				t.Fatal("rejection must retain the raw input")
			}
		})
	}
}

func TestNormalizeIsDeterministic(t *testing.T) {
	n := NewNormalizer(testMapper(),
		map[string]string{CodesetDiagnosis: "v1", CodesetEncounterType: "v1"},
		fixedClock)
	var first string
	for i := 0; i < 3; i++ {
		out, rej := n.Normalize(model.RawMessage{Source: "hospitalA", RecordID: "A-1", Format: "json", Body: []byte(validBody)})
		if rej != nil {
			t.Fatal(rej)
		}
		buf, _ := json.Marshal(out.Partial)
		if i == 0 {
			first = string(buf)
		} else if string(buf) != first {
			t.Fatal("normalization is not deterministic")
		}
	}
	fmt.Println("deterministic normalization x3")
}
