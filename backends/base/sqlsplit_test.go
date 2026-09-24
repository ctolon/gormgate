package base

import (
	"slices"
	"testing"
)

var (
	standard = SplitOptions{}
	mysqlish = SplitOptions{BackslashEscapes: true, BacktickQuotes: true}
	postgres = SplitOptions{DollarQuotes: true}
)

func TestSplitSQLShared(t *testing.T) {
	// What every vendor agrees on: single-quoted strings with '' doubling,
	// double-quoted identifiers, line and block comments, and empty
	// statements dropped.
	cases := []struct {
		name   string
		script string
		want   []string
	}{
		{"plain", "SELECT 1; SELECT 2", []string{"SELECT 1", "SELECT 2"}},
		{"trailing semicolon", "SELECT 1;", []string{"SELECT 1"}},
		{"empty statements", ";;SELECT 1;;", []string{"SELECT 1"}},
		{"semicolon in string", "INSERT INTO t VALUES ('a;b'); SELECT 1",
			[]string{"INSERT INTO t VALUES ('a;b')", "SELECT 1"}},
		{"doubled quote", "INSERT INTO t VALUES ('a''; SELECT 1')",
			[]string{"INSERT INTO t VALUES ('a''; SELECT 1')"}},
		{"semicolon in identifier", `SELECT "a;b" FROM t; SELECT 1`,
			[]string{`SELECT "a;b" FROM t`, "SELECT 1"}},
		{"line comment", "SELECT 1; -- a; b\nSELECT 2", []string{"SELECT 1", "-- a; b\nSELECT 2"}},
		{"block comment", "SELECT /* a; b */ 1; SELECT 2", []string{"SELECT /* a; b */ 1", "SELECT 2"}},
		{"comment only", "-- nothing here\n", nil},
	}
	for _, c := range cases {
		for _, opts := range []struct {
			name string
			o    SplitOptions
		}{{"standard", standard}, {"mysql", mysqlish}, {"postgres", postgres}} {
			got := SplitSQL(c.script, opts.o)
			if !slices.Equal(got, c.want) {
				t.Errorf("%s/%s: SplitSQL(%q) = %q, want %q", c.name, opts.name, c.script, got, c.want)
			}
		}
	}
}

func TestSplitSQLBackslashEscapes(t *testing.T) {
	// MySQL reads \' as an escaped quote, so the string runs on past the
	// semicolon. PostgreSQL runs with standard_conforming_strings = on and
	// SQLite and Oracle have no such escape at all: there the backslash is
	// an ordinary character and the quote after it ends the string.
	script := `INSERT INTO t VALUES ('a\'); SELECT 1`
	if got, want := SplitSQL(script, mysqlish), []string{script}; !slices.Equal(got, want) {
		t.Errorf("mysql: SplitSQL(%q) = %q, want %q", script, got, want)
	}
	want := []string{`INSERT INTO t VALUES ('a\')`, "SELECT 1"}
	for _, o := range []SplitOptions{standard, postgres} {
		if got := SplitSQL(script, o); !slices.Equal(got, want) {
			t.Errorf("%+v: SplitSQL(%q) = %q, want %q", o, script, got, want)
		}
	}
}

func TestSplitSQLBacktickQuotes(t *testing.T) {
	// A backtick quotes an identifier on MySQL, SQLite and ClickHouse; it
	// is an ordinary character everywhere else.
	script := "SELECT `a;b` FROM t"
	if got, want := SplitSQL(script, mysqlish), []string{script}; !slices.Equal(got, want) {
		t.Errorf("mysql: SplitSQL(%q) = %q, want %q", script, got, want)
	}
	want := []string{"SELECT `a", "b` FROM t"}
	if got := SplitSQL(script, standard); !slices.Equal(got, want) {
		t.Errorf("standard: SplitSQL(%q) = %q, want %q", script, got, want)
	}
}

func TestSplitSQLDollarQuotes(t *testing.T) {
	// $tag$ ... $tag$ is a string body on PostgreSQL; elsewhere a dollar
	// sign is an ordinary character, and on MySQL even part of a name.
	script := "CREATE FUNCTION f() RETURNS int AS $body$ BEGIN; RETURN 1; END; $body$ LANGUAGE plpgsql; SELECT 1"
	want := []string{
		"CREATE FUNCTION f() RETURNS int AS $body$ BEGIN; RETURN 1; END; $body$ LANGUAGE plpgsql",
		"SELECT 1",
	}
	if got := SplitSQL(script, postgres); !slices.Equal(got, want) {
		t.Errorf("postgres: SplitSQL(%q) = %q, want %q", script, got, want)
	}
	if got := SplitSQL(script, mysqlish); len(got) != 5 {
		t.Errorf("mysql: SplitSQL(%q) = %q, want 5 statements", script, got)
	}
	plain := "SELECT a$b FROM t; SELECT 1"
	if got, want := SplitSQL(plain, mysqlish), []string{"SELECT a$b FROM t", "SELECT 1"}; !slices.Equal(got, want) {
		t.Errorf("mysql: SplitSQL(%q) = %q, want %q", plain, got, want)
	}
}
