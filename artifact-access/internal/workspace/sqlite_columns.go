package workspace

import (
	"errors"
	"fmt"
	"strings"
)

// maxListedColumns bounds how many column names one table adds to an error.
const maxListedColumns = 60

// tableColumns is a loaded table and its column names, in the order the table has them.
type tableColumns struct {
	table   string
	columns []string
}

// withColumns adds the loaded tables and their columns to an error that names a column the
// tables do not have, so the next query can use a real name instead of a guess. Any other
// error is returned as it is.
func withColumns(err error, schema []tableColumns) error {
	if err == nil || !strings.Contains(err.Error(), "no such column") || len(schema) == 0 {
		return err
	}
	parts := make([]string, 0, len(schema))
	for _, t := range schema {
		parts = append(parts, t.table+" ("+listColumns(t.columns)+")")
	}
	return errors.New(err.Error() + "; the columns are: " + strings.Join(parts, "; "))
}

func listColumns(cols []string) string {
	if len(cols) <= maxListedColumns {
		return strings.Join(cols, ", ")
	}
	return fmt.Sprintf("%s, and %d more", strings.Join(cols[:maxListedColumns], ", "), len(cols)-maxListedColumns)
}
