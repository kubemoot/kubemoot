package workspace

import (
	"mime"
	"strings"

	"github.com/kubemoot/kubemoot/artifact-access/internal/store"
)

// HeaderContentType is the NATS object header that says what a stored result is. The agent
// runtime writes it when it stores the result; every reader here believes it and looks at the
// bytes only when it is missing (an object stored before the header existed).
const HeaderContentType = "Content-Type"

// mediaPlain is plain text; profileKubectlTable is the profile parameter that tells a kubectl
// table from other plain text.
const (
	mediaPlain          = "text/plain"
	profileParam        = "profile"
	profileKubectlTable = "kubectl-table"
)

// contentTypes is the one list of what each format is called as a content type. The agent
// runtime's ResultType (Java) writes the same strings; both run test/fixtures/result-types.json.
var contentTypes = map[Format]string{
	FormatJSON:          "application/json",
	FormatMarkdownTable: "text/markdown",
	FormatKubectlTable:  mediaPlain + "; " + profileParam + "=" + profileKubectlTable,
	FormatYAML:          "application/yaml",
	FormatCSV:           "text/csv",
	FormatText:          mediaPlain,
}

// mediaFormats maps the media types, without parameters, that name a format. Types an MCP server
// may send for the same thing are listed beside the ones Kubemoot writes.
var mediaFormats = map[string]Format{
	"application/json":   FormatJSON,
	"application/yaml":   FormatYAML,
	"application/x-yaml": FormatYAML,
	"text/yaml":          FormatYAML,
	"text/x-yaml":        FormatYAML,
	"text/csv":           FormatCSV,
	"text/markdown":      FormatMarkdownTable,
	mediaPlain:           FormatText,
}

// ContentType is the content type recorded for a format; text for a format it does not know.
func ContentType(f Format) string {
	if ct, ok := contentTypes[f]; ok {
		return ct
	}
	return contentTypes[FormatText]
}

// DetectContentType decides from the bytes alone what a result is, and names it as a content type.
func DetectContentType(data []byte) string {
	return ContentType(Detect(data))
}

// FormatOf is the format a content type names. False for an empty or unparsable value and for a
// type that is not one of the formats a query reads (an image, HTML); the caller then looks at
// the bytes. A +json or +yaml suffix type is the format of its suffix.
func FormatOf(contentType string) (Format, bool) {
	media, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return "", false
	}
	if media == mediaPlain && params[profileParam] == profileKubectlTable {
		return FormatKubectlTable, true
	}
	if f, ok := mediaFormats[media]; ok {
		return f, true
	}
	if i := strings.LastIndexByte(media, '+'); i >= 0 {
		f, ok := mediaFormats["application/"+media[i+1:]]
		return f, ok && (f == FormatJSON || f == FormatYAML)
	}
	return "", false
}

// HintOf is the format an object's Content-Type header names; empty when it has none, names a type
// a query does not read, or the result was cut at the size cap. A cut body is no longer what the
// header says of the whole result, so it is read by what it holds.
func HintOf(m store.Meta) Format {
	if m.Headers[headerTruncated] == flagSet {
		return ""
	}
	f, _ := FormatOf(m.Headers[HeaderContentType])
	return f
}

// trusted reports whether a recorded format can be used for data: the header is believed unless
// the bytes plainly are not that format. A JSON or Markdown table type must find what it names,
// so a server that labels any text "text/markdown" does not turn prose into an empty table.
func trusted(hint Format, data []byte) bool {
	switch hint {
	case "":
		return false
	case FormatJSON:
		return isJSON(data, nil)
	case FormatMarkdownTable:
		return isMarkdownTable(data, leadingLines(data, sniffLines))
	}
	return true
}
