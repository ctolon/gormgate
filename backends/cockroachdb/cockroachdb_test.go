package cockroachdb

import (
	"reflect"
	"slices"
	"testing"

	"github.com/ctolon/gormgate/backends/base"
	"github.com/ctolon/gormgate/backends/postgresql"
	m "github.com/ctolon/gormgate/migrations"
)

// editor returns an editor on a connection that carries nothing but the
// backend: everything it is asked here renders SQL or refuses to, and
// never reaches a server.
func editor(t *testing.T) *Editor {
	t.Helper()
	return NewEditor(&base.Conn{Backend: Backend}, true, false)
}

var itemFields = m.Fields{
	{Name: "id", Field: m.Field{Type: m.Int, Size: 64, PrimaryKey: true, AutoIncrement: true}},
	{Name: "code", Field: m.Field{Type: m.String, Size: 20}},
	{Name: "name", Field: m.Field{Type: m.String}},
}

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

func TestServerVersion(t *testing.T) {
	cases := []struct {
		name         string
		in           string
		major, minor int
		ok           bool
	}{
		{"ccl", "CockroachDB CCL v25.2.23 (x86_64-pc-linux-gnu, built 2025/09/02 17:01:28, go1.23.12)", 25, 2, true},
		{"oss", "CockroachDB OSS v24.1.0 (x86_64-pc-linux-gnu)", 24, 1, true},
		// The PostgreSQL server the wire protocol imitates must not be
		// read as a CockroachDB version: the detector hands every
		// "postgres" dialector here first.
		{"postgresql", "PostgreSQL 16.2 on x86_64-pc-linux-gnu", 0, 0, false},
		{"no minor", "CockroachDB CCL v25", 0, 0, false},
		{"no build", "CockroachDB v25.2.23", 0, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			major, minor, ok := ServerVersion(c.in)
			if major != c.major || minor != c.minor || ok != c.ok {
				t.Errorf("ServerVersion(%q) = %d, %d, %v, want %d, %d, %v",
					c.in, major, minor, ok, c.major, c.minor, c.ok)
			}
		})
	}
}

func TestCheckVersion(t *testing.T) {
	cases := []struct{ name, version, want string }{
		{"current", "CockroachDB CCL v25.2.23 (x86_64-pc-linux-gnu)", ""},
		{"minimum", "CockroachDB CCL v24.1.0 (x86_64-pc-linux-gnu)", ""},
		{"older minor", "CockroachDB CCL v24.0.9 (x86_64-pc-linux-gnu)",
			"gormgate: CockroachDB 24.0 is too old; gormgate requires 24.1 or later"},
		{"older major", "CockroachDB CCL v23.2.0 (x86_64-pc-linux-gnu)",
			"gormgate: CockroachDB 23.2 is too old; gormgate requires 24.1 or later"},
		{"unreadable", "CockroachDB", `gormgate: cannot read the CockroachDB version from "CockroachDB"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := CheckVersion(nil, c.version)
			if c.want == "" {
				if err != nil {
					t.Fatalf("CheckVersion(%q) = %v, want nil", c.version, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("CheckVersion(%q) = nil, want %q", c.version, c.want)
			}
			if err.Error() != c.want {
				t.Errorf("CheckVersion(%q) = %q, want %q", c.version, err, c.want)
			}
		})
	}
}

func TestFeaturesDifferFromPostgreSQL(t *testing.T) {
	// CockroachDB inherits PostgreSQL's features, so every difference is a
	// deliberate one; a flag that starts or stops differing without being
	// listed here is a change nobody asked for.
	want := map[string]bool{
		"CanRollbackDDL":                         false,
		"SupportsCombinedAlters":                 false,
		"SupportsDeferrableUniqueConstraints":    false,
		"SupportsNullsDistinctUniqueConstraints": false,
		"NonSequentialAutoIncrement":             true,
		// CREATE EXTENSION is "not yet implemented" and there is no
		// CREATE COLLATION.
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
	// django-cockroachdb turns comments off because reading them is slow;
	// gorm's AutoMigrate emits COMMENT ON, so gormgate keeps them.
	if !Features.SupportsComments {
		t.Error("SupportsComments = false, want true")
	}
}

func TestGrammar(t *testing.T) {
	var g Grammar
	c := base.DropConstraint{Table: "T1", Name: "N"}
	cases := []struct{ name, got, want string }{
		// A unique constraint can only be dropped through its index, and
		// an index name is scoped to its table.
		{"DropUnique", g.DropUnique(c), "DROP INDEX T1@N CASCADE"},
		{"DropIndex", g.DropIndex(base.DropIndex{Table: "T1", Name: "N"}), "DROP INDEX IF EXISTS T1@N"},
		{"DropIndexConcurrently", g.DropIndex(base.DropIndex{Table: "T1", Name: "N", Concurrently: true}), "DROP INDEX CONCURRENTLY IF EXISTS T1@N"},
		{"RenameIndex", g.RenameIndex(base.RenameIndex{Table: "T1", OldName: "O", NewName: "W"}), "ALTER INDEX T1@O RENAME TO W"},
		// Every table has a primary key, so one is replaced, never added.
		{"AddPrimaryKey", g.AddPrimaryKey(base.AddPrimaryKey{Table: "T1", Columns: "C"}), "ALTER TABLE T1 ALTER PRIMARY KEY USING COLUMNS (C)"},
		// PostgreSQL's spelling is the starting point and stays.
		{"DropTable", postgresql.Grammar{}.DropTable(base.DropTable{Table: "T1"}), "DROP TABLE T1 CASCADE"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.got != c.want {
				t.Errorf("%s = %q, want %q", c.name, c.got, c.want)
			}
		})
	}
}

func TestIndexStatements(t *testing.T) {
	e := editor(t)
	mdl := model(t, itemFields)
	if err := e.RemoveIndex(mdl, m.Index{Name: "ix_name", Fields: m.Columns("name")}); err != nil {
		t.Fatalf("RemoveIndex: %v", err)
	}
	if err := e.RenameIndex(mdl, m.Index{Name: "ix_old"}, m.Index{Name: "ix_new"}); err != nil {
		t.Fatalf("RenameIndex: %v", err)
	}
	if got := e.DeleteUniqueSQL(mdl, "uq_code").String(); got != `DROP INDEX "shop_items"@"uq_code" CASCADE` {
		t.Errorf("DeleteUniqueSQL = %q", got)
	}
	want := []string{
		`DROP INDEX IF EXISTS "shop_items"@"ix_name";`,
		`ALTER INDEX "shop_items"@"ix_old" RENAME TO "ix_new";`,
	}
	if got := e.CollectedSQL(); !slices.Equal(got, want) {
		t.Errorf("collected SQL = %q, want %q", got, want)
	}
}

func TestCreateIndexSQLDropsOpClasses(t *testing.T) {
	// CockroachDB has no operator classes; a CREATE INDEX carrying one is
	// rejected outright, so PostgreSQL's opclass suffixes are dropped.
	st, err := editor(t).CreateIndexSQL(model(t, itemFields),
		m.Index{Name: "ix_code", Fields: m.Columns("code"), OpClasses: []string{"varchar_pattern_ops"}})
	if err != nil {
		t.Fatalf("CreateIndexSQL: %v", err)
	}
	if want := `CREATE INDEX "ix_code" ON "shop_items" ("code")`; st.String() != want {
		t.Errorf("CreateIndexSQL = %q, want %q", st.String(), want)
	}
}

func TestCreatePrimaryKeySQL(t *testing.T) {
	// ALTER PRIMARY KEY names no constraint: the key of a table is
	// replaced by the columns it is to consist of.
	got := editor(t).CreatePrimaryKeySQL(model(t, itemFields), []string{"id", "code"}).String()
	if want := `ALTER TABLE "shop_items" ALTER PRIMARY KEY USING COLUMNS ("id", "code")`; got != want {
		t.Errorf("CreatePrimaryKeySQL = %q, want %q", got, want)
	}
}

func TestDeletePrimaryKey(t *testing.T) {
	mdl := model(t, itemFields)

	t.Run("while a new key is installed", func(t *testing.T) {
		// ALTER PRIMARY KEY replaces the old key in the same statement,
		// so the drop has nothing to do.
		e := editor(t)
		e.replacingPK = true
		if err := e.DeletePrimaryKey(mdl, true); err != nil {
			t.Fatalf("DeletePrimaryKey: %v", err)
		}
		if got := e.CollectedSQL(); len(got) != 0 {
			t.Errorf("collected SQL = %q, want none", got)
		}
	})

	t.Run("with a remaining key", func(t *testing.T) {
		e := editor(t)
		e.droppingPK, e.newPK = true, []string{"code"}
		if err := e.DeletePrimaryKey(mdl, true); err != nil {
			t.Fatalf("DeletePrimaryKey: %v", err)
		}
		want := []string{`ALTER TABLE "shop_items" ALTER PRIMARY KEY USING COLUMNS ("code");`}
		if got := e.CollectedSQL(); !slices.Equal(got, want) {
			t.Errorf("collected SQL = %q, want %q", got, want)
		}
	})

	t.Run("without a replacement", func(t *testing.T) {
		// Leaving the table without a primary key is what CockroachDB
		// cannot do, and the message has to say so rather than let the
		// server fail halfway through a migration it cannot roll back.
		err := editor(t).DeletePrimaryKey(mdl, true)
		if err == nil {
			t.Fatal("DeletePrimaryKey without a replacement key returned no error")
		}
		want := "gormgate: CockroachDB cannot drop the primary key of shop_items without installing a new one: " +
			"every table has a primary key and dropping one is unimplemented (go.crdb.dev/issue/48026)"
		if err.Error() != want {
			t.Errorf("DeletePrimaryKey = %q, want %q", err, want)
		}
	})
}

func TestUsingSQL(t *testing.T) {
	cases := []struct {
		name             string
		oldType, newType string
		want             string
	}{
		// Every integer width is the same 64-bit value on disk, and a
		// USING clause would turn the change into a rewrite, which
		// CockroachDB refuses for an indexed column.
		{"integer widths", "integer", "bigint", ""},
		{"smallint to bigint", "smallint", "bigint", ""},
		{"varchar widths", "varchar(20)", "varchar(40)", ""},
		{"text to integer", "text", "bigint", ` USING "c"::bigint`},
		{"integer to text", "bigint", "text", ` USING "c"::text`},
		{"integer to timestamp", "bigint", "timestamptz", ` USING "c"::timestamptz`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := usingSQL(`"c"`, c.oldType, c.newType); got != c.want {
				t.Errorf("usingSQL(%q, %q) = %q, want %q", c.oldType, c.newType, got, c.want)
			}
		})
	}
}

func TestSequenceResetSQL(t *testing.T) {
	// A serial column is unique_rowid(), not a sequence, so
	// sqlsequencereset has nothing to print.
	got, err := SequenceResetSQL(nil, base.PlainStyle{}, nil)
	if err != nil || got != nil {
		t.Errorf("SequenceResetSQL = %q, %v, want nil, nil", got, err)
	}
}

func TestDetect(t *testing.T) {
	// CockroachDB and PostgreSQL share the "postgres" dialector and are
	// told apart by the version string alone, so the two detectors have to
	// be ordered and to exclude each other.
	cases := []struct {
		name, version string
		want          base.Vendor
	}{
		{"cockroachdb", "CockroachDB CCL v25.2.23 (x86_64-pc-linux-gnu)", base.CockroachDB},
		{"postgresql", "PostgreSQL 16.2 on x86_64-pc-linux-gnu", base.PostgreSQL},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			conn, _ := openFake(t, c.version)
			if got := conn.Backend.Vendor; got != c.want {
				t.Errorf("detected vendor = %q, want %q", got, c.want)
			}
		})
	}

	t.Run("too old", func(t *testing.T) {
		// A server below the minimum version is rejected while the
		// connection is opened, not when a migration first fails.
		_, _, err := tryOpenFake(t, "CockroachDB CCL v23.2.0 (x86_64-pc-linux-gnu)")
		if err == nil {
			t.Fatal("base.Open on CockroachDB 23.2 returned no error")
		}
		if want := "gormgate: CockroachDB 23.2 is too old; gormgate requires 24.1 or later"; err.Error() != want {
			t.Errorf("base.Open = %q, want %q", err, want)
		}
	})
}

func TestInitConnectionState(t *testing.T) {
	// Without the setting, an ALTER COLUMN TYPE that rewrites the column
	// is refused as experimental on servers before v25.1.
	_, fdb := openFake(t, "CockroachDB CCL v25.2.23 (x86_64-pc-linux-gnu)")
	want := "SET enable_experimental_alter_column_type_general = true"
	if !slices.Contains(fdb.statements(), want) {
		t.Errorf("statements run on connect = %q, want one of them to be %q", fdb.statements(), want)
	}
}

func TestAlterColumnTypeSQL(t *testing.T) {
	// A serial column is an INT8 whose default is unique_rowid(), so a
	// field that becomes (or stops being) auto-incrementing only changes
	// that default: there is no sequence to create, own or drop, and the
	// column type itself does not change.
	field := func(name string, f m.Field) *m.ModelField {
		mdl := model(t, m.Fields{{Name: name, Field: f}})
		return mdl.Field(name)
	}
	serial := m.Field{Type: m.Int, Size: 64, PrimaryKey: true, AutoIncrement: true}
	plain := m.Field{Type: m.Int, Size: 64, PrimaryKey: true}

	cases := []struct {
		name     string
		old, new *m.ModelField
		wantFrag string
		wantSQL  []string
	}{
		{
			"into a serial column", field("id", plain), field("id", serial), "",
			[]string{`ALTER TABLE "shop_items" ALTER COLUMN "id" SET DEFAULT unique_rowid()`},
		},
		{
			"out of a serial column", field("id", serial), field("id", plain), "",
			[]string{`ALTER TABLE "shop_items" ALTER COLUMN "id" DROP DEFAULT`},
		},
		{
			// Two integer widths are the same value on disk, so the type
			// change carries no USING cast that would force a rewrite.
			"integer widths",
			field("id", m.Field{Type: m.Int, Size: 32}), field("id", m.Field{Type: m.Int, Size: 64}),
			`ALTER COLUMN "id" TYPE bigint`, nil,
		},
		{
			"string to integer",
			field("code", m.Field{Type: m.String, Size: 20}), field("code", m.Field{Type: m.Int, Size: 64}),
			`ALTER COLUMN "code" TYPE bigint USING "code"::bigint`, nil,
		},
		{
			// Only the parameters of the type change, which keeps the
			// on-disk representation.
			"string widths",
			field("code", m.Field{Type: m.String, Size: 20}), field("code", m.Field{Type: m.String, Size: 40}),
			`ALTER COLUMN "code" TYPE varchar(40)`, nil,
		},
		{"unchanged", field("id", plain), field("id", plain), "", nil},
	}
	conn, _ := openFake(t, "CockroachDB CCL v25.2.23 (x86_64-pc-linux-gnu)")
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := NewEditor(conn, true, false)
			newType, err := e.ColumnType(c.new.Model, c.new)
			if err != nil {
				t.Fatalf("ColumnType: %v", err)
			}
			frag, other, err := e.AlterColumnTypeSQL(c.new.Model, c.old, c.new, newType)
			if err != nil {
				t.Fatalf("AlterColumnTypeSQL: %v", err)
			}
			if frag.SQL != c.wantFrag {
				t.Errorf("fragment = %q, want %q", frag.SQL, c.wantFrag)
			}
			if !slices.Equal(other, c.wantSQL) {
				t.Errorf("other statements = %q, want %q", other, c.wantSQL)
			}
		})
	}
}
