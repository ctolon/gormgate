package conformance

import (
	"regexp"
	"strings"
)

// Normalizers holds per-vendor-key snapshot normalizers; vendors without an
// entry compare introspection output verbatim.
var Normalizers = map[string]*Normalizer{
	"pg14":      postgres,
	"pg18":      postgres,
	"cockroach": postgres,
	"gauss":     postgres,
}

var nextval = regexp.MustCompile(`nextval\('[^']*'::regclass\)`)

// postgres normalizes the names PostgreSQL derives from table and column
// names: "<table>_pkey", "<table>_<column>_not_null" (PostgreSQL 18) and
// the "<table>_<column>_seq" sequence behind serial columns. Renaming a
// table or column keeps them, while a fresh CREATE TABLE derives new ones.
var postgres = &Normalizer{
	Constraint: func(table, name string, c Constraint) (string, bool) {
		switch {
		case c.PrimaryKey && strings.HasSuffix(name, "_pkey"):
			return "<pkey>", true
		case strings.HasSuffix(name, "_not_null") && !c.PrimaryKey && !c.Unique && !c.Index && c.ForeignKey == "":
			return "<not null " + c.Columns + ">", true
		}
		return name, true
	},
	Column: func(table, name string, c Column) Column {
		c.Default = nextval.ReplaceAllString(c.Default, "nextval(<sequence>)")
		c.Default = stripCasts(c.Default)
		return c
	},
}
