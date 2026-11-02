package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"strings"

	"github.com/kubemoot/kubemoot/artifact-access/internal/store"
)

// A raw-output tooler with per-call artifacts on stores each tool call's result as its own
// object, and the NATS object headers describe the call. The header names follow the
// OpenTelemetry GenAI semantic conventions where they define one, and are set by the agent
// runtime (CallHeaders in agent-runtime); an object without them is read as any other artifact.
const (
	headerOperation      = "gen_ai.operation.name"
	operationExecuteTool = "execute_tool"
	headerTool           = "gen_ai.tool.name"
	headerError          = "kubemoot.error"
	headerFormat         = "kubemoot.format"
	headerTruncated      = "kubemoot.truncated"
	headerRows           = "kubemoot.rows"
	// flagSet is the value of a header that is on.
	flagSet = "true"
)

const (
	// MaxGlobObjects is the most objects one key pattern may match.
	MaxGlobObjects = 100
	// minPatternSlashes is the slashes the fixed part of a key pattern must hold: it must name
	// the namespace, crew and thread (ns/crew/thread/), so a pattern never reaches into another thread.
	minPatternSlashes = 3
)

// callInfo is what an object's headers say about the tool call whose result it holds.
type callInfo struct {
	tool      string
	failed    bool
	truncated bool
	// hint is the format the producer says the body has; empty when it says text.
	hint Format
}

// callInfoOf reads the call headers of an object; false when the object is not a stored tool call.
func callInfoOf(m store.Meta) (callInfo, bool) {
	if m.Headers[headerOperation] != operationExecuteTool {
		return callInfo{}, false
	}
	ci := callInfo{
		tool:      m.Headers[headerTool],
		failed:    m.Headers[headerError] == flagSet,
		truncated: m.Headers[headerTruncated] == flagSet,
	}
	if m.Headers[headerFormat] == string(FormatJSON) {
		ci.hint = FormatJSON
	}
	return ci, true
}

// RowsHeader returns the row or item count the runtime recorded for a stored tool call, and
// whether it is a complete count: false when the header is absent or the result was cut.
func RowsHeader(m store.Meta) (int64, bool) {
	ci, ok := callInfoOf(m)
	if !ok || ci.truncated {
		return 0, false
	}
	var n int64
	if _, err := fmt.Sscan(m.Headers[headerRows], &n); err != nil {
		return 0, false
	}
	return n, true
}

// section is the body of one stored call as a tool section. A result cut at the runtime's
// size cap ends with the cut marker; it is handled like a cut section of a tooler's text.
func (c callInfo) section(data []byte) section {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	if !c.truncated {
		return section{tool: c.tool, body: text}
	}
	return closeSection([]section{{tool: c.tool}}, strings.Split(text, "\n"))[0]
}

// describe returns the call headers of key's object; false when the store keeps none.
func (s *Service) describe(ctx context.Context, key string) (callInfo, bool) {
	d, ok := s.Store.(store.Describer)
	if !ok {
		return callInfo{}, false
	}
	m, err := d.Describe(ctx, key)
	if err != nil {
		return callInfo{}, false
	}
	return callInfoOf(m)
}

// singleCall makes the relation for one stored tool call.
func singleCall(name string, data []byte, workDir string, taken map[string]bool,
	ci callInfo) ([]Relation, []string, error) {
	p, ok, err := prepareSection(name, ci.section(data), workDir, ci.hint)
	if err != nil {
		return nil, nil, err
	}
	if !ok {
		return nil, nil, errors.New("the call returned an empty result")
	}
	rels, notes, err := groupRelations(name, []prepared{p}, workDir, taken)
	if err == nil && ci.failed {
		notes = append(notes, "this call returned a tool error")
	}
	return rels, notes, err
}

// callGroup makes the relations for every stored call whose key matches pattern. Calls of the
// same shape are the rows of one table, as for a tooler's sections; a call that failed is left
// out and named in a note.
func (s *Service) callGroup(ctx context.Context, name, pattern, workDir string,
	taken map[string]bool) ([]Relation, []string, error) {
	metas, err := s.matching(ctx, pattern)
	if err != nil {
		return nil, nil, err
	}
	var items []prepared
	var skipped []string
	var total int64
	for i, m := range metas {
		ci, ok := callInfoOf(m)
		if !ok || ci.failed {
			skipped = append(skipped, m.Key)
			continue
		}
		_, data, err := s.readObject(ctx, m.Key, &total)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", m.Key, err)
		}
		p, ok, err := prepareSection(fmt.Sprintf("%s__call%d", name, i+1), ci.section(data), workDir, ci.hint)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", m.Key, err)
		}
		if ok {
			items = append(items, p)
		}
	}
	if len(items) == 0 {
		return nil, nil, fmt.Errorf("no successful tool call matches %q (%d skipped)", pattern, len(skipped))
	}
	rels, notes, err := groupRelations(name, items, workDir, taken)
	if len(skipped) > 0 {
		notes = append(notes, fmt.Sprintf("left out %d object(s) that are failed calls or not stored tool calls: %s",
			len(skipped), strings.Join(skipped, ", ")))
	}
	return rels, notes, err
}

// matching lists the objects whose key matches pattern, where * stands for any characters
// except a slash. The part before the first * must name the namespace, crew and thread.
func (s *Service) matching(ctx context.Context, pattern string) ([]store.Meta, error) {
	l, ok := s.Store.(store.Lister)
	if !ok {
		return nil, errors.New("this store cannot list objects, so a key with * cannot be used")
	}
	prefix := pattern[:strings.IndexByte(pattern, '*')]
	if strings.Count(prefix, "/") < minPatternSlashes {
		return nil, fmt.Errorf("a key with * must start with the namespace, crew and thread "+
			"(ns/crew/thread/), got %q", pattern)
	}
	if _, err := path.Match(pattern, ""); err != nil {
		return nil, fmt.Errorf("bad key pattern %q: %w", pattern, err)
	}
	all, err := l.List(ctx, prefix)
	if err != nil {
		return nil, fmt.Errorf("list %q: %w", prefix, err)
	}
	var out []store.Meta
	for _, m := range all {
		if ok, _ := path.Match(pattern, m.Key); ok {
			out = append(out, m)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no object matches %q", pattern)
	}
	if len(out) > MaxGlobObjects {
		return nil, fmt.Errorf("%q matches %d objects, over the limit of %d; narrow the key",
			pattern, len(out), MaxGlobObjects)
	}
	return out, nil
}

// readObject finds the object's file under Dir, fetching it first when it is not there yet,
// checks its size against the limit for one artifact, and returns the path and the bytes. When
// total is not nil it also adds the size to it and refuses once the sum passes the same limit.
func (s *Service) readObject(ctx context.Context, key string, total *int64) (string, []byte, error) {
	p, err := s.materialized(ctx, key)
	if err != nil {
		return "", nil, err
	}
	info, err := os.Stat(p)
	if err != nil {
		return "", nil, err
	}
	if s.MaxArtifactBytes > 0 && info.Size() > s.MaxArtifactBytes {
		return "", nil, fmt.Errorf("the artifact is %d bytes, over the %d byte limit for queries",
			info.Size(), s.MaxArtifactBytes)
	}
	if total != nil {
		*total += info.Size()
		if s.MaxArtifactBytes > 0 && *total > s.MaxArtifactBytes {
			return "", nil, fmt.Errorf("the matched calls together pass the %d byte limit for queries; narrow the key",
				s.MaxArtifactBytes)
		}
	}
	data, err := os.ReadFile(p)
	return p, data, err
}
