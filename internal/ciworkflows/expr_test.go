package ciworkflows

import (
	"strconv"
	"strings"
	"testing"
	"unicode"
)

// A minimal evaluator for the GitHub Actions expression subset used by the
// concurrency blocks: property lookups, 'string' literals, numbers, ==, !=,
// &&, || (Actions semantics: they return an operand, not a bool), parentheses
// and format(). Unknown properties evaluate to null, as in Actions.

func interpolate(t *testing.T, s string, ctx map[string]any) string {
	t.Helper()
	var out strings.Builder
	for {
		start := strings.Index(s, "${{")
		if start < 0 {
			out.WriteString(s)
			return out.String()
		}
		end := strings.Index(s[start:], "}}")
		if end < 0 {
			t.Fatalf("unterminated expression in %q", s)
		}
		out.WriteString(s[:start])
		out.WriteString(toString(eval(t, s[start+3:start+end], ctx)))
		s = s[start+end+2:]
	}
}

type parser struct {
	t    *testing.T
	toks []string
	pos  int
	ctx  map[string]any
}

func eval(t *testing.T, expr string, ctx map[string]any) any {
	t.Helper()
	p := &parser{t: t, toks: tokenize(t, expr), ctx: ctx}
	v := p.or()
	if p.pos != len(p.toks) {
		t.Fatalf("unexpected token %q in %q", p.toks[p.pos], expr)
	}
	return v
}

func tokenize(t *testing.T, s string) []string {
	var toks []string
	for i := 0; i < len(s); {
		c := rune(s[i])
		switch {
		case unicode.IsSpace(c):
			i++
		case strings.HasPrefix(s[i:], "&&"), strings.HasPrefix(s[i:], "||"),
			strings.HasPrefix(s[i:], "=="), strings.HasPrefix(s[i:], "!="):
			toks = append(toks, s[i:i+2])
			i += 2
		case c == '(' || c == ')' || c == ',':
			toks = append(toks, string(c))
			i++
		case c == '\'':
			j := i + 1
			for j < len(s) && s[j] != '\'' {
				j++
			}
			if j >= len(s) {
				t.Fatalf("unterminated string in %q", s)
			}
			toks = append(toks, s[i:j+1])
			i = j + 1
		default:
			j := i
			for j < len(s) && (unicode.IsLetter(rune(s[j])) || unicode.IsDigit(rune(s[j])) || strings.ContainsRune("._-", rune(s[j]))) {
				j++
			}
			if j == i {
				t.Fatalf("unexpected character %q in %q", c, s)
			}
			toks = append(toks, s[i:j])
			i = j
		}
	}
	return toks
}

func (p *parser) peek() string {
	if p.pos < len(p.toks) {
		return p.toks[p.pos]
	}
	return ""
}

func (p *parser) next() string { tok := p.peek(); p.pos++; return tok }

func (p *parser) expect(tok string) {
	if got := p.next(); got != tok {
		p.t.Fatalf("expected %q, got %q", tok, got)
	}
}

func (p *parser) or() any {
	left := p.and()
	for p.peek() == "||" {
		p.next()
		right := p.and()
		if !truthy(left) {
			left = right
		}
	}
	return left
}

func (p *parser) and() any {
	left := p.cmp()
	for p.peek() == "&&" {
		p.next()
		right := p.cmp()
		if truthy(left) {
			left = right
		}
	}
	return left
}

func (p *parser) cmp() any {
	left := p.primary()
	for p.peek() == "==" || p.peek() == "!=" {
		op := p.next()
		right := p.primary()
		eq := toString(left) == toString(right) && (left == nil) == (right == nil)
		left = eq == (op == "==")
	}
	return left
}

func (p *parser) primary() any {
	tok := p.next()
	switch {
	case tok == "(":
		v := p.or()
		p.expect(")")
		return v
	case strings.HasPrefix(tok, "'"):
		return strings.Trim(tok, "'")
	case tok == "true" || tok == "false":
		return tok == "true"
	case tok == "null":
		return nil
	case tok == "format" && p.peek() == "(":
		p.next()
		format := toString(p.or())
		var args []any
		for p.peek() == "," {
			p.next()
			args = append(args, p.or())
		}
		p.expect(")")
		for i, a := range args {
			format = strings.ReplaceAll(format, "{"+strconv.Itoa(i)+"}", toString(a))
		}
		return format
	}
	if n, err := strconv.ParseFloat(tok, 64); err == nil {
		return n
	}
	if v, ok := p.ctx[tok]; ok {
		return v
	}
	return nil
}

func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case float64:
		return x != 0
	}
	return true
}

func toString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case bool:
		return strconv.FormatBool(x)
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	}
	return ""
}
