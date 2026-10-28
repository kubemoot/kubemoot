package workspace

import (
	"fmt"
	"strings"
)

// checkSelect accepts text that is exactly one SELECT statement (also WITH ... SELECT
// and VALUES) and refuses everything else. It reads the text as SQLite does: string
// literals, quoted names and comments hide any semicolon or keyword inside them.
// Trailing semicolons are allowed; a second statement, an unbalanced parenthesis (an
// attempt to close the wrapper query) and a NUL byte are not.
func checkSelect(query string) error {
	if len(query) > maxSQLBytes {
		return fmt.Errorf("the query is over %d bytes", maxSQLBytes)
	}
	if strings.ContainsRune(query, 0) {
		return fmt.Errorf("%w: the query holds a NUL byte", ErrNotReadOnly)
	}
	words, err := scanSQL(query)
	if err != nil {
		return err
	}
	if len(words) == 0 {
		return fmt.Errorf("%w: the query is empty", ErrNotReadOnly)
	}
	switch words[0] {
	case "SELECT", "WITH", "VALUES":
		return nil
	}
	return ErrNotReadOnly
}

// scanSQL walks the text and returns its first keyword. It reports an error for a
// second statement, unbalanced parentheses and an unterminated literal or comment.
func scanSQL(query string) ([]string, error) {
	s := &sqlScanner{src: query}
	for s.pos < len(s.src) {
		if err := s.step(); err != nil {
			return nil, err
		}
	}
	if s.depth != 0 {
		return nil, fmt.Errorf("%w: unbalanced parentheses", ErrNotReadOnly)
	}
	return s.words, nil
}

type sqlScanner struct {
	src   string
	pos   int
	depth int
	words []string
	ended bool // a semicolon ended the statement
}

// step consumes one token.
func (s *sqlScanner) step() error {
	c := s.src[s.pos]
	switch {
	case strings.IndexByte("'\"`[", c) >= 0:
		return s.quote(c)
	case c == '-' || c == '/':
		return s.comment()
	case c == ';':
		s.ended = true
		s.pos++
	case c == '(' || c == ')':
		return s.paren(c)
	case isWordByte(c):
		return s.word()
	default:
		return s.other()
	}
	return nil
}

// quote skips a literal or quoted name that starts with c.
func (s *sqlScanner) quote(c byte) error {
	if c == '[' {
		return s.quoted('[', ']')
	}
	return s.quoted(c, c)
}

// comment skips a line or block comment; a lone dash or slash is an operator.
func (s *sqlScanner) comment() error {
	switch {
	case strings.HasPrefix(s.src[s.pos:], "--"):
		s.skipTo("\n")
		return nil
	case strings.HasPrefix(s.src[s.pos:], "/*"):
		return s.blockComment()
	}
	return s.other()
}

func (s *sqlScanner) paren(c byte) error {
	if s.ended {
		return fmt.Errorf("%w: found a second statement", ErrNotReadOnly)
	}
	if c == '(' {
		s.depth++
	} else if s.depth--; s.depth < 0 {
		return fmt.Errorf("%w: unbalanced parentheses", ErrNotReadOnly)
	}
	s.pos++
	return nil
}

// other consumes a byte that is part of no token; after a semicolon only space is allowed.
func (s *sqlScanner) other() error {
	if s.ended && !isSpace(s.src[s.pos]) {
		return fmt.Errorf("%w: found a second statement", ErrNotReadOnly)
	}
	s.pos++
	return nil
}

func (s *sqlScanner) word() error {
	if s.ended {
		return fmt.Errorf("%w: found a second statement", ErrNotReadOnly)
	}
	start := s.pos
	for s.pos < len(s.src) && isWordByte(s.src[s.pos]) {
		s.pos++
	}
	if len(s.words) == 0 {
		s.words = append(s.words, strings.ToUpper(s.src[start:s.pos]))
	}
	return nil
}

// quoted skips a literal or quoted name that opens with open and closes with closeCh;
// a doubled closing quote is an escape.
func (s *sqlScanner) quoted(open, closeCh byte) error {
	if s.ended {
		return fmt.Errorf("%w: found a second statement", ErrNotReadOnly)
	}
	i := s.pos + 1
	for i < len(s.src) {
		if s.src[i] == closeCh {
			if open != '[' && i+1 < len(s.src) && s.src[i+1] == closeCh {
				i += 2
				continue
			}
			s.pos = i + 1
			if len(s.words) == 0 {
				s.words = append(s.words, "QUOTED")
			}
			return nil
		}
		i++
	}
	return fmt.Errorf("invalid SQL: unterminated quote")
}

func (s *sqlScanner) blockComment() error {
	end := strings.Index(s.src[s.pos+2:], "*/")
	if end < 0 {
		return fmt.Errorf("invalid SQL: unterminated comment")
	}
	s.pos += end + 4
	return nil
}

// skipTo moves past the next occurrence of mark, or to the end when there is none.
func (s *sqlScanner) skipTo(mark string) {
	if i := strings.Index(s.src[s.pos:], mark); i >= 0 {
		s.pos += i + len(mark)
		return
	}
	s.pos = len(s.src)
}

func isWordByte(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' }
