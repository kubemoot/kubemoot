package workspace

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"github.com/kubemoot/kubemoot/artifact-access/internal/store"
)

// The catalog of a thread is the object store's own listing of the thread's key prefix, read
// from the headers the agent runtime sets on each stored tool call (see calls.go). Nothing
// else stores it. Its lines use the words of the review catalog the coordinator builds in the
// runtime (ThreadCatalog in agent-runtime): both render test/fixtures/catalog-lines.json, so the
// two cannot drift apart. The read-op adds the time the call started.
const (
	headerStarted = "kubemoot.started"
	headerBytes   = "kubemoot.bytes"
	// MaxCatalogCalls is the most call lines one listing carries; the rest are counted, and a
	// tool filter lists them.
	MaxCatalogCalls = 200
	// argsSummaryChars is where a call's arguments are cut on its line.
	argsSummaryChars = 60
	unknownValue     = "unknown"
	catalogHeader    = "[CATALOG "
	catalogCallStart = "[CATALOG-CALL key="
)

// ErrNoThread means a listing was asked for outside a discussion's thread.
var ErrNoThread = errors.New("this tool works on the thread it is called from, and this call names none")

// catalogEntry is one stored tool call as the listing shows it.
type catalogEntry struct {
	key     string
	tool    string
	args    string
	typ     string
	rows    int64
	bytes   int64
	started string
	failed  bool
}

// Catalog lists the stored tool calls under prefix (the thread's "{namespace}/{crew}/{thread}/"),
// one line each, oldest key first. A non-empty tool lists only that tool's calls. Objects that
// are not stored tool calls are not listed.
func Catalog(ctx context.Context, l store.Lister, prefix, tool string) (string, error) {
	if prefix == "" {
		return "", ErrNoThread
	}
	metas, err := l.List(ctx, prefix)
	if err != nil {
		return "", fmt.Errorf("list the workspace: %w", err)
	}
	entries := catalogEntries(metas, prefix, tool)
	return renderCatalog(entries), nil
}

func catalogEntries(metas []store.Meta, prefix, tool string) []catalogEntry {
	var out []catalogEntry
	for _, m := range metas {
		ci, ok := callInfoOf(m)
		if !ok || (tool != "" && ci.tool != tool) {
			continue
		}
		out = append(out, catalogEntry{
			key:     strings.TrimPrefix(m.Key, prefix),
			tool:    ci.tool,
			args:    ci.args,
			typ:     orUnknown(m.Headers[HeaderContentType]),
			rows:    numberHeader(m, headerRows, -1),
			bytes:   numberHeader(m, headerBytes, int64(m.Size)),
			started: orUnknown(m.Headers[headerStarted]),
			failed:  ci.failed,
		})
	}
	return out
}

func orUnknown(v string) string {
	if v == "" {
		return unknownValue
	}
	return v
}

// numberHeader reads a whole-number header; fallback when it is absent or not one.
func numberHeader(m store.Meta, name string, fallback int64) int64 {
	n, err := strconv.ParseInt(m.Headers[name], 10, 64)
	if err != nil {
		return fallback
	}
	return n
}

func renderCatalog(entries []catalogEntry) string {
	var errs, bytes int64
	for _, e := range entries {
		bytes += e.bytes
		if e.failed {
			errs++
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%scalls=%d errors=%d bytes=%d]\n", catalogHeader, len(entries), errs, bytes)
	for i, e := range entries {
		if i == MaxCatalogCalls {
			fmt.Fprintf(&b, "[%d more calls are not listed; pass tool to list the calls of one tool]\n",
				len(entries)-MaxCatalogCalls)
			break
		}
		b.WriteString(e.line() + "\n")
	}
	if len(entries) == 0 {
		b.WriteString("nothing has been stored in this thread" + "\n")
	}
	return b.String()
}

// line is the call's catalog line: the review catalog's fields in its order, then when the call
// started.
func (e catalogEntry) line() string {
	rows := unknownValue
	if e.rows >= 0 {
		rows = strconv.FormatInt(e.rows, 10)
	}
	return fmt.Sprintf("%s%s tool=%s args=%s type=%s rows=%s bytes=%d error=%t started=%s]",
		catalogCallStart, e.key, neutralize(e.tool), argsSummary(e.args), e.typ, rows, e.bytes, e.failed, e.started)
}

// neutralize turns every control character (an escape, a line separator) into a space, so header
// text read from the store cannot act on a terminal or end a catalog line. The stored value is
// not changed; only what is shown.
func neutralize(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			return ' '
		}
		return r
	}, s)
}

// isASCIISpace is what the runtime's \s matches.
func isASCIISpace(r rune) bool { return strings.ContainsRune(" \t\n\v\f\r", r) }

// argsSummary is a call's arguments on one line: white space folded, square brackets turned into
// parentheses so a list cannot end the line early, cut at argsSummaryChars. The runtime's
// CallManifest.argsSummary does the same.
func argsSummary(args string) string {
	folded := strings.Join(strings.FieldsFunc(neutralize(args), isASCIISpace), " ")
	one := strings.NewReplacer("[", "(", "]", ")").Replace(folded)
	if one == "" {
		return "{}"
	}
	if r := []rune(one); len(r) > argsSummaryChars {
		return string(r[:argsSummaryChars]) + "..."
	}
	return one
}
