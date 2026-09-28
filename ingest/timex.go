package ingest

import (
	"fmt"
	"strings"
	"time"
)

// parseBirthDate requires an explicit day-precision date (YYYY-MM-DD).
// It rejects 2-digit years, month-only or full timestamps so birth dates
// never carry ambiguous time semantics.
func parseBirthDate(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if len(s) != 10 || s[4] != '-' || s[7] != '-' {
		return time.Time{}, fmt.Errorf("birthDate must be YYYY-MM-DD, got %q", s)
	}
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid birthDate %q: %w", s, err)
	}
	return t, nil
}
