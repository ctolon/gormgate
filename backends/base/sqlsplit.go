package base

import (
	"strings"
)

// SplitOptions describes the lexical rules of one vendor's dialect that
// SplitSQL has to know about in order to find the top-level semicolons.
// They differ per vendor, so every backend passes its own value; the zero
// value is the standard SQL syntax that all of them share: single-quoted
// strings and double-quoted identifiers, in both of which the quote is
// escaped by doubling it, plus -- and /* */ comments.
type SplitOptions struct {
	// BackslashEscapes reports that a backslash escapes the next character
	// inside a single-quoted string, so that '\'' is a one-character
	// string rather than the end of one. It is MySQL's rule (unless
	// NO_BACKSLASH_ESCAPES is set) and ClickHouse's. PostgreSQL with
	// standard_conforming_strings = on, which is what Ops.QuoteValue
	// relies on, as well as SQLite and Oracle, have no such escape: there
	// a backslash is an ordinary character and the next quote ends the
	// string.
	BackslashEscapes bool
	// BacktickQuotes reports that `name` is a quoted identifier (MySQL,
	// SQLite, ClickHouse). Where it is not, a backtick is an ordinary
	// character.
	BacktickQuotes bool
	// DollarQuotes reports that $tag$ ... $tag$ delimits a string body
	// (PostgreSQL, and the forks that speak its dialect). Where it is not,
	// a dollar sign is an ordinary character -- in MySQL it may even be
	// part of an identifier.
	DollarQuotes bool
}

// SplitSQL splits a script into statements on top-level semicolons,
// skipping quoted strings, quoted identifiers, dollar-quoted bodies and
// comments as opts describes them. Empty statements are dropped and each
// statement keeps no trailing semicolon (sqlparse.split as used by
// prepare_sql_script).
func SplitSQL(script string, opts SplitOptions) []string {
	var out []string
	var cur strings.Builder
	flush := func() {
		s := strings.TrimSpace(cur.String())
		if s != "" && !onlyComments(s) {
			out = append(out, s)
		}
		cur.Reset()
	}
	rs := []rune(script)
	for i := 0; i < len(rs); i++ {
		c := rs[i]
		switch {
		case c == '\'' || c == '"' || (c == '`' && opts.BacktickQuotes):
			j := i + 1
			for j < len(rs) {
				if rs[j] == c {
					if j+1 < len(rs) && rs[j+1] == c {
						j += 2
						continue
					}
					break
				}
				if rs[j] == '\\' && c == '\'' && opts.BackslashEscapes {
					j++
				}
				j++
			}
			if j >= len(rs) {
				j = len(rs) - 1
			}
			cur.WriteString(string(rs[i : j+1]))
			i = j
		case c == '-' && i+1 < len(rs) && rs[i+1] == '-':
			j := i
			for j < len(rs) && rs[j] != '\n' {
				j++
			}
			cur.WriteString(string(rs[i:j]))
			i = j - 1
		case c == '/' && i+1 < len(rs) && rs[i+1] == '*':
			end := strings.Index(string(rs[i+2:]), "*/")
			j := len(rs) - 1
			if end >= 0 {
				j = i + 2 + len([]rune(string(rs[i+2:])[:end])) + 1
			}
			cur.WriteString(string(rs[i : j+1]))
			i = j
		case c == '$' && opts.DollarQuotes:
			// Dollar quoting: $tag$ ... $tag$
			j := i + 1
			for j < len(rs) && (rs[j] == '_' || isAlnum(rs[j])) {
				j++
			}
			if j < len(rs) && rs[j] == '$' {
				tag := string(rs[i : j+1])
				rest := string(rs[j+1:])
				end := strings.Index(rest, tag)
				if end >= 0 {
					body := string(rs[i:j+1]) + rest[:end] + tag
					cur.WriteString(body)
					i += len([]rune(body)) - 1
					continue
				}
			}
			cur.WriteRune(c)
		case c == ';':
			flush()
		default:
			cur.WriteRune(c)
		}
	}
	flush()
	return out
}

func isAlnum(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
}

func onlyComments(s string) bool {
	for _, line := range strings.Split(s, "\n") {
		l := strings.TrimSpace(line)
		if l != "" && !strings.HasPrefix(l, "--") {
			return false
		}
	}
	return true
}
