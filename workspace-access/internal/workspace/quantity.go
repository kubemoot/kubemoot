package workspace

import (
	"math/big"
	"path/filepath"
	"regexp"
	"strings"
)

// Units a quantity column is read in. A column of Kubernetes quantities is stored as numbers in
// these base units so ORDER BY and SUM compare values, not text.
const (
	unitBytes = "bytes"
	unitCores = "cores"
)

// quantityCell matches one Kubernetes quantity: a number with an optional binary (Ki..Ei),
// decimal (k..E) or milli (m) suffix, or an exponent (12e3).
var quantityCell = regexp.MustCompile(`^([+-]?(?:\d+(?:\.\d+)?|\.\d+))(Ki|Mi|Gi|Ti|Pi|Ei|k|M|G|T|P|E|m|[eE][+-]?\d+)?$`)

var (
	binaryFactor = map[string]int64{"Ki": 1, "Mi": 2, "Gi": 3, "Ti": 4, "Pi": 5, "Ei": 6}
	decimalPower = map[string]int64{"k": 3, "M": 6, "G": 9, "T": 12, "P": 15, "E": 18}
)

// parseQuantity returns the value of a quantity cell in base units, the unit its suffix
// implies ("" for a plain number or an exponent) and whether the cell is a quantity at all.
func parseQuantity(cell string) (value *big.Rat, unit string, ok bool) {
	m := quantityCell.FindStringSubmatch(cell)
	if m == nil {
		return nil, "", false
	}
	value, ok = new(big.Rat).SetString(m[1])
	if !ok {
		return nil, "", false
	}
	suffix := m[2]
	switch {
	case suffix == "":
		return value, "", true
	case suffix == "m":
		return value.Quo(value, big.NewRat(1000, 1)), unitCores, true
	case isExponent(suffix):
		exp, _ := new(big.Rat).SetString("1" + suffix)
		return value.Mul(value, exp), "", true
	}
	if p, found := binaryFactor[suffix]; found {
		return value.Mul(value, new(big.Rat).SetInt(new(big.Int).Lsh(big.NewInt(1), uint(10*p)))), unitBytes, true
	}
	pow := new(big.Int).Exp(big.NewInt(10), big.NewInt(decimalPower[suffix]), nil)
	return value.Mul(value, new(big.Rat).SetInt(pow)), unitBytes, true
}

// formatQuantity writes a base-unit value as an integer when it is whole, else as a decimal.
func formatQuantity(v *big.Rat) string {
	if v.IsInt() {
		return v.Num().String()
	}
	s := v.FloatString(9)
	return strings.TrimRight(strings.TrimRight(s, "0"), ".")
}

// quantityUnit decides the unit of column i of rows (the first row is the header): the column
// is a quantity column when every non-empty cell is a quantity and at least one has a suffix.
// A milli suffix reads as cores only under a cpu or core header, since 5m is also five minutes.
func quantityUnit(rows [][]string, i int) string {
	unit, ok := columnUnit(rows[1:], i)
	if !ok || (unit == unitCores && !cpuHeader(rows[0][i])) {
		return ""
	}
	return unit
}

// columnUnit is the one unit the suffixes of column i imply, and false when a non-empty cell
// is not a quantity or two cells imply different units.
func columnUnit(body [][]string, i int) (string, bool) {
	unit := ""
	for _, row := range body {
		if i >= len(row) || row[i] == "" {
			continue
		}
		_, u, ok := parseQuantity(row[i])
		if !ok || (u != "" && unit != "" && u != unit) {
			return "", false
		}
		if u != "" {
			unit = u
		}
	}
	return unit, true
}

// quantify rewrites every quantity column of rows in base units and returns the unit of each
// column by position ("" for a column that is not a quantity), or nil when there is none.
// The input is not changed; the raw artifact keeps the original text.
func quantify(rows [][]string) ([][]string, []string) {
	if len(rows) < 2 {
		return rows, nil
	}
	var units []string
	out := make([][]string, len(rows))
	for r := range rows {
		out[r] = append([]string(nil), rows[r]...)
	}
	for i := range rows[0] {
		unit := quantityUnit(rows, i)
		if unit == "" {
			continue
		}
		if units == nil {
			units = make([]string, len(rows[0]))
		}
		units[i] = unit
		for r := 1; r < len(out); r++ {
			if i < len(out[r]) && out[r][i] != "" {
				v, _, _ := parseQuantity(out[r][i])
				out[r][i] = formatQuantity(v)
			}
		}
	}
	return out, units
}

// quantifyRelation reads the quantity columns of a field table once, over all of its rows, and
// rewrites its file with base-unit numbers. Sections stacked into one table are read together, so
// a column is a quantity only when every section agrees. The relation it returns is not read again.
func quantifyRelation(rel Relation) (Relation, error) {
	if !rel.fields || rel.Kind != KindCSV {
		return rel, nil
	}
	var rows [][]string
	err := csvSource{path: rel.Path}.each(func(rec []string) error {
		rows = append(rows, append([]string(nil), rec...))
		return nil
	})
	if err != nil {
		return rel, err
	}
	out, units := quantify(rows)
	if units == nil {
		rel.fields = false
		return rel, nil
	}
	written, err := writeRows(rel.Name, filepath.Dir(rel.Path), out)
	if err != nil {
		return rel, err
	}
	written.Quantities = units
	written.fields = false
	return written, nil
}

// cpuHeader reports whether a column name counts cores: it holds the word cpu or core(s).
func cpuHeader(name string) bool {
	for _, w := range strings.FieldsFunc(strings.ToLower(name), func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	}) {
		if w == "cpu" || w == "cpus" || w == "core" || w == "cores" {
			return true
		}
	}
	return false
}

// isExponent reports whether a suffix is an exponent (e3, E-2) and not a unit such as Ei or E.
func isExponent(suffix string) bool {
	return len(suffix) > 1 && (suffix[0] == 'e' || suffix[0] == 'E') && suffix[1] != 'i'
}
