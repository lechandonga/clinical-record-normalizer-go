package normalize

import (
	"fmt"
	"testing"
	"time"

	"github.com/lechandonga/clinical-record-normalizer-go/internal/domain"
	"github.com/lechandonga/clinical-record-normalizer-go/internal/ingest"
	"github.com/lechandonga/clinical-record-normalizer-go/internal/terminology"
)

func testRegistry(t *testing.T) *terminology.Registry {
	t.Helper()
	r := terminology.NewRegistry()
	if err := r.Load(terminology.Table{
		System: "diag", Version: "diag-v1",
		Entries: []terminology.TableEntry{
			{Code: "I10", Status: domain.MappingActive, ConceptSys: "DIAG", Concept: "HTN", Display: "高血压"},
			{Code: "OLD", Status: domain.MappingActive, ConceptSys: "DIAG", Concept: "X", Display: "旧"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.Load(terminology.Table{
		System: "diag", Version: "diag-v2",
		Entries: []terminology.TableEntry{
			{Code: "OLD", Status: domain.MappingDeprecated, ConceptSys: "DIAG", Concept: "X", ReplacedBy: "NEW"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	return r
}

func profiles() map[string]ingest.Profile {
	return map[string]ingest.Profile{
		"with-tz":    {ID: "with-tz", TimeZone: "Asia/Shanghai", TimeZoneSet: true},
		"without-tz": {ID: "without-tz"},
	}
}

// trace logs the raw input and the normalization outcome, as required.
func trace(t *testing.T, raw string, c *Candidate, rj *Rejection) {
	t.Helper()
	t.Logf("raw input   : %s", raw)
	if rj != nil {
		t.Logf("normalized  : REJECTED reason=%s field=%s detail=%s", rj.Reason, rj.Field, rj.Detail)
	} else {
		t.Logf("normalized  : ACCEPTED local=%s sex=%s birth=%s enc=%s diagnoses=%d",
			c.LocalID, c.Sex.Value, c.BirthDate.Value, c.Encounter.ID, len(c.Encounter.Diagnoses))
	}
}

func norm(t *testing.T, reg *terminology.Registry, raw string) (*Candidate, *Rejection) {
	t.Helper()
	env, perr := ingest.Parse([]byte(raw))
	if perr != nil {
		rj := &Rejection{Reason: RejectReason(perr.Reason), Field: perr.Field, Detail: perr.Detail, Raw: raw}
		trace(t, raw, nil, rj)
		return nil, rj
	}
	c, rj := New(reg).Normalize(env, profiles()[env.SourceID], "", []byte(raw))
	trace(t, raw, c, rj)
	return c, rj
}

const validMsg = `{
  "message_id":"M1","source_id":"with-tz","sent_at":"2026-03-01T08:00:00+08:00",
  "version":"diag-v1",
  "payload":{"patient_mrn":"MRN1","id_card":"110101199001011234","sex":"男",
  "birth_date":"1990-01-01","visit_id":"V1","visit_type":"住院",
  "start":"2026-03-01T08:00:00+08:00","diagnoses":["I10"]}
}`

func TestNormalizeHappyPath(t *testing.T) {
	reg := testRegistry(t)
	c, rj := norm(t, reg, validMsg)
	if rj != nil {
		t.Fatalf("unexpected rejection: %+v", rj)
	}
	if c.Sex.Value != "M" || c.BirthDate.Value != "1990-01-01" {
		t.Fatalf("demographics wrong: %+v", c)
	}
	if c.Encounter.Type != "inpatient" || c.Encounter.PatientID != "" {
		t.Fatalf("encounter wrong: %+v", c.Encounter)
	}
	if c.Received.Location() != time.UTC {
		t.Fatalf("received should be UTC: %v", c.Received)
	}
	if len(c.Encounter.Diagnoses) != 1 ||
		c.Encounter.Diagnoses[0].Coding.TableVersion != "diag-v1" ||
		c.Encounter.Diagnoses[0].Coding.Status != domain.MappingActive {
		t.Fatalf("diagnosis wrong: %+v", c.Encounter.Diagnoses)
	}
}

func TestRejectionReasonsAreDistinguishable(t *testing.T) {
	reg := testRegistry(t)
	cases := []struct {
		name   string
		raw    string
		reason RejectReason
		field  string
	}{
		{
			"malformed_json",
			`{"message_id":"M","source_id":"with-tz", broken`,
			RejectMalformedJSON, "",
		},
		{
			"missing_message_id",
			`{"source_id":"with-tz","sent_at":"2026-03-01T08:00:00+08:00","payload":{"a":1}}`,
			RejectMissingField, "message_id",
		},
		{
			"missing_sex",
			`{"message_id":"M","source_id":"with-tz","sent_at":"2026-03-01T08:00:00+08:00",
			"payload":{"patient_mrn":"X","birth_date":"1990-01-01","visit_id":"V","visit_type":"OP","start":"2026-03-01T08:00:00+08:00"}}`,
			RejectMissingField, "sex",
		},
		{
			"invalid_sex",
			`{"message_id":"M","source_id":"with-tz","sent_at":"2026-03-01T08:00:00+08:00",
			"payload":{"patient_mrn":"X","sex":"X","birth_date":"1990-01-01","visit_id":"V","visit_type":"OP","start":"2026-03-01T08:00:00+08:00"}}`,
			RejectInvalidSex, "sex",
		},
		{
			"invalid_birth_date",
			`{"message_id":"M","source_id":"with-tz","sent_at":"2026-03-01T08:00:00+08:00",
			"payload":{"patient_mrn":"X","sex":"M","birth_date":"01/01/1990","visit_id":"V","visit_type":"OP","start":"2026-03-01T08:00:00+08:00"}}`,
			RejectInvalidBirth, "birth_date",
		},
		{
			"future_birth_date",
			`{"message_id":"M","source_id":"with-tz","sent_at":"2026-03-01T08:00:00+08:00",
			"payload":{"patient_mrn":"X","sex":"M","birth_date":"2099-01-01","visit_id":"V","visit_type":"OP","start":"2026-03-01T08:00:00+08:00"}}`,
			RejectFutureBirth, "birth_date",
		},
		{
			"ambiguous_wall_clock_without_source_tz",
			`{"message_id":"M","source_id":"without-tz","sent_at":"2026-03-01 08:00:00",
			"payload":{"patient_mrn":"X","sex":"M","birth_date":"1990-01-01","visit_id":"V","visit_type":"OP","start":"2026-03-01 08:00:00"}}`,
			RejectAmbiguousTime, "sent_at",
		},
		{
			"ambiguous_date_only",
			`{"message_id":"M","source_id":"with-tz","sent_at":"2026-03-01",
			"payload":{"patient_mrn":"X","sex":"M","birth_date":"1990-01-01","visit_id":"V","visit_type":"OP","start":"2026-03-01T08:00:00+08:00"}}`,
			RejectAmbiguousTime, "sent_at",
		},
		{
			"invalid_time",
			`{"message_id":"M","source_id":"with-tz","sent_at":"not-a-time",
			"payload":{"patient_mrn":"X","sex":"M","birth_date":"1990-01-01","visit_id":"V","visit_type":"OP","start":"2026-03-01T08:00:00+08:00"}}`,
			RejectInvalidTime, "sent_at",
		},
		{
			"illegal_code_shape",
			`{"message_id":"M","source_id":"with-tz","sent_at":"2026-03-01T08:00:00+08:00",
			"version":"diag-v1",
			"payload":{"patient_mrn":"X","sex":"M","birth_date":"1990-01-01","visit_id":"V","visit_type":"OP","start":"2026-03-01T08:00:00+08:00","diagnoses":["bad code!"]}}`,
			RejectInvalidCode, "diagnoses",
		},
		{
			"unknown_table_version",
			`{"message_id":"M","source_id":"with-tz","sent_at":"2026-03-01T08:00:00+08:00",
			"version":"diag-v9",
			"payload":{"patient_mrn":"X","sex":"M","birth_date":"1990-01-01","visit_id":"V","visit_type":"OP","start":"2026-03-01T08:00:00+08:00","diagnoses":["I10"]}}`,
			RejectInvalidCode, "diag_version",
		},
		{
			"bad_id_card",
			`{"message_id":"M","source_id":"with-tz","sent_at":"2026-03-01T08:00:00+08:00",
			"payload":{"patient_mrn":"X","id_card":"123","sex":"M","birth_date":"1990-01-01","visit_id":"V","visit_type":"OP","start":"2026-03-01T08:00:00+08:00"}}`,
			RejectInvalidCode, "id_card",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, rj := norm(t, reg, tc.raw)
			if rj == nil {
				t.Fatal("expected rejection")
			}
			if rj.Reason != tc.reason {
				t.Fatalf("reason: got %q want %q", rj.Reason, tc.reason)
			}
			if tc.field != "" && rj.Field != tc.field {
				t.Fatalf("field: got %q want %q", rj.Field, tc.field)
			}
			if rj.Raw != tc.raw {
				t.Fatal("rejection must retain the original raw input")
			}
		})
	}
}

func TestUnknownCodeIsMarkedNotRejected(t *testing.T) {
	reg := testRegistry(t)
	raw := `{
  "message_id":"M","source_id":"with-tz","sent_at":"2026-03-01T08:00:00+08:00",
  "version":"diag-v1",
  "payload":{"patient_mrn":"X","sex":"M","birth_date":"1990-01-01",
  "visit_id":"V","visit_type":"OP","start":"2026-03-01T08:00:00+08:00",
  "diagnoses":["ZZZ.1"]}}`
	c, rj := norm(t, reg, raw)
	if rj != nil {
		t.Fatalf("unknown-but-well-formed code should be kept, got %+v", rj)
	}
	d := c.Encounter.Diagnoses[0]
	if d.Coding.Status != domain.MappingUnknown || d.Concept.Code != "UNKNOWN" {
		t.Fatalf("expected marked unknown: %+v", d)
	}
}

func TestDeprecatedCodeReproducibleByVersion(t *testing.T) {
	reg := testRegistry(t)
	base := `{
  "message_id":"M","source_id":"with-tz","sent_at":"2026-03-01T08:00:00+08:00",
  "version":"%s",
  "payload":{"patient_mrn":"X","sex":"M","birth_date":"1990-01-01",
  "visit_id":"V","visit_type":"OP","start":"2026-03-01T08:00:00+08:00",
  "diagnoses":["OLD"]}}`
	c1, rj1 := norm(t, reg, fmt.Sprintf(base, "diag-v1"))
	c2, rj2 := norm(t, reg, fmt.Sprintf(base, "diag-v2"))
	if rj1 != nil || rj2 != nil {
		t.Fatalf("unexpected rejections: %v %v", rj1, rj2)
	}
	if c1.Encounter.Diagnoses[0].Coding.Status != domain.MappingActive {
		t.Fatal("v1 must keep OLD active")
	}
	if c2.Encounter.Diagnoses[0].Coding.Status != domain.MappingDeprecated ||
		c2.Encounter.Diagnoses[0].Coding.ReplacedBy != "NEW" {
		t.Fatalf("v2 must mark OLD deprecated: %+v", c2.Encounter.Diagnoses[0])
	}
}
