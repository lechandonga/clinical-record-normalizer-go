package terminology

import "github.com/lechandonga/clinical-record-normalizer-go/internal/domain"

// Outcome is the result of mapping one source code against a pinned version.
type Outcome struct {
	Coding  domain.Coding
	Concept *domain.Concept
}

// Map maps a source code to a concept using the requested table version.
//
// The version is resolved exactly once at import time and recorded on the
// coding, so later table loads can never change an existing result:
//   - code present and active     -> Status=active,    concept returned
//   - code present but deprecated -> Status=deprecated, concept still returned
//     (the historical meaning stays reproducible) with ReplacedBy populated
//   - code absent from the pinned version -> Status=unknown, concept nil;
//     the raw code is preserved for traceability
func (r *Registry) Map(system, code, requestedVersion string) (Outcome, bool) {
	version, ok := r.ResolveVersion(system, requestedVersion)
	if !ok {
		return Outcome{}, false
	}
	tbl := r.systems[system].versions[version]

	coding := domain.Coding{
		System:       system,
		Code:         code,
		TableVersion: version,
	}

	entry, found := tbl.byCode[code]
	if !found {
		coding.Status = domain.MappingUnknown
		return Outcome{Coding: coding}, true
	}

	coding.Status = entry.Status
	coding.ReplacedBy = entry.ReplacedBy
	concept := &domain.Concept{
		System:  entry.ConceptSys,
		Code:    entry.Concept,
		Display: entry.Display,
	}
	return Outcome{Coding: coding, Concept: concept}, true
}
