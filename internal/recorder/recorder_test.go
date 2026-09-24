package recorder

import (
	"reflect"
	"testing"
	"time"

	"github.com/ctolon/gormgate/backends/base"
	m "github.com/ctolon/gormgate/migrations"
)

func TestModelStateStringSize(t *testing.T) {
	// gorm maps a string field with a size to FixedString(n) on
	// ClickHouse, which pads every stored value with NUL bytes to the full
	// width; the recorder compares what it reads back with the names of
	// the migrations on disk, which would then never match.
	cases := []struct {
		name string
		f    base.Features
		want int
	}{
		{"a backend that pads sized strings", base.Features{PadsSizedStrings: true}, 0},
		{"a backend that does not", base.Features{}, 255},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := ModelState(c.f)
			for _, name := range []string{"app", "name"} {
				f, ok := st.Fields.Get(name)
				if !ok {
					t.Fatalf("the migrations table has no %q field", name)
				}
				if f.Size != c.want {
					t.Errorf("%s size = %d, want %d", name, f.Size, c.want)
				}
			}
		})
	}
}

func TestModelState(t *testing.T) {
	st := ModelState(base.Features{})
	if st.Table != Table {
		t.Errorf("table = %q, want %q", st.Table, Table)
	}
	// The key has to be handed out by the database: two commands may
	// record a migration at the same time.
	id, ok := st.Fields.Get("id")
	if !ok || !id.PrimaryKey || !id.AutoIncrement {
		t.Errorf("id field = %+v, want an auto-incrementing primary key", id)
	}
	if applied, ok := st.Fields.Get("applied"); !ok || applied.Type != m.Time {
		t.Errorf("applied field = %+v, want a time field", applied)
	}
	// A ClickHouse table needs an engine and a sorting key, whatever the
	// vendor of the connection is: the options travel with the state.
	ch := st.Options.ClickHouse
	if ch == nil || ch.Engine != "MergeTree()" || ch.OrderBy != "(app, name)" {
		t.Errorf("ClickHouse options = %+v, want MergeTree() ordered by (app, name)", ch)
	}
}

func TestModel(t *testing.T) {
	mdl := Model(base.Features{})
	if mdl.Table != Table {
		t.Errorf("table = %q, want %q", mdl.Table, Table)
	}
	var columns []string
	for _, f := range mdl.Fields {
		columns = append(columns, f.Column)
	}
	if want := []string{"id", "app", "name", "applied"}; !reflect.DeepEqual(columns, want) {
		t.Errorf("columns = %q, want %q", columns, want)
	}
	if got := Model(base.Features{PadsSizedStrings: true}).Field("name").Field.Size; got != 0 {
		t.Errorf("padded-string name size = %d, want 0", got)
	}
}

func TestRecordApplied(t *testing.T) {
	cases := []struct {
		vendor string
		want   string
	}{
		{"postgresql", `INSERT INTO "gormgate_migrations" ("app", "name", "applied") VALUES (?, ?, ?)`},
		// ClickHouse has no auto-incrementing column, so the key is read
		// off the table the row is inserted into.
		{"clickhouse", "INSERT INTO `gormgate_migrations` (`id`, `app`, `name`, `applied`) " +
			"SELECT coalesce(max(`id`), 0) + 1, ?, ?, ? FROM `gormgate_migrations`"},
	}
	for _, c := range cases {
		t.Run(c.vendor, func(t *testing.T) {
			conn, fdb := openFake(t, c.vendor)
			before := time.Now().UTC()
			if err := New(conn).RecordApplied("shop", "0001_initial"); err != nil {
				t.Fatalf("RecordApplied: %v", err)
			}
			st := fdb.only(t)
			if st.sql != c.want {
				t.Errorf("statement = %q, want %q", st.sql, c.want)
			}
			if len(st.args) != 3 {
				t.Fatalf("arguments = %v, want 3", st.args)
			}
			if st.args[0] != "shop" || st.args[1] != "0001_initial" {
				t.Errorf("arguments = %v, want the app and the migration name", st.args[:2])
			}
			applied, ok := st.args[2].(time.Time)
			if !ok {
				t.Fatalf("applied argument is %T, want time.Time", st.args[2])
			}
			// The timestamp is written in UTC, so that records made in
			// different time zones can be compared.
			if applied.Before(before) || applied.Location() != time.UTC {
				t.Errorf("applied = %v, want a UTC time at or after %v", applied, before)
			}
		})
	}
}

func TestRecordUnapplied(t *testing.T) {
	cases := []struct {
		vendor string
		want   string
	}{
		{"postgresql", `DELETE FROM "gormgate_migrations" WHERE "app" = ? AND "name" = ?`},
		// A ClickHouse DELETE is a mutation that is applied in the
		// background; without the setting the row can still be there when
		// the migration that follows reads the table.
		{"clickhouse", "DELETE FROM `gormgate_migrations` WHERE `app` = ? AND `name` = ? SETTINGS mutations_sync = 2"},
	}
	for _, c := range cases {
		t.Run(c.vendor, func(t *testing.T) {
			conn, fdb := openFake(t, c.vendor)
			if err := New(conn).RecordUnapplied("shop", "0001_initial"); err != nil {
				t.Fatalf("RecordUnapplied: %v", err)
			}
			st := fdb.only(t)
			if st.sql != c.want {
				t.Errorf("statement = %q, want %q", st.sql, c.want)
			}
			want := []any{"shop", "0001_initial"}
			if len(st.args) != 2 || st.args[0] != want[0] || st.args[1] != want[1] {
				t.Errorf("arguments = %v, want %v", st.args, want)
			}
		})
	}
}

func TestRecords(t *testing.T) {
	conn, fdb := openFake(t, "postgresql")
	fdb.rows = []Record{
		{ID: 1, App: "shop", Name: "0001_initial", Applied: day},
		{ID: 2, App: "blog", Name: "0001_initial", Applied: day},
	}
	r := New(conn)
	got, err := r.Records()
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	// The rows are read in recording order, which is the order the
	// migrations have to be unapplied in.
	want := `SELECT "id", "app", "name", "applied" FROM "gormgate_migrations" ORDER BY "id"`
	if st := fdb.only(t); st.sql != want {
		t.Errorf("query = %q, want %q", st.sql, want)
	}
	if !reflect.DeepEqual(got, fdb.rows) {
		t.Errorf("records = %+v, want %+v", got, fdb.rows)
	}

	keys, err := r.AppliedMigrations()
	if err != nil {
		t.Fatalf("AppliedMigrations: %v", err)
	}
	wantKeys := []m.Key{{App: "shop", Name: "0001_initial"}, {App: "blog", Name: "0001_initial"}}
	if !reflect.DeepEqual(keys, wantKeys) {
		t.Errorf("applied migrations = %v, want %v", keys, wantKeys)
	}
}

func TestRecordsWithoutTable(t *testing.T) {
	// Before the first migration is applied there is no table, and asking
	// for the records is not an error: every migration is unapplied.
	conn, fdb := openFakeTables(t, "postgresql", nil)
	got, err := New(conn).Records()
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	if got != nil {
		t.Errorf("records = %+v, want none", got)
	}
	if sent := fdb.sent(); len(sent) != 0 {
		t.Errorf("statements = %v, want none", sent)
	}
}

func TestHasTable(t *testing.T) {
	// Oracle stores an unquoted name in upper case, so the name the
	// introspection reports is compared through its identifier converter.
	conn, fdb := openFakeTables(t, "oracle", []base.TableInfo{{Name: "GORMGATE_MIGRATIONS", Type: "t"}})
	fdb.upper = true
	r := New(conn)
	ok, err := r.HasTable()
	if err != nil {
		t.Fatalf("HasTable: %v", err)
	}
	if !ok {
		t.Error("HasTable = false, want true: the table is there under the name the database folded it to")
	}
	// A true answer is cached: the table cannot go away within a command,
	// and the lookup is a query.
	if _, err := r.HasTable(); err != nil {
		t.Fatalf("HasTable: %v", err)
	}
	if fdb.tableLookup != 1 {
		t.Errorf("the tables were listed %d times, want 1", fdb.tableLookup)
	}
}

func TestHasTableMissing(t *testing.T) {
	// A false answer is not cached: EnsureSchema creates the table and the
	// next question has to see it.
	conn, fdb := openFakeTables(t, "postgresql", []base.TableInfo{{Name: "shop_items", Type: "t"}})
	r := New(conn)
	for i := range 2 {
		ok, err := r.HasTable()
		if err != nil {
			t.Fatalf("HasTable: %v", err)
		}
		if ok {
			t.Fatalf("HasTable = true, want false (call %d)", i+1)
		}
	}
	if fdb.tableLookup != 2 {
		t.Errorf("the tables were listed %d times, want 2", fdb.tableLookup)
	}
}
