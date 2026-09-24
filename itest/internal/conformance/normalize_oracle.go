package conformance

import (
	"regexp"
	"strings"
)

func init() { Normalizers["oracle"] = oracle }

// iseq matches the default of an identity column, which names the sequence
// Oracle created for it: `"GG_7773E0368EC1"."ISEQ$$_73373".nextval`. Both
// the schema and the sequence number are per-database.
var iseq = regexp.MustCompile(`"[^"]+"\."ISEQ\$\$_\d+"\.nextval`)

// oracle normalizes the names Oracle generates itself. A constraint
// created without a name - the primary key of a CREATE TABLE and the check
// constraint behind every NOT NULL column attribute - is called SYS_Cnnnnn,
// and the number is a per-database counter, so two databases never agree on
// it. The same holds for the sequence of an identity column.
var oracle = &Normalizer{
	Constraint: func(table, name string, c Constraint) (string, bool) {
		if !strings.HasPrefix(name, "SYS_C") {
			return name, true
		}
		switch {
		case c.PrimaryKey:
			return "<pkey>", true
		case c.Check:
			// Oracle catalogues the NOT NULL attribute of a column as a
			// check constraint with a generated name, but only for some
			// columns: an identity column is implicitly NOT NULL and gets
			// no such constraint, so a column that loses its identity keeps
			// nullable = 'N' without one. The attribute itself is compared
			// as Column.Null, which makes this entry redundant.
			return "", false
		}
		return name, true
	},
	Column: func(table, name string, c Column) Column {
		c.Default = iseq.ReplaceAllLiteralString(c.Default, "<identity sequence>.nextval")
		return c
	},
}
