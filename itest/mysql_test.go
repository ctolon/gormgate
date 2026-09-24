//go:build integration

package itest

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ctolon/gormgate/itest/internal/dbtest"
	"github.com/ctolon/gormgate/itest/internal/harness"
	m "github.com/ctolon/gormgate/migrations"
)

// mysqlKeys are the vendor keys served by the MySQL and TiDB backends.
var mysqlKeys = []string{"mysql80", "mysql84", "mariadb106", "mariadb118", "tidb"}

// mysqlFamilyUnderTest intersects mysqlKeys with the vendors under test.
func mysqlFamilyUnderTest(t *testing.T) []string {
	wanted := map[string]bool{}
	for _, k := range vendorsUnderTest(t) {
		wanted[k] = true
	}
	var out []string
	for _, k := range mysqlKeys {
		if wanted[k] {
			out = append(out, k)
		}
	}
	return out
}

// indexNames returns the names of the indexes of a table whose first column
// is the given one.
func indexNames(t *testing.T, p *harness.Project, table, column string) []string {
	t.Helper()
	cons, err := p.Conn.Introspection().Constraints(table)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for name, c := range cons {
		if c.Index && len(c.Columns) > 0 && c.Columns[0] == column {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}

// fkSetup is a Pony with a Rider referencing it, plus a plain column the
// unique_together test extends the index with.
func fkSetup(app, prefix string) *m.Migration {
	return &m.Migration{App: app, Name: "0001_setup", Operations: []m.Operation{
		&m.CreateModel{Name: "Pony", Table: prefix + "pony", Fields: m.Fields{
			{Name: "id", Field: m.Field{Type: m.Int, Size: 64, PrimaryKey: true, AutoIncrement: true}},
		}},
		&m.CreateModel{Name: "Rider", Table: prefix + "rider", Fields: m.Fields{
			{Name: "id", Field: m.Field{Type: m.Int, Size: 64, PrimaryKey: true, AutoIncrement: true}},
			{Name: "pony_id", Field: m.Field{Type: m.Int, Size: 64, Null: true,
				ForeignKey: &m.ForeignKey{To: "Pony", ToField: "id", Name: "fk_" + prefix + "rider_pony"}}},
			{Name: "name", Field: m.Field{Type: m.String, Size: 50, Null: true}},
		}},
	}}
}

// TestMySQLForeignKeyIndexDropped checks that the index MySQL creates on its
// own for a foreign key goes away with the constraint: a table created from
// scratch without the foreign key has no such index.
func TestMySQLForeignKeyIndexDropped(t *testing.T) {
	for _, key := range mysqlFamilyUnderTest(t) {
		t.Run(key, func(t *testing.T) {
			db, _ := dbtest.Open(t, key)
			p := harness.New(t, db, "fkd")
			setup := fkSetup("fkd", "fkd_")
			plain := &m.Migration{App: "fkd", Name: "0002_plain", Dependencies: []m.Key{setup.Key()},
				Operations: []m.Operation{&m.AlterField{ModelName: "rider", Name: "pony_id",
					Field: m.Field{Type: m.Int, Size: 64, Null: true}}}}
			p.Register(setup, plain)
			p.MustMigrate(setup.Key())
			want := "fk_fkd_rider_pony"
			if got := indexNames(t, p, "fkd_rider", "pony_id"); len(got) != 1 || got[0] != want {
				t.Fatalf("indexes on the foreign key column: %v, want [%s]", got, want)
			}
			p.MustMigrate(plain.Key())
			if got := indexNames(t, p, "fkd_rider", "pony_id"); len(got) != 0 {
				t.Fatalf("indexes left after dropping the foreign key: %v, want none", got)
			}
			// Restoring the foreign key restores its index.
			p.MustMigrate(setup.Key())
			if got := indexNames(t, p, "fkd_rider", "pony_id"); len(got) != 1 || got[0] != want {
				t.Fatalf("indexes after restoring the foreign key: %v, want [%s]", got, want)
			}
			p.MustMigrate(m.Key{App: "fkd", Name: ""})
		})
	}
}

// TestMySQLMissingForeignKeyIndex checks Django's _create_missing_fk_index:
// MySQL drops the implicit index of a foreign key once a wider index starts
// with the same column, so that wider index may only be dropped after the
// foreign key has an index of its own again.
func TestMySQLMissingForeignKeyIndex(t *testing.T) {
	for _, key := range mysqlFamilyUnderTest(t) {
		t.Run(key, func(t *testing.T) {
			db, _ := dbtest.Open(t, key)
			p := harness.New(t, db, "fkm")
			setup := fkSetup("fkm", "fkm_")
			together := &m.Migration{App: "fkm", Name: "0002_together", Dependencies: []m.Key{setup.Key()},
				Operations: []m.Operation{&m.AlterUniqueTogether{Name: "rider",
					UniqueTogether: [][]string{{"pony_id", "name"}}}}}
			p.Register(setup, together)
			p.MustMigrate(together.Key())
			// Dropping the unique_together must leave the foreign key with an
			// index; without the repair MySQL refuses the drop (errno 1553).
			p.MustMigrate(setup.Key())
			if got := indexNames(t, p, "fkm_rider", "pony_id"); len(got) != 1 {
				t.Fatalf("indexes on the foreign key column after dropping the unique_together: %v, want exactly one", got)
			}
			p.MustMigrate(m.Key{App: "fkm", Name: ""})
		})
	}
}

// TestTiDBClusteredPrimaryKey checks that TiDB refuses to change the type of
// a clustered integer primary key with an explanation instead of failing
// halfway through a migration it cannot roll back.
func TestTiDBClusteredPrimaryKey(t *testing.T) {
	for _, key := range mysqlFamilyUnderTest(t) {
		t.Run(key, func(t *testing.T) {
			db, _ := dbtest.Open(t, key)
			p := harness.New(t, db, "pk")
			setup := &m.Migration{App: "pk", Name: "0001_setup", Operations: []m.Operation{
				&m.CreateModel{Name: "Pony", Table: "pk_pony", Fields: m.Fields{
					{Name: "id", Field: m.Field{Type: m.Int, Size: 32, PrimaryKey: true}},
				}},
			}}
			widen := &m.Migration{App: "pk", Name: "0002_widen", Dependencies: []m.Key{setup.Key()},
				Operations: []m.Operation{&m.AlterField{ModelName: "pony", Name: "id",
					Field: m.Field{Type: m.Int, Size: 64, PrimaryKey: true}}}}
			p.Register(setup, widen)
			p.MustMigrate(setup.Key())
			err := p.Migrate(widen.Key())
			if !p.Conn.Backend.Features.CannotAlterIntegerPrimaryKeyType {
				if err != nil {
					t.Fatalf("widening the primary key: %v", err)
				}
				p.MustMigrate(m.Key{App: "pk", Name: ""})
				return
			}
			var notSupported *m.NotSupportedError
			if !errors.As(err, &notSupported) {
				t.Fatalf("widening the clustered primary key: %v, want a NotSupportedError", err)
			}
			if !strings.Contains(notSupported.Msg, "clustered integer primary key") {
				t.Fatalf("error message %q does not name the limitation", notSupported.Msg)
			}
			p.MustMigrate(setup.Key())
			p.MustMigrate(m.Key{App: "pk", Name: ""})
		})
	}
}
