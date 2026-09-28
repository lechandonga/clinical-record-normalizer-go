package ingest

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/lechandonga/clinical-record-normalizer-go/model"
	"github.com/lechandonga/clinical-record-normalizer-go/terminology"
)

// Codeset names used by the normalizer.
const (
	CodesetDiagnosis     = "diagnosis"
	CodesetEncounterType = "encounter-type"
)

// Mapper is the subset of the terminology service the normalizer needs.
type Mapper interface {
	Map(name, version, code string) (terminology.LookupResult, bool)
	SystemOf(name, version string) (string, bool)
}

// Normalizer parses and normalizes raw messages against codeset versions
// pinned at batch start.
type Normalizer struct {
	terms    Mapper
	versions map[string]string // codeset name -> pinned version
	now      func() time.Time
}

// NewNormalizer builds a Normalizer pinned to the given codeset versions.
func NewNormalizer(terms Mapper, versions map[string]string, now func() time.Time) *Normalizer {
	if now == nil {
		now = time.Now
	}
	pinned := make(map[string]string, len(versions))
	for k, v := range versions {
		pinned[k] = v
	}
	return &Normalizer{terms: terms, versions: pinned, now: now}
}

// PinnedVersion returns a deterministic token naming the pinned versions,
// e.g. "diagnosis:v2026/encounter-type:v2026".
func (n *Normalizer) PinnedVersion() string {
	keys := make([]string, 0, len(n.versions))
	for k := range n.versions {
		keys = append(keys, k)
	}
	// sort keys for a stable token
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j-1] > keys[j]; j-- {
			keys[j-1], keys[j] = keys[j], keys[j-1]
		}
	}
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+":"+n.versions[k])
	}
	return strings.Join(parts, "/")
}

// Normalized is the pre-identity-merge output for one source record.
type Normalized struct {
	Partial model.Record
}

var (
	// ICD-10 style: letter, two alphanumerics, optional .chunk
	reICD10 = regexp.MustCompile(`^[A-Z][0-9][0-9A-Z](\.[0-9A-Z]{1,4})?$`)
	reLocal = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,31}$`)
)

// validCodeSyntax checks that a code is well-formed for its system.
// A syntactically illegal code is rejected (invalid_code); a well-formed
// code that is simply absent from the codeset is mapped as "unknown" and
// retained.
func validCodeSyntax(system, code string) bool {
	switch system {
	case "ICD-10":
		return reICD10.MatchString(code)
	case "LOCAL-DX", "LOCAL-ENC":
		return reLocal.MatchString(code)
	default:
		return false
	}
}

func normGender(raw string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "male", "m", "1":
		return "male", true
	case "female", "f", "2":
		return "female", true
	case "other", "o", "3":
		return "other", true
	case "unknown", "u", "4", "":
		return "unknown", true
	default:
		return "", false
	}
}

func hashID(prefix, s string) string {
	sum := sha256.Sum256([]byte(s))
	return prefix + hex.EncodeToString(sum[:])[:20]
}

// Normalize parses and normalizes one raw message.
func (n *Normalizer) Normalize(raw model.RawMessage) (*Normalized, *model.Rejection) {
	parsed, rej := Parse(raw)
	if rej != nil {
		return nil, rej
	}
	msg := parsed.Msg
	rawText := string(raw.Body)

	source := strings.TrimSpace(raw.Source)
	if source == "" {
		source = strings.TrimSpace(msg.Source)
	}
	srcRef := model.SourceRef{Source: source, RecordID: strings.TrimSpace(msg.RecordID)}

	receivedAt := parsed.ReceivedAt
	if receivedAt.IsZero() {
		receivedAt = n.now()
	}
	now := n.now()

	// --- demographics ---
	bd, err := parseBirthDate(*msg.Patient.BirthDate)
	if err != nil {
		return nil, rejectField(srcRef, model.RejectInvalidBirthDate,
			"patient.birthDate", err.Error(), rawText)
	}
	if bd.After(now) || bd.Year() < 1900 {
		return nil, rejectField(srcRef, model.RejectInvalidBirthDate,
			"patient.birthDate",
			fmt.Sprintf("birthDate %s is outside the plausible range", bd.Format("2006-01-02")),
			rawText)
	}
	gender, ok := normGender(*msg.Patient.Gender)
	if !ok {
		return nil, rejectField(srcRef, model.RejectInvalidGender,
			"patient.gender", "unrecognized gender: "+*msg.Patient.Gender, rawText)
	}

	var assumedAt time.Time
	if msg.Patient.AssumedAt != nil && strings.TrimSpace(*msg.Patient.AssumedAt) != "" {
		t, _, perr := parseTimeLiteral(*msg.Patient.AssumedAt)
		if perr != nil {
			return nil, rejectField(srcRef, model.RejectInvalidTime,
				"patient.assumedAt", perr.Error(), rawText)
		}
		assumedAt = t
	}
	attr := func(v string) model.AttributedValue {
		return model.AttributedValue{
			Value:      v,
			Source:     source,
			RecordID:   srcRef.RecordID,
			AssumedAt:  assumedAt,
			ReceivedAt: receivedAt,
		}
	}

	// --- encounter times ---
	start, rej := n.parseBoundary(srcRef, "encounter.start", *msg.Encounter.Start, now, rawText)
	if rej != nil {
		return nil, rej
	}
	end, rej := n.parseBoundary(srcRef, "encounter.end", *msg.Encounter.End, now, rawText)
	if rej != nil {
		return nil, rej
	}
	if end.Before(start) {
		return nil, rejectField(srcRef, model.RejectInvalidTime, "encounter",
			fmt.Sprintf("end %s is before start %s", *msg.Encounter.End, *msg.Encounter.Start),
			rawText)
	}

	// --- coded fields ---
	encType, rej := n.mapCode(srcRef, CodesetEncounterType,
		deref(msg.Encounter.TypeSystem), deref(msg.Encounter.TypeCode),
		"encounter.type", rawText)
	if rej != nil {
		return nil, rej
	}
	diagnoses := make([]model.Diagnosis, 0, len(msg.Encounter.Diagnoses))
	for i, d := range msg.Encounter.Diagnoses {
		field := fmt.Sprintf("encounter.diagnoses[%d]", i)
		if d.System == nil || strings.TrimSpace(*d.System) == "" {
			return nil, rejectField(srcRef, model.RejectMissingField, field+".system",
				"diagnosis system is required", rawText)
		}
		if d.Code == nil || strings.TrimSpace(*d.Code) == "" {
			return nil, rejectField(srcRef, model.RejectMissingField, field+".code",
				"diagnosis code is required", rawText)
		}
		concept, rej := n.mapCode(srcRef, CodesetDiagnosis,
			*d.System, *d.Code, field, rawText)
		if rej != nil {
			return nil, rej
		}
		diagnoses = append(diagnoses, model.Diagnosis{Concept: concept, Rank: d.Rank})
	}

	patient := model.Patient{
		Identifiers: convertIDs(msg.Patient.Identifiers),
		Name:        attr(strings.TrimSpace(deref(msg.Patient.Name))),
		Gender:      attr(gender),
		BirthDate:   attr(bd.Format("2006-01-02")),
		Sources:     []string{source},
		UpdatedAt:   receivedAt,
	}
	enc := model.Encounter{
		EncounterID: hashID("enc-", source+"/"+srcRef.RecordID),
		Identifiers: convertIDs(msg.Encounter.Identifiers),
		Start:       start,
		End:         end,
		Type:        encType,
		Diagnoses:   diagnoses,
		Source:      srcRef,
	}
	rec := model.Record{
		DedupKey:   source + "/" + srcRef.RecordID,
		Source:     srcRef,
		CodesetVer: n.PinnedVersion(),
		Patient:    patient,
		Encounter:  enc,
	}
	return &Normalized{Partial: rec}, nil
}

func (n *Normalizer) parseBoundary(src model.SourceRef, field, s string, now time.Time, rawText string) (time.Time, *model.Rejection) {
	t, prec, err := parseTimeLiteral(s)
	if err != nil {
		return time.Time{}, rejectField(src, model.RejectInvalidTime, field, err.Error(), rawText)
	}
	if prec == precisionDay {
		return time.Time{}, rejectField(src, model.RejectAmbiguousTime, field,
			"date without time of day is ambiguous for an encounter boundary", rawText)
	}
	if t.After(now) {
		return time.Time{}, rejectField(src, model.RejectAmbiguousTime, field,
			"timestamp lies in the future; its time semantics are not yet established", rawText)
	}
	return t, nil
}

func (n *Normalizer) mapCode(src model.SourceRef, codeset, sys, code, field, rawText string) (model.Concept, *model.Rejection) {
	version, ok := n.versions[codeset]
	if !ok {
		return model.Concept{}, rejectField(src, model.RejectInvalidCode, field,
			"no pinned version for codeset "+codeset, rawText)
	}
	expectedSys, ok := n.terms.SystemOf(codeset, version)
	if !ok {
		return model.Concept{}, rejectField(src, model.RejectInvalidCode, field,
			fmt.Sprintf("codeset %s/%s is not loaded", codeset, version), rawText)
	}
	if sys != expectedSys {
		return model.Concept{}, rejectField(src, model.RejectInvalidCode, field+".system",
			fmt.Sprintf("coding system %q is not valid for %s (expected %q)",
				sys, codeset, expectedSys), rawText)
	}
	if !validCodeSyntax(sys, code) {
		return model.Concept{}, rejectField(src, model.RejectInvalidCode, field+".code",
			"malformed code "+code+" for system "+sys, rawText)
	}
	res, ok := n.terms.Map(codeset, version, code)
	if !ok {
		return model.Concept{}, rejectField(src, model.RejectInvalidCode, field,
			fmt.Sprintf("codeset %s/%s is not loaded", codeset, version), rawText)
	}
	c := model.Concept{
		System:  sys,
		Code:    code,
		Version: version,
		Codeset: codeset,
	}
	switch res.Status {
	case terminology.StatusActive:
		c.Status = model.MapActive
	case terminology.StatusDeprecated:
		c.Status = model.MapDeprecated
	default:
		c.Status = model.MapUnknown
	}
	c.Display = res.Display
	c.CanonicalCode = res.CanonicalCode
	return c, nil
}

func rejectField(src model.SourceRef, reason model.RejectReason, field, detail, rawText string) *model.Rejection {
	return &model.Rejection{
		Source:   src,
		Reason:   reason,
		Field:    field,
		Detail:   detail,
		RawInput: rawText,
	}
}

func convertIDs(in []MessageIdentifier) []model.Identifier {
	out := make([]model.Identifier, 0, len(in))
	for _, id := range in {
		out = append(out, model.Identifier{
			System: strings.TrimSpace(id.System),
			Value:  strings.TrimSpace(id.Value),
		})
	}
	return out
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return strings.TrimSpace(*p)
}
