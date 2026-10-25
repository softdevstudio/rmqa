package rmqa

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// The manifest is TOML. rmqa reads the part of TOML a manifest needs, with
// the standard library only: comments, key = value pairs, [tables],
// [[arrays of tables]], strings, integers, booleans and arrays. Anything
// else is a clear error with a line number rather than a silent guess.

// tomlValue is a parsed value plus the line it was set on.
type tomlValue struct {
	v    any // string, int64, bool, []any
	line int
}

// tomlTable is one table: keys in the order they appeared.
type tomlTable struct {
	keys   []string
	values map[string]tomlValue
	line   int
}

func newTable(line int) *tomlTable {
	return &tomlTable{values: map[string]tomlValue{}, line: line}
}

func (t *tomlTable) set(k string, v tomlValue) error {
	if _, dup := t.values[k]; dup {
		return fmt.Errorf("line %d: %q is set twice", v.line, k)
	}
	t.keys = append(t.keys, k)
	t.values[k] = v
	return nil
}

// tomlDoc is a whole document: the top-level table, and for each
// [[name]] the list of tables in order. Single [name] tables are kept in
// tables.
type tomlDoc struct {
	root   *tomlTable
	arrays map[string][]*tomlTable
	tables map[string]*tomlTable
}

type tomlParser struct {
	src  string
	pos  int
	line int
}

func parseTOML(src string) (*tomlDoc, error) {
	if !utf8.ValidString(src) {
		return nil, fmt.Errorf("the manifest is not valid UTF-8")
	}
	src = strings.TrimPrefix(src, "\ufeff")
	doc := &tomlDoc{root: newTable(1), arrays: map[string][]*tomlTable{}, tables: map[string]*tomlTable{}}
	p := &tomlParser{src: src, line: 1}
	cur := doc.root
	for {
		p.skipSpaceAndComments(true)
		if p.eof() {
			return doc, nil
		}
		c := p.src[p.pos]
		if c == '[' {
			line := p.line
			arr := strings.HasPrefix(p.src[p.pos:], "[[")
			if arr {
				p.pos += 2
			} else {
				p.pos++
			}
			p.skipInlineSpace()
			name, err := p.key()
			if err != nil {
				return nil, err
			}
			p.skipInlineSpace()
			if !p.eof() && p.src[p.pos] == '.' {
				return nil, fmt.Errorf("line %d: dotted table names such as [%s.x] aren't supported", line, name)
			}
			closer := "]"
			if arr {
				closer = "]]"
			}
			if !strings.HasPrefix(p.src[p.pos:], closer) {
				return nil, fmt.Errorf("line %d: expected %q after the table name", line, closer)
			}
			p.pos += len(closer)
			if err := p.endOfLine(); err != nil {
				return nil, err
			}
			t := newTable(line)
			if arr {
				if _, ok := doc.tables[name]; ok {
					return nil, fmt.Errorf("line %d: [[%s]] used after [%s]", line, name, name)
				}
				doc.arrays[name] = append(doc.arrays[name], t)
			} else {
				if _, ok := doc.tables[name]; ok {
					return nil, fmt.Errorf("line %d: [%s] appears twice", line, name)
				}
				if _, ok := doc.arrays[name]; ok {
					return nil, fmt.Errorf("line %d: [%s] used after [[%s]]", line, name, name)
				}
				doc.tables[name] = t
			}
			cur = t
			continue
		}
		line := p.line
		k, err := p.key()
		if err != nil {
			return nil, err
		}
		p.skipInlineSpace()
		if !p.eof() && p.src[p.pos] == '.' {
			return nil, fmt.Errorf("line %d: dotted keys such as %s.x aren't supported", line, k)
		}
		if p.eof() || p.src[p.pos] != '=' {
			return nil, fmt.Errorf("line %d: expected = after %q", line, k)
		}
		p.pos++
		p.skipInlineSpace()
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		if err := cur.set(k, tomlValue{v: v, line: line}); err != nil {
			return nil, err
		}
		if err := p.endOfLine(); err != nil {
			return nil, err
		}
	}
}

func (p *tomlParser) eof() bool { return p.pos >= len(p.src) }

func (p *tomlParser) skipInlineSpace() {
	for !p.eof() && (p.src[p.pos] == ' ' || p.src[p.pos] == '\t') {
		p.pos++
	}
}

// skipSpaceAndComments skips spaces, comments and, when newlines is true,
// line breaks.
func (p *tomlParser) skipSpaceAndComments(newlines bool) {
	for !p.eof() {
		c := p.src[p.pos]
		switch {
		case c == ' ' || c == '\t' || c == '\r':
			p.pos++
		case c == '\n' && newlines:
			p.pos++
			p.line++
		case c == '#':
			for !p.eof() && p.src[p.pos] != '\n' {
				p.pos++
			}
		default:
			return
		}
	}
}

func (p *tomlParser) endOfLine() error {
	p.skipInlineSpace()
	if !p.eof() && p.src[p.pos] == '#' {
		for !p.eof() && p.src[p.pos] != '\n' {
			p.pos++
		}
	}
	if !p.eof() && p.src[p.pos] == '\r' {
		p.pos++
	}
	if p.eof() {
		return nil
	}
	if p.src[p.pos] != '\n' {
		return fmt.Errorf("line %d: unexpected %q after the value", p.line, rest(p.src[p.pos:]))
	}
	p.pos++
	p.line++
	return nil
}

func rest(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 30 {
		s = s[:30] + "..."
	}
	return strings.TrimSpace(s)
}

func isBareKeyChar(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-'
}

func (p *tomlParser) key() (string, error) {
	if p.eof() {
		return "", fmt.Errorf("line %d: expected a key", p.line)
	}
	if c := p.src[p.pos]; c == '"' || c == '\'' {
		s, err := p.str()
		if err != nil {
			return "", err
		}
		return s, nil
	}
	start := p.pos
	for !p.eof() && isBareKeyChar(p.src[p.pos]) {
		p.pos++
	}
	if start == p.pos {
		return "", fmt.Errorf("line %d: expected a key, found %q", p.line, rest(p.src[p.pos:]))
	}
	return p.src[start:p.pos], nil
}

func (p *tomlParser) value() (any, error) {
	if p.eof() {
		return nil, fmt.Errorf("line %d: missing value", p.line)
	}
	c := p.src[p.pos]
	switch {
	case c == '"' || c == '\'':
		return p.str()
	case c == '[':
		return p.array()
	case c == '{':
		return nil, fmt.Errorf("line %d: inline tables { } aren't supported; use a [table] instead", p.line)
	case strings.HasPrefix(p.src[p.pos:], "true") && !p.bareContinues(4):
		p.pos += 4
		return true, nil
	case strings.HasPrefix(p.src[p.pos:], "false") && !p.bareContinues(5):
		p.pos += 5
		return false, nil
	case c == '+' || c == '-' || c >= '0' && c <= '9':
		start := p.pos
		p.pos++
		for !p.eof() && (p.src[p.pos] >= '0' && p.src[p.pos] <= '9' || p.src[p.pos] == '_') {
			p.pos++
		}
		if !p.eof() && (p.src[p.pos] == '.' || p.src[p.pos] == 'e' || p.src[p.pos] == 'E' || p.src[p.pos] == ':' || p.src[p.pos] == 'x') {
			return nil, fmt.Errorf("line %d: only whole numbers are supported here", p.line)
		}
		raw := strings.ReplaceAll(p.src[start:p.pos], "_", "")
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("line %d: %q is not a valid number", p.line, p.src[start:p.pos])
		}
		return n, nil
	}
	return nil, fmt.Errorf("line %d: can't read the value %q (strings need quotes)", p.line, rest(p.src[p.pos:]))
}

func (p *tomlParser) bareContinues(n int) bool {
	i := p.pos + n
	return i < len(p.src) && isBareKeyChar(p.src[i])
}

func (p *tomlParser) array() ([]any, error) {
	start := p.line
	p.pos++ // [
	out := []any{}
	for {
		p.skipSpaceAndComments(true)
		if p.eof() {
			return nil, fmt.Errorf("line %d: array is never closed with ]", start)
		}
		if p.src[p.pos] == ']' {
			p.pos++
			return out, nil
		}
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		out = append(out, v)
		p.skipSpaceAndComments(true)
		if p.eof() {
			return nil, fmt.Errorf("line %d: array is never closed with ]", start)
		}
		switch p.src[p.pos] {
		case ',':
			p.pos++
		case ']':
			p.pos++
			return out, nil
		default:
			return nil, fmt.Errorf("line %d: expected , or ] in the array, found %q", p.line, rest(p.src[p.pos:]))
		}
	}
}

func (p *tomlParser) str() (string, error) {
	q := p.src[p.pos]
	if strings.HasPrefix(p.src[p.pos:], `"""`) || strings.HasPrefix(p.src[p.pos:], `'''`) {
		return "", fmt.Errorf("line %d: multi-line strings aren't supported", p.line)
	}
	p.pos++
	var b strings.Builder
	for {
		if p.eof() || p.src[p.pos] == '\n' {
			return "", fmt.Errorf("line %d: string is never closed", p.line)
		}
		c := p.src[p.pos]
		if c == q {
			p.pos++
			return b.String(), nil
		}
		if c == '\\' && q == '"' {
			p.pos++
			if p.eof() {
				return "", fmt.Errorf("line %d: string is never closed", p.line)
			}
			e := p.src[p.pos]
			p.pos++
			switch e {
			case '"':
				b.WriteByte('"')
			case '\\':
				b.WriteByte('\\')
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			case 'b':
				b.WriteByte('\b')
			case 'f':
				b.WriteByte('\f')
			case 'u', 'U':
				n := 4
				if e == 'U' {
					n = 8
				}
				if p.pos+n > len(p.src) {
					return "", fmt.Errorf("line %d: short \\%c escape", p.line, e)
				}
				r, err := strconv.ParseUint(p.src[p.pos:p.pos+n], 16, 32)
				if err != nil || !utf8.ValidRune(rune(r)) {
					return "", fmt.Errorf("line %d: bad \\%c escape", p.line, e)
				}
				b.WriteRune(rune(r))
				p.pos += n
			default:
				return "", fmt.Errorf("line %d: unknown escape \\%c (write \\\\ for a backslash, or use 'single quotes')", p.line, e)
			}
			continue
		}
		b.WriteByte(c)
		p.pos++
	}
}
