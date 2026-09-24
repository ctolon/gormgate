package gaussdb

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ctolon/gormgate/backends/base"
	"github.com/ctolon/gormgate/backends/postgresql"
	m "github.com/ctolon/gormgate/migrations"
)

func model(t *testing.T, fields m.Fields) *m.Model {
	t.Helper()
	st := m.NewProjectState()
	st.AddModel(&m.ModelState{App: "shop", Name: "Item", Table: "shop_items", Fields: fields})
	apps, err := st.Apps()
	if err != nil {
		t.Fatalf("rendering the state: %v", err)
	}
	return apps.MustModel("shop", "Item")
}

func TestFeaturesDifferFromPostgreSQL(t *testing.T) {
	// openGauss forked PostgreSQL 9.2 and inherits its features, so every
	// difference stands for something the fork was found not to do; a flag
	// that starts or stops differing without being listed here is a change
	// nobody asked for.
	want := map[string]bool{
		// INCLUDE needs the ubtree access method, which is reserved for
		// Ustore tables.
		"SupportsCoveringIndexes": false,
		// UNIQUE ... NULLS NOT DISTINCT is PostgreSQL 15 syntax.
		"SupportsNullsDistinctUniqueConstraints": false,
		// A column whose type was already altered in the same transaction
		// cannot be altered again by a combined ALTER TABLE.
		"SupportsCombinedAlters": false,
		// Only the extensions built into the installation, no DROP
		// EXTENSION, and no CREATE COLLATION.
		"SupportsExtensions": false,
		"SupportsCollations": false,
	}
	got := map[string]bool{}
	v, pg := reflect.ValueOf(Features), reflect.ValueOf(postgresql.Features)
	for i := 0; i < v.NumField(); i++ {
		if v.Field(i).Bool() != pg.Field(i).Bool() {
			got[v.Type().Field(i).Name] = v.Field(i).Bool()
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("features differing from PostgreSQL = %v, want %v", got, want)
	}
}

func TestCheckCompatibility(t *testing.T) {
	const wrongMode = "gormgate: the openGauss database is in DBCOMPATIBILITY 'A' mode; " +
		"gormgate requires 'PG' (PostgreSQL) compatibility, because in the other modes the empty string is NULL " +
		"and NOT NULL columns, defaults and introspection behave differently. " +
		"Recreate the database with: CREATE DATABASE <name> DBCOMPATIBILITY 'PG'"
	cases := []struct{ name, compat, want string }{
		{"PG", "PG", ""},
		// The server pads the value, and the mode is named case
		// insensitively in CREATE DATABASE.
		{"padded", "pg  ", ""},
		// 'A' is the default mode, so this is the message most users who
		// created their database without thinking about it will see.
		{"Oracle", "A", wrongMode},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			conn, err := openFake(t, c.compat)
			if c.want == "" {
				if err != nil {
					t.Fatalf("base.Open on a %q database = %v, want nil", c.compat, err)
				}
				if got := conn.Backend.Vendor; got != "gaussdb" {
					t.Errorf("detected vendor = %q, want %q", got, "gaussdb")
				}
				return
			}
			if err == nil {
				t.Fatalf("base.Open on a %q database returned no error", c.compat)
			}
			if err.Error() != c.want {
				t.Errorf("base.Open = %q, want %q", err, c.want)
			}
		})
	}

	t.Run("unreadable", func(t *testing.T) {
		// A database that cannot even be asked is not silently taken to be
		// in the right mode.
		_, err := openFake(t, "")
		if err == nil {
			t.Fatal("base.Open with a failing compatibility query returned no error")
		}
		const prefix = "gormgate: reading the openGauss compatibility mode of the current database: "
		if !strings.HasPrefix(err.Error(), prefix) {
			t.Errorf("base.Open = %q, want it to start with %q", err, prefix)
		}
	})
}

func TestIntrospectionQueries(t *testing.T) {
	// openGauss forked PostgreSQL 9.2, so the queries it cannot run are
	// replaced. Each case names what the PostgreSQL query uses and the
	// fork does not have.
	i := NewIntrospection(&base.Conn{Backend: Backend})
	cases := []struct {
		name, query, postgres, missing string
	}{
		{"table list", i.TableQuery, postgresql.TableListSQL, "relispartition"},
		{"columns", i.DescriptionQuery, postgresql.ColumnsSQL, "attidentity"},
		{"indexes", i.IndexQuery, postgresql.IndexesSQL, "unnest(i.indkey, i.indoption)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// An empty query means "use PostgreSQL's", which would reach
			// the server with SQL it rejects.
			if c.query == "" {
				t.Fatal("the query is empty, so PostgreSQL's would be used")
			}
			if !strings.Contains(c.postgres, c.missing) {
				t.Fatalf("PostgreSQL's query does not use %q, so there is nothing to replace", c.missing)
			}
			if strings.Contains(c.query, c.missing) {
				t.Errorf("the openGauss query uses %q, which the fork does not have", c.missing)
			}
		})
	}
}

func TestAlterColumnTypeSQLWithoutTypedSequences(t *testing.T) {
	// openGauss has neither CREATE/ALTER SEQUENCE ... AS <type> nor
	// identity columns, so its editor is built with the zero SerialSQL.
	conn, err := openFake(t, "PG")
	if err != nil {
		t.Fatalf("base.Open: %v", err)
	}
	mdl := model(t, m.Fields{
		{Name: "id", Field: m.Field{Type: m.Int, Size: 64, PrimaryKey: true}},
		{Name: "small", Field: m.Field{Type: m.Int, Size: 32, PrimaryKey: true, AutoIncrement: true}},
		{Name: "big", Field: m.Field{Type: m.Int, Size: 64, PrimaryKey: true, AutoIncrement: true}},
	})
	plain, small, big := mdl.Field("id"), mdl.Field("small"), mdl.Field("big")

	t.Run("into a serial column", func(t *testing.T) {
		e := NewEditor(conn, true, false)
		_, other, err := e.AlterColumnTypeSQL(mdl, plain, big, "bigserial")
		if err != nil {
			t.Fatalf("AlterColumnTypeSQL: %v", err)
		}
		want := []string{
			// PostgreSQL would create the sequence "AS bigint".
			`CREATE SEQUENCE IF NOT EXISTS "shop_items_big_seq"`,
			`ALTER TABLE "shop_items" ALTER COLUMN "big" SET DEFAULT nextval('"shop_items_big_seq"')`,
			`ALTER SEQUENCE "shop_items_big_seq" OWNED BY "shop_items"."big"`,
			`SELECT setval('"shop_items_big_seq"', coalesce(max("big"), 0) + 1, false) FROM "shop_items"`,
		}
		if !slices.Equal(other, want) {
			t.Errorf("statements = %q, want %q", other, want)
		}
	})

	t.Run("between two serial widths", func(t *testing.T) {
		// The sequence carries no type, so only the column changes; the
		// ALTER SEQUENCE PostgreSQL emits would be a syntax error.
		e := NewEditor(conn, true, false)
		frag, other, err := e.AlterColumnTypeSQL(mdl, small, big, "bigserial")
		if err != nil {
			t.Fatalf("AlterColumnTypeSQL: %v", err)
		}
		if want := `ALTER COLUMN "big" TYPE bigint USING "big"::bigint`; frag.SQL != want {
			t.Errorf("fragment = %q, want %q", frag.SQL, want)
		}
		if len(other) != 0 {
			t.Errorf("statements = %q, want none", other)
		}
	})

	t.Run("into an identity column", func(t *testing.T) {
		e := NewEditor(conn, true, false)
		_, _, err := e.AlterColumnTypeSQL(mdl, plain, big, "bigint GENERATED BY DEFAULT AS IDENTITY")
		if err == nil {
			t.Fatal("altering a column into an identity returned no error")
		}
		want := "openGauss has no identity columns; the field shop.Item.big cannot be altered into or out of one"
		if err.Error() != want {
			t.Errorf("AlterColumnTypeSQL = %q, want %q", err, want)
		}
	})
}
