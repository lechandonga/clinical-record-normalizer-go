// Package normalize turns single source messages into unified-model
// candidates or typed rejections. It performs no cross-message state.
package normalize

import (
	"encoding/json"
	"regexp"
	"strings"
	"time"

	"github.com/lechandonga/clinical-record-normalizer-go/internal/domain"
	"github.com/lechandonga/clinical-record-normalizer-go/internal/ingest"
	"github.com/lechandonga/clinical-record-normalizer-go/internal/terminology"
)

// diagSystem is the only coding system normalized by the local tables.
const diagSystem = "diag"

var (
	birthDateRE = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})$`)
	idCardRE    = regexp.MustCompile(`^[0-9]{17}[0-9Xx]$`)
)

// Candidate is one accepted, normalized message awaiting identity merge.
type Candidate struct {
	Ref       domain.SourceRef
	Received  time.Time
	LocalID   string
	Ident     []domain.Identifier
	Sex       domain.TracedValue
	BirthDate domain.TracedValue
	Encounter *domain.Encounter
}

// Normalizer validates and maps one message at a time.
type Normalizer struct {
	registry *terminology.Registry
}

// New constructs a Normalizer bound to the loaded terminology registry.
func New(registry *terminology.Registry) *Normalizer {
	return &Normalizer{registry: registry}
}

func reject(reason RejectReason, ref domain.SourceRef, field, detail string, raw []byte) *Rejection {
	r := &Rejection{
		Reason: reason,
		Field:  field,
		Detail: detail,
		Source: &ref,
	}
	// Keep the original input for traceability in logs/reports.
	r.Raw = string(raw)
	return r
}

// Normalize validates and normalizes one parsed envelope. It is a pure
// function: the same input and registry state always yields the same output.
func (n *Normalizer) Normalize(env ingest.Envelope, profile ingest.Profile, defaultDiagVersion string,
	raw []byte) (*Candidate, *Rejection) {
	ref := domain.SourceRef{SourceID: env.SourceID, MessageID: env.MessageID}

	received, rj := parseSentAt(env.SentAt, profile, ref, raw)
	if rj != nil {
		return nil, rj
	}

	var p struct {
		PatientMRN  string   `json:"patient_mrn"`
		PatientID   string   `json:"patient_id"`
		CardID      string   `json:"card_id"`
		IDCard      string   `json:"id_card"`
		IdentityNo  string   `json:"identity_no"`
		Sex         string   `json:"sex"`
		Gender      string   `json:"gender"`
		BirthDate   string   `json:"birth_date"`
		Birthday    string   `json:"birthday"`
		VisitID     string   `json:"visit_id"`
		VisitNo     string   `json:"visit_no"`
		EncounterID string   `json:"encounter_id"`
		VisitType   string   `json:"visit_type"`
		Type        string   `json:"type"`
		Start       string   `json:"start"`
		VisitDate   string   `json:"visit_date"`
		StartAt     string   `json:"start_at"`
		End         string   `json:"end"`
		EndAt       string   `json:"end_at"`
		DiagVersion string   `json:"diag_version"`
		Diagnoses   []string `json:"diagnoses"`
		DiagCodes   []string `json:"diag_codes"`
		Codes       []string `json:"codes"`
	}
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		return nil, reject(RejectMalformedJSON, ref, "payload", err.Error(), raw)
	}

	// --- identifiers -------------------------------------------------------
	localID, localField := firstNonEmpty(
		kv{"patient_mrn", p.PatientMRN},
		kv{"patient_id", p.PatientID},
		kv{"card_id", p.CardID},
	)
	if localID == "" {
		return nil, reject(RejectMissingField, ref, localField, "local patient identifier is required", raw)
	}
	idents := []domain.Identifier{{
		System: "MRN|" + env.SourceID,
		Value:  localID,
		Source: ref,
	}}

	idCard, idField := firstNonEmpty(kv{"id_card", p.IDCard}, kv{"identity_no", p.IdentityNo})
	if idCard != "" {
		if !idCardRE.MatchString(idCard) {
			return nil, reject(RejectInvalidCode, ref, idField,
				"id_card must be 17 digits followed by a digit or X", raw)
		}
		idents = append(idents, domain.Identifier{
			System: "ID_CARD",
			Value:  strings.ToUpper(idCard),
			Source: ref,
		})
	}

	// --- sex ---------------------------------------------------------------
	sexRaw, sexField := firstNonEmpty(kv{"sex", p.Sex}, kv{"gender", p.Gender})
	if sexRaw == "" {
		return nil, reject(RejectMissingField, ref, "sex", "sex is required", raw)
	}
	sexCode, ok := mapSex(sexRaw)
	if !ok {
		return nil, reject(RejectInvalidSex, ref, sexField,
			"unsupported sex value: "+sexRaw, raw)
	}
	sexTV := domain.TracedValue{
		Value: string(sexCode), Source: ref, Received: received,
	}

	// --- birth date --------------------------------------------------------
	birthRaw, birthField := firstNonEmpty(kv{"birth_date", p.BirthDate}, kv{"birthday", p.Birthday})
	if birthRaw == "" {
		return nil, reject(RejectMissingField, ref, "birth_date", "birth date is required", raw)
	}
	birth, err := parseBirthDate(birthRaw)
	if err != nil {
		reason := RejectInvalidBirth
		if err == errFutureBirth {
			reason = RejectFutureBirth
		}
		return nil, reject(reason, ref, birthField, err.Error(), raw)
	}
	birthTV := domain.TracedValue{
		Value: birth.Format("2006-01-02"), Source: ref, Received: received,
	}

	// --- encounter ---------------------------------------------------------
	visitID, _ := firstNonEmpty(kv{"visit_id", p.VisitID}, kv{"visit_no", p.VisitNo},
		kv{"encounter_id", p.EncounterID})
	if visitID == "" {
		return nil, reject(RejectMissingField, ref, "visit_id", "encounter identifier is required", raw)
	}
	typeRaw, tf := firstNonEmpty(kv{"visit_type", p.VisitType}, kv{"type", p.Type})
	if typeRaw == "" {
		return nil, reject(RejectMissingField, ref, "visit_type", "visit type is required", raw)
	}
	visitType, ok := mapVisitType(typeRaw)
	if !ok {
		return nil, reject(RejectInvalidCode, ref, tf, "unsupported visit type: "+typeRaw, raw)
	}
	startRaw, sf := firstNonEmpty(kv{"start", p.Start}, kv{"visit_date", p.VisitDate},
		kv{"start_at", p.StartAt})
	if startRaw == "" {
		return nil, reject(RejectMissingField, ref, "start", "encounter start is required", raw)
	}
	start, rj := parseEventTime(startRaw, profile, sf, ref, raw)
	if rj != nil {
		return nil, rj
	}
	var end *time.Time
	endRaw, ef := firstNonEmpty(kv{"end", p.End}, kv{"end_at", p.EndAt})
	if endRaw != "" {
		t, rj := parseEventTime(endRaw, profile, ef, ref, raw)
		if rj != nil {
			return nil, rj
		}
		if t.Before(start) {
			return nil, reject(RejectInvalidTime, ref, ef,
				"encounter end is before start", raw)
		}
		end = &t
	}

	// --- diagnoses ---------------------------------------------------------
	diagRaw, _ := mergeNonEmpty(p.Diagnoses, p.DiagCodes, p.Codes)
	// Explicit per-message version wins (historical records keep the version
	// they arrived with); otherwise the batch provides the pinned default.
	diagVersion := env.Version
	if p.DiagVersion != "" {
		diagVersion = p.DiagVersion
	}
	if diagVersion == "" {
		diagVersion = defaultDiagVersion
	}
	diagnoses := make([]domain.Diagnosis, 0, len(diagRaw))
	seenCode := map[string]bool{}
	for _, code := range diagRaw {
		code = strings.TrimSpace(code)
		if code == "" || seenCode[code] {
			continue
		}
		seenCode[code] = true
		if !terminology.IsWellFormedCode(code) {
			return nil, reject(RejectInvalidCode, ref, "diagnoses",
				"illegal code shape: "+code, raw)
		}
		out, ok := n.registry.Map(diagSystem, code, diagVersion)
		if !ok {
			return nil, reject(RejectInvalidCode, ref, "diag_version",
				"unknown diag table version: "+diagVersion, raw)
		}
		if out.Concept == nil {
			// Unknown-in-pinned-version codes are KEPT but explicitly marked,
			// never silently dropped.
			out.Concept = &domain.Concept{
				System:  "diag",
				Code:    "UNKNOWN",
				Display: "未映射编码: " + code,
			}
		}
		diagnoses = append(diagnoses, domain.Diagnosis{
			Concept: *out.Concept,
			Coding:  out.Coding,
			Source:  ref,
		})
	}

	enc := &domain.Encounter{
		ID:        encounterID(ref, visitID),
		PatientID: "", // assigned by identity resolution
		Type:      visitType,
		Start:     start,
		End:       end,
		Diagnoses: diagnoses,
		Sources:   []domain.SourceRef{ref},
	}

	return &Candidate{
		Ref:       ref,
		Received:  received,
		LocalID:   localID,
		Ident:     idents,
		Sex:       sexTV,
		BirthDate: birthTV,
		Encounter: enc,
	}, nil
}

type kv struct {
	Field string
	Value string
}

func firstNonEmpty(pairs ...kv) (string, string) {
	for _, p := range pairs {
		if strings.TrimSpace(p.Value) != "" {
			return strings.TrimSpace(p.Value), p.Field
		}
	}
	return "", pairs[len(pairs)-1].Field
}

// firstNonEmptyField returns a rejection placeholder (Detail non-empty) when
// every candidate field is absent.
func firstNonEmptyField(pairs ...kv) (string, *kv) {
	v, _ := firstNonEmpty(pairs...)
	if v == "" {
		return "", &pairs[0]
	}
	return v, nil
}

func mergeNonEmpty(groups ...[]string) ([]string, string) {
	var out []string
	seen := map[string]bool{}
	for _, g := range groups {
		for _, v := range g {
			v = strings.TrimSpace(v)
			if v != "" && !seen[v] {
				seen[v] = true
				out = append(out, v)
			}
		}
	}
	return out, ""
}
