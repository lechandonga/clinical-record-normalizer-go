package pipeline

import (
	"sort"
	"sync"

	"github.com/lechandonga/clinical-record-normalizer-go/internal/domain"
	"github.com/lechandonga/clinical-record-normalizer-go/internal/identity"
	"github.com/lechandonga/clinical-record-normalizer-go/internal/ingest"
	"github.com/lechandonga/clinical-record-normalizer-go/internal/normalize"
	"github.com/lechandonga/clinical-record-normalizer-go/internal/terminology"
)

// Logger receives the raw input and normalized result for each record.
type Logger interface {
	LogRecord(inputName string, raw []byte, cand *normalize.Candidate, rej *normalize.Rejection)
}

// Pipeline is a stateless, deterministic per-batch processor.
type Pipeline struct {
	catalog    *ingest.Catalog
	normalizer *normalize.Normalizer
	resolver   *identity.Resolver
	registry   *terminology.Registry
	logger     Logger
}

// New wires a pipeline from the catalog and registry.
func New(catalog *ingest.Catalog, registry *terminology.Registry,
	priorities map[string]int, logger Logger) *Pipeline {
	return &Pipeline{
		catalog:    catalog,
		normalizer: normalize.New(registry),
		resolver:   identity.NewResolver(priorities),
		registry:   registry,
		logger:     logger,
	}
}

type processed struct {
	index int
	name  string
	raw   string
	cand  *normalize.Candidate
	rej   *normalize.Rejection
}

func (p *Pipeline) processOne(index int, in Input, batch Batch) processed {
	raw := append([]byte(nil), in.Raw...)

	env, perr := ingest.Parse(raw)
	if perr != nil {
		rej := &normalize.Rejection{
			Reason: normalize.RejectReason(perr.Reason),
			Field:  perr.Field,
			Detail: perr.Detail,
			Raw:    string(raw),
		}
		p.log(in.Name, raw, nil, rej)
		return processed{index: index, name: in.Name, raw: string(raw), rej: rej}
	}

	ref := domain.SourceRef{SourceID: env.SourceID, MessageID: env.MessageID}
	profile, ok := p.catalog.Get(env.SourceID)
	if !ok {
		rej := &normalize.Rejection{
			Reason: normalize.RejectUnknownSource,
			Field:  "source_id",
			Detail: "source is not registered: " + env.SourceID,
			Source: &ref,
			Raw:    string(raw),
		}
		p.log(in.Name, raw, nil, rej)
		return processed{index: index, name: in.Name, raw: string(raw), rej: rej}
	}

	cand, rej := p.normalizer.Normalize(env, profile, batch.DiagVersion, raw)
	p.log(in.Name, raw, cand, rej)
	return processed{index: index, name: in.Name, raw: string(raw), cand: cand, rej: rej}
}

func (p *Pipeline) log(name string, raw []byte, c *normalize.Candidate, r *normalize.Rejection) {
	if p.logger != nil {
		p.logger.LogRecord(name, raw, c, r)
	}
}

// RunSerial imports inputs one at a time in slice order.
func (p *Pipeline) RunSerial(inputs []Input, batch Batch) Report {
	processed := make([]processed, len(inputs))
	for i, in := range inputs {
		processed[i] = p.processOne(i, in, batch)
	}
	return p.assemble(processed, batch)
}

// RunConcurrent processes records in parallel workers but performs all
// merging and ordering in a single deterministic reduction, so its output is
// byte-for-byte identical to RunSerial for the same inputs.
func (p *Pipeline) RunConcurrent(inputs []Input, batch Batch, workers int) Report {
	if workers < 1 {
		workers = 1
	}
	if workers > len(inputs) && len(inputs) > 0 {
		workers = len(inputs)
	}

	results := make([]processed, len(inputs))
	var wg sync.WaitGroup
	ch := make(chan int)

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range ch {
				results[i] = p.processOne(i, inputs[i], batch)
			}
		}()
	}
	for i := range inputs {
		ch <- i
	}
	close(ch)
	wg.Wait()

	return p.assemble(results, batch)
}

// assemble reduces per-record results into the traceable batch report.
func (p *Pipeline) assemble(results []processed, batch Batch) Report {
	rep := Report{Batch: batch}

	// Exact duplicate detection: same source+message, first in input order
	// wins; later copies are rejected with a distinguishable reason.
	firstSeen := map[domain.SourceRef]int{}

	candidates := make([]*normalize.Candidate, 0, len(results))
	for _, pr := range results {
		if pr.rej != nil {
			rep.Rejections = append(rep.Rejections, *pr.rej)
			continue
		}
		c := pr.cand
		if first, dup := firstSeen[c.Ref]; dup {
			rep.Rejections = append(rep.Rejections, normalize.Rejection{
				Reason: normalize.RejectDuplicate,
				Detail: "duplicate of input #" + itoa(first),
				Source: &domain.SourceRef{
					SourceID:  c.Ref.SourceID,
					MessageID: c.Ref.MessageID,
				},
				Raw: pr.raw,
			})
			continue
		}
		firstSeen[c.Ref] = pr.index
		candidates = append(candidates, c)
	}

	// Canonical processing order independent of arrival/worker scheduling.
	sort.Slice(candidates, func(i, j int) bool {
		return lessRef(candidates[i].Ref, candidates[j].Ref)
	})

	res := p.resolver.Resolve(candidates)

	encounters := make([]domain.Encounter, 0, len(candidates))
	for _, c := range candidates {
		enc := *c.Encounter
		enc.PatientID = res.PatientIDByRef[c.Ref]
		encounters = append(encounters, enc)
	}
	sort.Slice(encounters, func(i, j int) bool {
		return encounters[i].ID < encounters[j].ID
	})

	patients := append([]domain.Patient(nil), res.Patients...)
	sort.Slice(patients, func(i, j int) bool {
		return patients[i].ID < patients[j].ID
	})

	// Rejections stay in input order so malformed records are easy to locate.
	rep.Accepted = len(candidates)
	rep.Rejected = len(rep.Rejections)
	rep.Patients = patients
	rep.Encounters = encounters
	return rep
}

func lessRef(a, b domain.SourceRef) bool {
	if a.SourceID != b.SourceID {
		return a.SourceID < b.SourceID
	}
	return a.MessageID < b.MessageID
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(b[pos:])
}
