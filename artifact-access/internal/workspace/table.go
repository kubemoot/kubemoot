package workspace

import (
	"bytes"
	"encoding/csv"
	"errors"
)

// ErrNotTable is returned by ReadTable for an artifact that is not a kubectl table, a Markdown
// table or CSV. The format it was detected as comes back with it.
var ErrNotTable = errors.New("not a table")

// tableReaders parse the formats that are tables. Each returns the rows with the first one as
// the header, named as workspace_query names columns.
var tableReaders = map[Format]func(data []byte, comma rune) ([][]string, error){
	FormatCSV:           readCSVRows,
	FormatMarkdownTable: readMarkdownRows,
	FormatKubectlTable:  readKubectlRows,
}

func readMarkdownRows(data []byte, _ rune) ([][]string, error) {
	return normalizeHeader(parseMarkdownTable(string(data))), nil
}

func readKubectlRows(data []byte, _ rune) ([][]string, error) {
	return normalizeHeader(parseKubectlTable(string(data))), nil
}

// ReadTable reads an artifact as a table the way workspace_query does: the format is detected
// from the content, and the header row gets SQL-safe column names. A comma other than ',' says
// the artifact is delimited text and skips detection. An artifact in any other format returns
// its format and ErrNotTable.
func ReadTable(data []byte, comma rune) (Format, [][]string, error) {
	format := FormatCSV
	if comma == 0 || comma == ',' {
		format = Detect(data)
	}
	read, ok := tableReaders[format]
	if !ok {
		return format, nil, ErrNotTable
	}
	rows, err := read(data, comma)
	return format, rows, err
}

// newCSVReader reads ragged rows and bare quotes, which tool output has.
func newCSVReader(data []byte, comma rune) *csv.Reader {
	r := csv.NewReader(bytes.NewReader(data))
	if comma != 0 {
		r.Comma = comma
	}
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	return r
}

func readCSVRows(data []byte, comma rune) ([][]string, error) {
	rows, err := newCSVReader(data, comma).ReadAll()
	return normalizeHeader(rows), err
}
