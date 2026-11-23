// Command qs-compare replays a JSONL query set against two query services and prints
// where their answers differ. Exit status 0 means every query matched, 1 means at least
// one differed, 2 means the command could not run.
//
//	qs-compare -queries set.jsonl \
//	  -a 'http://{source}-query.pilot:8000' \
//	  -b 'http://pilot-query.pilot:8000/sources/{source}'
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"

	"github.com/kubemoot/kubemoot/query-service-go/internal/golden"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("qs-compare", flag.ContinueOnError)
	fs.SetOutput(stderr)
	queries := fs.String("queries", "", "JSONL query set (required)")
	a := fs.String("a", "", "base URL of the reference service, {source} is replaced (required)")
	b := fs.String("b", "", "base URL of the service under test, {source} is replaced (required)")
	tol := fs.Float64("tolerance", golden.DefaultTolerance, "largest score difference that still matches")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *queries == "" || *a == "" || *b == "" {
		_, _ = fmt.Fprintln(stderr, "qs-compare: -queries, -a and -b are required")
		return 2
	}
	set, err := load(*queries)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "qs-compare:", err)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	rep := (&golden.Runner{A: *a, B: *b, Tolerance: *tol}).Run(ctx, set)
	return report(stdout, rep)
}

func load(path string) ([]golden.Query, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return golden.ReadQuerySet(f)
}

func report(w io.Writer, rep golden.Report) int {
	for _, m := range rep.Mismatches {
		_, _ = fmt.Fprintf(w, "MISMATCH %s\n", m.ID)
		for _, d := range m.Diffs {
			_, _ = fmt.Fprintf(w, "  %s\n", d)
		}
	}
	bad := len(rep.Mismatches)
	_, _ = fmt.Fprintf(w, "%d queries, %d matched, %d differed\n", rep.Total, rep.Total-bad, bad)
	if len(rep.Mismatches) > 0 {
		return 1
	}
	return 0
}
