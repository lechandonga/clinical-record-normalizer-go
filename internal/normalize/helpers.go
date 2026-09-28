package normalize

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lechandonga/clinical-record-normalizer-go/internal/domain"
	"github.com/lechandonga/clinical-record-normalizer-go/internal/ingest"
)

// errFutureBirth marks a structurally valid but impossible birth date.
var errFutureBirth = errors.New("birth date is in the future")

// mapSex normalizes source-specific sex tokens to M/F/U.
func mapSex(raw string) (domain.Sex, bool) {
	switch strings.ToUpper(strings.TrimSpace(raw)) {
	case "M", "MALE", "男", "1":
		return domain.SexMale, true
	case "F", "FEMALE", "女", "2":
		return domain.SexFemale, true
	case "U", "UNKNOWN", "未知", "0", "":
		return domain.SexUnknown, true
	}
	return "", false
}

// mapVisitType normalizes visit-type tokens to a closed vocabulary.
func mapVisitType(raw string) (string, bool) {
	switch strings.ToUpper(strings.TrimSpace(raw)) {
	case "IP", "INPATIENT", "住院":
		return "inpatient", true
	case "OP", "OUTPATIENT", "门诊":
		return "outpatient", true
	case "ER", "EMERGENCY", "急诊":
		return "emergency", true
	}
	return "", false
}

// parseBirthDate accepts strict YYYY-MM-DD and rejects impossible/future dates.
func parseBirthDate(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if !birthDateRE.MatchString(raw) {
		return time.Time{}, fmt.Errorf("birth date must be YYYY-MM-DD, got %q", raw)
	}
	t, err := time.Parse("2006-01-02", raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid calendar date %q", raw)
	}
	if t.After(time.Now().UTC()) {
		return time.Time{}, errFutureBirth
	}
	return t.UTC(), nil
}

// parseSentAt parses the envelope timestamp. Offsets are honored as-is; a
// bare wall clock is only unambiguous when the source profile declares a
// time zone, otherwise the record is rejected with ambiguous_time.
func parseSentAt(raw string, profile ingest.Profile,
	ref domain.SourceRef, rawMsg []byte) (time.Time, *Rejection) {
	return parseTimestamp(raw, profile, "sent_at", ref, rawMsg)
}

// parseEventTime parses encounter start/end times with the same rules.
func parseEventTime(raw string, profile ingest.Profile, field string,
	ref domain.SourceRef, rawMsg []byte) (time.Time, *Rejection) {
	return parseTimestamp(raw, profile, field, ref, rawMsg)
}

func parseTimestamp(raw string, profile ingest.Profile, field string,
	ref domain.SourceRef, rawMsg []byte) (time.Time, *Rejection) {
	raw = strings.TrimSpace(raw)

	// RFC3339 with explicit offset or trailing Z: unambiguous everywhere.
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t.UTC(), nil
	}
	// "2006-01-02" is a whole day, not an instant: ambiguous by definition.
	if birthDateRE.MatchString(raw) {
		return time.Time{}, reject(RejectAmbiguousTime, ref, field,
			"date without time/offset is ambiguous: "+raw, rawMsg)
	}
	// "2006-01-02 15:04:05" wall clock: only safe with a declared zone.
	if t, err := time.Parse("2006-01-02 15:04:05", raw); err == nil {
		if !profile.TimeZoneSet {
			return time.Time{}, reject(RejectAmbiguousTime, ref, field,
				"wall-clock time without offset and source has no time_zone: "+raw, rawMsg)
		}
		loc, _ := time.LoadLocation(profile.TimeZone)
		return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(),
			t.Second(), 0, loc).UTC(), nil
	}
	return time.Time{}, reject(RejectInvalidTime, ref, field,
		"unparseable timestamp: "+raw, rawMsg)
}

// encounterID is a deterministic content hash so repeated imports reproduce
// identical IDs.
func encounterID(ref domain.SourceRef, visitID string) string {
	sum := sha256.Sum256([]byte(ref.SourceID + "|" + ref.MessageID + "|" + visitID))
	return "ENC-" + hex.EncodeToString(sum[:12])
}
