// Command normalizer loads local sample messages and codesets, imports the
// messages, and prints a deterministic JSON report for local verification.
//
// Usage:
//
//	go run ./cmd/normalizer \
//	    -codesets ./samples/codesets \
//	    -messages ./samples/messages \
//	    -version v2026 \
//	    -now 2026-09-28T09:00:00Z
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/lechandonga/clinical-record-normalizer-go/model"
	"github.com/lechandonga/clinical-record-normalizer-go/registry"
	"github.com/lechandonga/clinical-record-normalizer-go/terminology"
)

func main() {
	root, _ := filepath.Abs(".")
	codesetDir := flag.String("codesets", filepath.Join(root, "samples", "codesets"), "codeset directory")
	messageDir := flag.String("messages", filepath.Join(root, "samples", "messages"), "message directory")
	version := flag.String("version", "", "codeset version to pin for all codesets (default: first loaded)")
	now := flag.String("now", "2026-09-28T09:00:00Z", "fixed wall clock (RFC3339) for reproducible runs")
	flag.Parse()

	clock, err := time.Parse(time.RFC3339, *now)
	if err != nil {
		fatal("invalid -now: %v", err)
	}

	terms := terminology.NewService()
	if err := terms.LoadDir(*codesetDir); err != nil {
		fatal("load codesets: %v", err)
	}
	if *version != "" {
		for _, name := range []string{"diagnosis", "encounter-type"} {
			if err := terms.SetActiveVersion(name, *version); err != nil {
				fatal("select version %s for %s: %v", *version, name, err)
			}
		}
	}

	raws, err := loadMessages(*messageDir)
	if err != nil {
		fatal("load messages: %v", err)
	}

	reg := registry.New(terms).WithClock(func() time.Time { return clock })
	report, err := reg.ImportBatch("local-sample", raws)
	if err != nil {
		fatal("import: %v", err)
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	out := struct {
		Report     model.ImportReport `json:"report"`
		Patients   []model.Patient    `json:"patients"`
		Encounters []model.Encounter  `json:"encounters"`
	}{
		Report:     report,
		Patients:   reg.Patients(),
		Encounters: reg.Encounters(),
	}
	if err := enc.Encode(out); err != nil {
		fatal("encode report: %v", err)
	}
}

// loadMessages reads every *.json file in dir. Files whose name starts with
// "_" are treated as malformed-message fixtures and loaded byte-for-byte
// without a recordId so the pipeline can reject them with a distinct reason.
func loadMessages(dir string) ([]model.RawMessage, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	var out []model.RawMessage
	for _, p := range files {
		body, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		base := filepath.Base(p)
		// Each file may contain a JSON array of messages or one object.
		source := strings.TrimSuffix(base, ".json")
		loaded, err := decodeFile(source, body)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		out = append(out, loaded...)
	}
	return out, nil
}

func decodeFile(source string, body []byte) ([]model.RawMessage, error) {
	trimmed := strings.TrimSpace(string(body))
	if len(trimmed) > 0 && trimmed[0] == '[' {
		var docs []json.RawMessage
		if err := json.Unmarshal(body, &docs); err != nil {
			// Malformed fixture: pass the raw bytes through as one record.
			return []model.RawMessage{{Source: source, RecordID: "malformed", Format: "json", Body: body}}, nil
		}
		out := make([]model.RawMessage, 0, len(docs))
		for i, d := range docs {
			var hdr struct {
				Source   string `json:"source"`
				RecordID string `json:"recordId"`
			}
			_ = json.Unmarshal(d, &hdr)
			src, rid := source, fmt.Sprintf("item-%d", i)
			if hdr.Source != "" {
				src = hdr.Source
			}
			if hdr.RecordID != "" {
				rid = hdr.RecordID
			}
			out = append(out, model.RawMessage{
				Source:   src,
				RecordID: rid,
				Format:   "json",
				Body:     d,
			})
		}
		return out, nil
	}
	var hdr struct {
		Source   string `json:"source"`
		RecordID string `json:"recordId"`
	}
	src, rid := source, "doc-0"
	if json.Unmarshal(body, &hdr) == nil {
		if hdr.Source != "" {
			src = hdr.Source
		}
		if hdr.RecordID != "" {
			rid = hdr.RecordID
		}
	}
	return []model.RawMessage{{Source: src, RecordID: rid, Format: "json", Body: body}}, nil
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "normalizer: "+format+"\n", args...)
	os.Exit(1)
}
