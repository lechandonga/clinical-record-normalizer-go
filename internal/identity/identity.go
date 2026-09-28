// Package identity merges normalized candidates into unified patients using a
// deterministic linkage and demographic conflict-resolution rule.
package identity

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"

	"github.com/lechandonga/clinical-record-normalizer-go/internal/domain"
	"github.com/lechandonga/clinical-record-normalizer-go/internal/normalize"
)

// Rule is the human-readable demographic resolution rule.
const Rule = "source_priority_then_recency_then_source_ref"

// Resolver is stateless; Resolve is a pure function of its inputs.
type Resolver struct {
	priority map[string]int
}

// NewResolver builds a resolver with the given source priorities.
func NewResolver(priority map[string]int) *Resolver {
	return &Resolver{priority: priority}
}

// Result is the merged patient set plus the encounter -> patient mapping.
type Result struct {
	Patients       []domain.Patient
	PatientIDByRef map[domain.SourceRef]string
}

// dsu is a small deterministic union-find.
type dsu struct {
	parent map[int]int
	rank   map[int]int
}

func newDSU(n int) *dsu {
	d := &dsu{parent: make(map[int]int, n), rank: make(map[int]int, n)}
	for i := 0; i < n; i++ {
		d.parent[i] = i
	}
	return d
}

func (d *dsu) find(x int) int {
	for d.parent[x] != x {
		d.parent[x] = d.parent[d.parent[x]]
		x = d.parent[x]
	}
	return x
}

func (d *dsu) union(a, b int) {
	ra, rb := d.find(a), d.find(b)
	if ra == rb {
		return
	}
	// Attach the larger index under the smaller one to keep roots stable.
	if ra > rb {
		ra, rb = rb, ra
	}
	d.parent[rb] = ra
}

// Resolve links candidates that share any identifier system+value, resolves
// demographic conflicts by (source priority desc, received desc, source ref
// asc) and assigns deterministic patient IDs.
func (r *Resolver) Resolve(candidates []*normalize.Candidate) Result {
	n := len(candidates)
	d := newDSU(n)

	// Link by identical (system, value). First-seen index order is the
	// candidate slice order; callers feed a deterministically sorted slice.
	type identKey struct{ system, value string }
	first := map[identKey]int{}
	for i, c := range candidates {
		for _, id := range c.Ident {
			k := identKey{id.System, id.Value}
			if j, ok := first[k]; ok {
				d.union(i, j)
			} else {
				first[k] = i
			}
		}
	}

	groups := map[int][]*normalize.Candidate{}
	for i, c := range candidates {
		root := d.find(i)
		groups[root] = append(groups[root], c)
	}

	// Build patients in root-index order for determinism.
	roots := make([]int, 0, len(groups))
	for root := range groups {
		roots = append(roots, root)
	}
	sort.Ints(roots)

	res := Result{PatientIDByRef: map[domain.SourceRef]string{}}
	for _, root := range roots {
		members := groups[root]
		pat := r.buildPatient(members)
		res.Patients = append(res.Patients, pat)
		for _, m := range members {
			res.PatientIDByRef[m.Ref] = pat.ID
		}
	}
	return res
}

func (r *Resolver) buildPatient(members []*normalize.Candidate) domain.Patient {
	// Deterministic member order: source ref.
	sort.Slice(members, func(i, j int) bool {
		return lessRef(members[i].Ref, members[j].Ref)
	})

	// Linkage key for the patient ID: all (system,value) pairs, sorted.
	keys := map[string]bool{}
	for _, m := range members {
		for _, id := range m.Ident {
			keys[id.System+"|"+id.Value] = true
		}
	}
	keyList := make([]string, 0, len(keys))
	for k := range keys {
		keyList = append(keyList, k)
	}
	sort.Strings(keyList)
	sum := sha256.Sum256([]byte(join(keyList, "\n")))
	pat := domain.Patient{
		ID: "PAT-" + hex.EncodeToString(sum[:12]),
	}

	// Collect identifiers deterministically (dedupe system+value).
	seenID := map[string]bool{}
	seenSrc := map[domain.SourceRef]bool{}
	for _, m := range members {
		for _, id := range m.Ident {
			k := id.System + "|" + id.Value
			if !seenID[k] {
				seenID[k] = true
				pat.Identifiers = append(pat.Identifiers, id)
			}
		}
		if !seenSrc[m.Ref] {
			seenSrc[m.Ref] = true
			pat.Sources = append(pat.Sources, m.Ref)
		}
	}

	pat.Sex, pat.Conflicts = r.resolveField("sex", members,
		func(c *normalize.Candidate) domain.TracedValue { return c.Sex }, pat.Conflicts)
	pat.BirthDate, pat.Conflicts = r.resolveField("birth_date", members,
		func(c *normalize.Candidate) domain.TracedValue { return c.BirthDate }, pat.Conflicts)

	return pat
}

// resolveField picks one winner by the fixed total order and records every
// distinct losing value as a conflict with its origin.
func (r *Resolver) resolveField(field string, members []*normalize.Candidate,
	get func(*normalize.Candidate) domain.TracedValue,
	conflicts []domain.FieldConflict) (domain.TracedValue, []domain.FieldConflict) {

	ranked := make([]domain.TracedValue, len(members))
	for i, m := range members {
		ranked[i] = get(m)
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		return r.prefer(ranked[i], ranked[j])
	})
	winner := ranked[0]
	for _, v := range ranked[1:] {
		if v.Value != winner.Value {
			conflicts = append(conflicts, domain.FieldConflict{
				Field:  field,
				Winner: winner,
				Loser:  v,
				Rule:   Rule,
			})
		}
	}
	return winner, conflicts
}

// prefer reports whether a should beat b.
func (r *Resolver) prefer(a, b domain.TracedValue) bool {
	pa, pb := r.priority[a.Source.SourceID], r.priority[b.Source.SourceID]
	if pa != pb {
		return pa > pb
	}
	if !a.Received.Equal(b.Received) {
		return a.Received.After(b.Received)
	}
	return lessRef(a.Source, b.Source)
}

func lessRef(a, b domain.SourceRef) bool {
	if a.SourceID != b.SourceID {
		return a.SourceID < b.SourceID
	}
	return a.MessageID < b.MessageID
}

func join(parts []string, sep string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += sep
		}
		out += p
	}
	return out
}
