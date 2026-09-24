package conformance

import "fmt"

func init() {
	for _, key := range []string{"mysql80", "mysql84", "mariadb106", "mariadb118", "tidb"} {
		if Normalizers[key] == nil {
			Normalizers[key] = &Normalizer{}
		}
		Normalizers[key].Table = canonicalizeForeignKeyIndexes
	}
}

// canonicalizeForeignKeyIndexes hides where MySQL keeps the index a
// foreign key needs. Creating the key without an index of its own makes
// MySQL create one named after the constraint (reported on the constraint
// entry); dropping a user index that the key was using makes gormgate
// recreate one under a generated name (Django's behaviour). Both shapes
// mean "this foreign key is indexed on these columns", so both are
// reduced to one entry.
func canonicalizeForeignKeyIndexes(table string, t Table) Table {
	for name, c := range t.Constraints {
		if c.ForeignKey == "" {
			continue
		}
		canonical := fmt.Sprintf("<fk index %s>", c.Columns)
		if c.Index {
			c.Index = false
			c.Orders = ""
			t.Constraints[name] = c
			t.Constraints[canonical] = Constraint{Columns: c.Columns, Index: true}
			continue
		}
		for other, oc := range t.Constraints {
			if other == name || !oc.Index || oc.Unique || oc.Columns != c.Columns {
				continue
			}
			delete(t.Constraints, other)
			t.Constraints[canonical] = Constraint{Columns: c.Columns, Index: true}
		}
	}
	return t
}
