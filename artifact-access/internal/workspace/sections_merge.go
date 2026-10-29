package workspace

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// errEnough stops a row walk once the first row has been seen.
var errEnough = errors.New("enough")

// shapeOf says what columns a relation has, so sections that read the same way can
// be told apart from those that do not: a CSV relation by its header, a JSON
// relation by the keys and the kind of its first row (a list of Pods and a list of
// Nodes have the same keys and are different tables).
func shapeOf(rel Relation) (string, error) {
	if rel.Kind == KindCSV {
		cols, err := csvSource{path: rel.Path}.columns()
		return "csv:" + strings.Join(cols, "\x1f"), err
	}
	src := &jsonSource{path: rel.Path, items: rel.Kind == KindJSONItems}
	var keys []string
	kind := ""
	err := src.each(func(row map[string]any) error {
		for k := range row {
			keys = append(keys, k)
		}
		kind, _ = row["kind"].(string)
		return errEnough
	})
	if err != nil && !errors.Is(err, errEnough) {
		return "", err
	}
	sort.Strings(keys)
	return "json:" + kind + "\x1f" + strings.Join(keys, "\x1f"), nil
}

// mergeGroup makes the one relation named table from sections of the same shape.
// A single section is used as it is.
func mergeGroup(table string, g []prepared, workDir string) (Relation, error) {
	if len(g) == 1 {
		rel := g[0].rel
		rel.Name = table
		return rel, nil
	}
	if g[0].rel.Kind == KindCSV {
		return mergeRows(table, g, workDir)
	}
	return mergeJSON(table, g, workDir)
}

// mergeRows stacks the data rows of CSV relations that share a header. Text
// sections are numbered again so line_no runs across all of them.
func mergeRows(table string, g []prepared, workDir string) (Relation, error) {
	var rows [][]string
	for i, p := range g {
		first := true
		err := csvSource{path: p.rel.Path}.each(func(rec []string) error {
			if first {
				first = false
				if i == 0 {
					rows = append(rows, rec)
				}
				return nil
			}
			rows = append(rows, rec)
			return nil
		})
		if err != nil {
			return Relation{}, fmt.Errorf("table %s: %w", table, err)
		}
	}
	if g[0].format == FormatText {
		for i := 1; i < len(rows); i++ {
			rows[i][0] = strconv.Itoa(i)
		}
	}
	return writeRows(table, workDir, rows)
}

// mergeJSON stacks the rows of JSON relations into one list of objects.
func mergeJSON(table string, g []prepared, workDir string) (Relation, error) {
	rows := []map[string]any{}
	for _, p := range g {
		src := &jsonSource{path: p.rel.Path, items: p.rel.Kind == KindJSONItems}
		err := src.each(func(row map[string]any) error {
			rows = append(rows, row)
			return nil
		})
		if err != nil {
			return Relation{}, fmt.Errorf("table %s: %w", table, err)
		}
	}
	out, err := json.Marshal(rows)
	if err != nil {
		return Relation{}, fmt.Errorf("table %s: %w", table, err)
	}
	path := filepath.Join(workDir, table+".json")
	if err := os.WriteFile(path, out, 0o600); err != nil {
		return Relation{}, fmt.Errorf("write %s: %w", table, err)
	}
	return Relation{Name: table, Path: path, Kind: KindJSON}, nil
}
