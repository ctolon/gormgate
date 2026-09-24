package clickhouse

import (
	"slices"
	"testing"
)

// ClickHouse reports no constraint catalog: the CHECK constraints and the
// data-skipping indexes of a table are read back out of the SHOW CREATE
// TABLE text, so these helpers decide what the schema editor believes the
// table has.

func TestTableElements(t *testing.T) {
	cases := []struct{ name, ddl, want string }{
		{"plain", "CREATE TABLE t (a Int64, b String) ENGINE = MergeTree()", "a Int64, b String"},
		{"nested parentheses", "CREATE TABLE t (a Decimal(10, 2)) ENGINE = Log", "a Decimal(10, 2)"},
		// A parenthesis inside a string literal or a quoted identifier
		// does not close the element list.
		{"parenthesis in a literal", "CREATE TABLE t (a String DEFAULT ')') ENGINE = Log", "a String DEFAULT ')'"},
		{"parenthesis in an identifier", "CREATE TABLE t (`a)b` String) ENGINE = Log", "`a)b` String"},
		{"no parentheses", "CREATE TABLE t AS other", ""},
		{"unclosed", "CREATE TABLE t (a Int64", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := tableElements(c.ddl); got != c.want {
				t.Errorf("tableElements(%q) = %q, want %q", c.ddl, got, c.want)
			}
		})
	}
}

func TestSkipQuoted(t *testing.T) {
	cases := []struct {
		name  string
		s     string
		quote byte
		want  int
	}{
		{"simple", "'ab'rest", '\'', 4},
		{"doubled quote", "'a''b' rest", '\'', 6},
		{"backslash escape", `'a\'b' rest`, '\'', 6},
		{"backtick", "`ab` rest", '`', 4},
		{"unterminated", "'ab", '\'', 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := skipQuoted(c.s, c.quote); got != c.want {
				t.Errorf("skipQuoted(%q, %q) = %d, want %d", c.s, c.quote, got, c.want)
			}
		})
	}
}

func TestSplitTopLevel(t *testing.T) {
	cases := []struct {
		name string
		s    string
		want []string
	}{
		{"plain", "a, b, c", []string{"a", "b", "c"}},
		{"parentheses", "a Decimal(10, 2), b String", []string{"a Decimal(10, 2)", "b String"}},
		{"brackets", "a Array[1, 2], b", []string{"a Array[1, 2]", "b"}},
		{"comma in a literal", "a DEFAULT 'x,y', b", []string{"a DEFAULT 'x,y'", "b"}},
		{"comma in an identifier", "`a,b` String, c", []string{"`a,b` String", "c"}},
		{"trailing comma", "a, b,", []string{"a", "b"}},
		{"empty", "", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := splitTopLevel(c.s); !slices.Equal(got, c.want) {
				t.Errorf("splitTopLevel(%q) = %q, want %q", c.s, got, c.want)
			}
		})
	}
}

func TestUnquoteIdentifier(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"bare", "col", "col"},
		{"quoted", "`col`", "col"},
		{"doubled backtick", "`a``b`", "a`b"},
		{"spaces trimmed", "  col  ", "col"},
		{"digits after a letter", "col2", "col2"},
		// An expression is not an identifier: the caller has to tell a
		// plain column apart from one, because only a column follows a
		// rename.
		{"expression", "lower(col)", ""},
		{"leading digit", "2col", ""},
		{"empty", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := unquoteIdentifier(c.in); got != c.want {
				t.Errorf("unquoteIdentifier(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestCutKeyword(t *testing.T) {
	cases := []struct {
		name, s, keyword, want string
		ok                     bool
	}{
		{"match", "INDEX ix a TYPE minmax", "INDEX", "ix a TYPE minmax", true},
		{"case insensitive", "index ix", "INDEX", "ix", true},
		{"followed by a parenthesis", "CHECK(a > 0)", "CHECK", "(a > 0)", true},
		// A keyword that is only a prefix of the next word is not the
		// keyword: INDEXED is not INDEX.
		{"prefix of a longer word", "INDEXED ix", "INDEX", "INDEXED ix", false},
		{"other word", "CONSTRAINT c", "INDEX", "CONSTRAINT c", false},
		{"keyword alone", "INDEX", "INDEX", "INDEX", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := cutKeyword(c.s, c.keyword)
			if got != c.want || ok != c.ok {
				t.Errorf("cutKeyword(%q, %q) = %q, %v, want %q, %v", c.s, c.keyword, got, ok, c.want, c.ok)
			}
		})
	}
}

func TestCutIdentifier(t *testing.T) {
	cases := []struct{ name, in, wantName, wantRest string }{
		{"bare", "ix TYPE minmax", "ix", " TYPE minmax"},
		{"quoted", "`a b` TYPE minmax", "a b", " TYPE minmax"},
		{"doubled backtick", "`a``b` rest", "a`b", " rest"},
		{"whole string", "ix", "ix", ""},
		{"stops at a parenthesis", "ix(a)", "ix", "(a)"},
		{"leading spaces", "  ix rest", "ix", " rest"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			name, rest := cutIdentifier(c.in)
			if name != c.wantName || rest != c.wantRest {
				t.Errorf("cutIdentifier(%q) = %q, %q, want %q, %q", c.in, name, rest, c.wantName, c.wantRest)
			}
		})
	}
}
