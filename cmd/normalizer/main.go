// Command normalizer imports local sample messages into the unified model.
//
// Usage:
//
//	go run ./cmd/normalizer -samples ./samples/messages \
//	    -sources ./samples/sources.json -tables ./tables/diag \
//	    -diag-version diag-v1 -workers 4
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lechandonga/clinical-record-normalizer-go/internal/ingest"
	"github.com/lechandonga/clinical-record-normalizer-go/internal/normalize"
	"github.com/lechandonga/clinical-record-normalizer-go/internal/pipeline"
	"github.com/lechandonga/clinical-record-normalizer-go/internal/terminology"
)

// traceLogger prints the raw input and normalized outcome for every record.
type traceLogger struct{}

func (traceLogger) LogRecord(name string, raw []byte, cand *normalize.Candidate, rej *normalize.Rejection) {
	// Per-record trace goes to stderr so stdout remains a clean JSON report.
	out := os.Stderr
	fmt.Fprintf(out, "---- record %s\n", name)
	fmt.Fprintf(out, "raw:   %s\n", strings.TrimSpace(string(raw)))
	switch {
	case rej != nil:
		fmt.Fprintf(out, "reject reason=%s field=%s detail=%s\n\n", rej.Reason, rej.Field, rej.Detail)
	case cand != nil:
		b, _ := json.Marshal(cand)
		fmt.Fprintf(out, "normalized: %s\n\n", string(b))
	}
}

func main() {
	samplesDir := flag.String("samples", "./samples/messages", "directory of sample messages")
	sourcesPath := flag.String("sources", "./samples/sources.json", "source profiles JSON")
	tablesDir := flag.String("tables", "./tables/diag", "directory of diag table JSON files")
	diagVersion := flag.String("diag-version", "", "diag table version to pin (default: first loaded)")
	workers := flag.Int("workers", 4, "concurrent worker count")
	mode := flag.String("mode", "concurrent", "serial|concurrent")
	batchID := flag.String("batch-id", "BATCH-LOCAL-1", "deterministic batch identifier")
	flag.Parse()

	registry := terminology.NewRegistry()
	tableEntries, err := os.ReadDir(*tablesDir)
	if err != nil {
		log.Fatalf("read tables dir: %v", err)
	}
	tableNames := make([]string, 0)
	for _, e := range tableEntries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			tableNames = append(tableNames, e.Name())
		}
	}
	sort.Strings(tableNames)
	for _, name := range tableNames {
		raw, err := os.ReadFile(filepath.Join(*tablesDir, name))
		if err != nil {
			log.Fatalf("read table %s: %v", name, err)
		}
		tbl, err := terminology.LoadTable(raw)
		if err != nil {
			log.Fatalf("load table %s: %v", name, err)
		}
		if err := registry.Load(tbl); err != nil {
			log.Fatalf("register table %s: %v", name, err)
		}
	}
	if *diagVersion == "" {
		if vs := registry.Versions("diag"); len(vs) > 0 {
			*diagVersion = vs[0]
		}
	}

	profilesRaw, err := os.ReadFile(*sourcesPath)
	if err != nil {
		log.Fatalf("read sources: %v", err)
	}
	profiles, err := ingest.LoadProfiles(profilesRaw)
	if err != nil {
		log.Fatalf("load sources: %v", err)
	}
	catalog, err := ingest.NewCatalog(profiles)
	if err != nil {
		log.Fatalf("catalog: %v", err)
	}
	priorities := map[string]int{}
	for _, pr := range profiles {
		priorities[pr.ID] = pr.Priority
	}

	inputs, err := loadInputs(*samplesDir)
	if err != nil {
		log.Fatalf("load samples: %v", err)
	}

	pl := pipeline.New(catalog, registry, priorities, traceLogger{})
	batch := pipeline.Batch{ID: *batchID, DiagVersion: *diagVersion}

	var rep pipeline.Report
	switch *mode {
	case "serial":
		rep = pl.RunSerial(inputs, batch)
	case "concurrent":
		rep = pl.RunConcurrent(inputs, batch, *workers)
	default:
		log.Fatalf("unknown mode %q", *mode)
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(rep); err != nil {
		log.Fatalf("encode report: %v", err)
	}
	fmt.Fprintf(os.Stderr, "accepted=%d rejected=%d patients=%d encounters=%d\n",
		rep.Accepted, rep.Rejected, len(rep.Patients), len(rep.Encounters))
}

func loadInputs(dir string) ([]pipeline.Input, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0)
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	inputs := make([]pipeline.Input, 0, len(names))
	for _, name := range names {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", name, err)
		}
		inputs = append(inputs, pipeline.Input{Name: name, Raw: raw})
	}
	return inputs, nil
}
