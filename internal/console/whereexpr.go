package console

import (
	"fmt"
	"strings"
	"unicode"
)

// A raw WHERE expression for the grid, typed by the user like DBeaver's
// filter bar. It is spliced into the grid's query, so it is checked first,
// and the query runs as the project's read-only role inside BEGIN READ ONLY
// (which a statement cannot switch off once it has a snapshot), over the
// extended protocol (one statement only).
//
// The checks are about shape, not about privilege: the expression must stay
// inside the parentheses it is wrapped in, so it cannot end the WHERE clause
// and bolt on a UNION, an ORDER BY or a LIMIT of its own.

// MaxWhereLen bounds a raw filter expression.
const MaxWhereLen = 4000

// clauseWords end or extend a WHERE clause when they appear outside
// parentheses; inside (a subquery) they are fine.
var clauseWords = map[string]bool{
	"union": true, "intersect": true, "except": true, "order": true, "limit": true, "offset": true,
	"fetch": true, "for": true, "group": true, "having": true, "window": true, "returning": true,
	"into": true, "with": true, "select": true,
}

// ValidateWhere checks a raw filter expression.
func ValidateWhere(expr string) error {
	bad := func(format string, a ...any) error {
		return fmt.Errorf("%w: %s", ErrBadQuery, fmt.Sprintf(format, a...))
	}
	if strings.TrimSpace(expr) == "" {
		return bad("the filter is empty")
	}
	if len(expr) > MaxWhereLen {
		return bad("the filter is longer than %d characters", MaxWhereLen)
	}
	rs := []rune(expr)
	depth := 0
	for i := 0; i < len(rs); {
		c := rs[i]
		switch {
		case c == '\'' || c == '"':
			end, ok := skipQuoted(rs, i, c)
			if !ok {
				return bad("a quote is never closed")
			}
			i = end
		case c == '$':
			if end, ok := skipDollar(rs, i); ok {
				i = end
				continue
			} else if end == -1 {
				return bad("a $-quoted string is never closed")
			}
			i++
		case c == '-' && i+1 < len(rs) && rs[i+1] == '-':
			for i < len(rs) && rs[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(rs) && rs[i+1] == '*':
			end, ok := skipBlockComment(rs, i)
			if !ok {
				return bad("a /* comment is never closed")
			}
			i = end
		case c == ';':
			return bad("one expression only: remove the semicolon")
		case c == '(':
			depth++
			i++
		case c == ')':
			depth--
			if depth < 0 {
				return bad("there is a ) with no matching (")
			}
			i++
		case unicode.IsLetter(c) || c == '_':
			j := i
			for j < len(rs) && (unicode.IsLetter(rs[j]) || unicode.IsDigit(rs[j]) || rs[j] == '_' || rs[j] == '$') {
				j++
			}
			if w := strings.ToLower(string(rs[i:j])); depth == 0 && clauseWords[w] {
				return bad("%s can't be used here: write only the condition, as after WHERE", strings.ToUpper(w))
			}
			i = j
		default:
			i++
		}
	}
	if depth != 0 {
		return bad("a ( is never closed")
	}
	return nil
}

// skipQuoted returns the index after the quoted run starting at i (quote q
// doubled inside to escape it), and false when it never closes.
func skipQuoted(rs []rune, i int, q rune) (int, bool) {
	for j := i + 1; j < len(rs); j++ {
		if rs[j] != q {
			continue
		}
		if j+1 < len(rs) && rs[j+1] == q {
			j++
			continue
		}
		return j + 1, true
	}
	return 0, false
}

// skipDollar handles $tag$ ... $tag$ strings. It returns (end, true) after
// one, (-1, false) when one never closes, and (0, false) when the $ does not
// start one.
func skipDollar(rs []rune, i int) (int, bool) {
	j := i + 1
	for j < len(rs) && (unicode.IsLetter(rs[j]) || unicode.IsDigit(rs[j]) || rs[j] == '_') {
		j++
	}
	if j >= len(rs) || rs[j] != '$' {
		return 0, false
	}
	tag := rs[i : j+1]
	for k := j + 1; k+len(tag) <= len(rs); k++ {
		if string(rs[k:k+len(tag)]) == string(tag) {
			return k + len(tag), true
		}
	}
	return -1, false
}

// skipBlockComment returns the index after a (nested) /* ... */ comment.
func skipBlockComment(rs []rune, i int) (int, bool) {
	depth := 0
	for j := i; j+1 < len(rs); j++ {
		switch {
		case rs[j] == '/' && rs[j+1] == '*':
			depth++
			j++
		case rs[j] == '*' && rs[j+1] == '/':
			depth--
			j++
			if depth == 0 {
				return j + 1, true
			}
		}
	}
	return 0, false
}

// wrapWhere is how the expression sits in the query: on its own lines, so a
// trailing -- comment ends before the closing parenthesis.
func wrapWhere(expr string) string { return "(\n" + expr + "\n)" }
