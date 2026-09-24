package tidb

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/ctolon/gormgate/backends/base"
	"github.com/ctolon/gormgate/backends/mysql"
	m "github.com/ctolon/gormgate/migrations"
)

func server(version mysql.Version, checks bool) Server {
	return Server{
		Version:          version,
		CheckConstraints: checks,
		MySQL:            mysql.Server{Version: mysql.Version{8, 0, 11}, StorageEngine: "InnoDB"},
	}
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

func TestParseVersion(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want mysql.Version
		ok   bool
	}{
		// The MySQL version TiDB reports compatibility with (8.0.11) is
		// not the TiDB version, and the features hang off the latter.
		{"tidb", "8.0.11-TiDB-v8.5.1", mysql.Version{8, 5, 1}, true},
		{"three digit patch", "8.0.11-TiDB-v6.6.10", mysql.Version{6, 6, 10}, true},
		{"mysql", "8.4.3", mysql.Version{}, false},
		{"mariadb", "10.6.21-MariaDB-ubu2004", mysql.Version{}, false},
		{"no patch", "8.0.11-TiDB-v8.5", mysql.Version{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := ParseVersion(c.in)
			if got != c.want || ok != c.ok {
				t.Errorf("ParseVersion(%q) = %v, %v, want %v, %v", c.in, got, ok, c.want, c.ok)
			}
		})
	}
}

func TestFeaturesForDifferFromMySQL(t *testing.T) {
	// TiDB speaks MySQL's dialect and inherits its features, so every
	// difference stands for something TiDB was found to do differently.
	cases := []struct {
		name string
		s    Server
		want map[string]bool
	}{
		{
			"current", server(mysql.Version{8, 5, 1}, true),
			map[string]bool{
				// TiDB has functional indexes, which the MySQL 8.0.11 this
				// server reports compatibility with does not.
				"SupportsExpressionIndexes": true,
				// DESC is accepted and then stored ascending.
				"SupportsIndexColumnOrdering": false,
				// The primary key is the row key itself.
				"CannotAlterIntegerPrimaryKeyType": true,
				// tidb_enable_check_constraint is on, and MySQL 8.0.11 is
				// below the 8.0.16 that has check constraints.
				"SupportsTableCheckConstraints": true,
			},
		},
		{
			// A CHECK constraint is parsed and silently dropped while the
			// variable is off, so gormgate must not write one.
			"without check constraints", server(mysql.Version{8, 5, 1}, false),
			map[string]bool{
				"SupportsExpressionIndexes":        true,
				"SupportsIndexColumnOrdering":      false,
				"CannotAlterIntegerPrimaryKeyType": true,
			},
		},
		{
			// Foreign keys arrived in TiDB 6.6; before it the clauses are
			// accepted and ignored.
			"before foreign keys", server(mysql.Version{6, 5, 0}, true),
			map[string]bool{
				"SupportsExpressionIndexes":        true,
				"SupportsIndexColumnOrdering":      false,
				"CannotAlterIntegerPrimaryKeyType": true,
				"SupportsTableCheckConstraints":    true,
				"SupportsForeignKeys":              false,
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := map[string]bool{}
			v, my := reflect.ValueOf(FeaturesFor(c.s)), reflect.ValueOf(mysql.FeaturesFor(c.s.MySQL))
			for i := 0; i < v.NumField(); i++ {
				if v.Field(i).Bool() != my.Field(i).Bool() {
					got[v.Type().Field(i).Name] = v.Field(i).Bool()
				}
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("features differing from MySQL = %v, want %v", got, c.want)
			}
		})
	}
}

func TestNewIntrospectionCheckConstraintQuery(t *testing.T) {
	// information_schema.check_constraints has no table_name column on
	// TiDB, so MySQL's query cannot be used; and while the variable is off
	// there are no check constraints to read at all.
	conn := &base.Conn{Backend: BackendFor(server(mysql.Version{8, 5, 1}, true))}
	if got := NewIntrospection(conn, server(mysql.Version{8, 5, 1}, false)).CheckConstraintQuery; got != "" {
		t.Errorf("CheckConstraintQuery without check constraints = %q, want %q", got, "")
	}
	got := NewIntrospection(conn, server(mysql.Version{8, 5, 1}, true)).CheckConstraintQuery
	if !strings.Contains(got, "information_schema.tidb_check_constraints") {
		t.Errorf("CheckConstraintQuery = %q, want it to read information_schema.tidb_check_constraints", got)
	}
	if got == mysql.NewIntrospection(conn, server(mysql.Version{8, 5, 1}, true).MySQL).CheckConstraintQuery {
		t.Error("CheckConstraintQuery is MySQL's, which has no table_name column on TiDB")
	}
}

func TestBackendFor(t *testing.T) {
	s := server(mysql.Version{8, 5, 1}, true)
	b := BackendFor(s)
	if got, want := b.Vendor, base.TiDB; got != want {
		t.Errorf("Vendor = %q, want %q", got, want)
	}
	if got, want := b.DisplayName, "TiDB"; got != want {
		t.Errorf("DisplayName = %q, want %q", got, want)
	}
	if got, want := b.MaxNameLength, mysql.MaxNameLength; got != want {
		t.Errorf("MaxNameLength = %d, want %d", got, want)
	}
	// The same server must come back from the cache, not be rebuilt:
	// callers compare backend pointers.
	if again := BackendFor(s); again != b {
		t.Error("BackendFor returned a different backend for the same server")
	}
	// Two servers differing only in a setting are two backends, since the
	// setting decides a feature.
	if other := BackendFor(server(mysql.Version{8, 5, 1}, false)); other == b {
		t.Error("BackendFor returned the same backend for servers with different settings")
	}
}

func TestIsClusteredIntegerPK(t *testing.T) {
	single := m.Fields{
		{Name: "id", Field: m.Field{Type: m.Int, Size: 64, PrimaryKey: true, AutoIncrement: true}},
		{Name: "name", Field: m.Field{Type: m.String, Size: 20}},
	}
	unsigned := m.Fields{{Name: "id", Field: m.Field{Type: m.Uint, Size: 64, PrimaryKey: true}}}
	stringPK := m.Fields{{Name: "code", Field: m.Field{Type: m.String, Size: 20, PrimaryKey: true}}}
	composite := m.Fields{
		{Name: "id", Field: m.Field{Type: m.Int, Size: 64, PrimaryKey: true}},
		{Name: "code", Field: m.Field{Type: m.String, Size: 20, PrimaryKey: true}},
	}
	cases := []struct {
		name   string
		fields m.Fields
		field  string
		want   bool
	}{
		{"single integer key", single, "id", true},
		{"unsigned", unsigned, "id", true},
		{"ordinary column", single, "name", false},
		// A non-integer key is stored as an ordinary clustered key whose
		// column can still be altered.
		{"string key", stringPK, "code", false},
		// A composite key is not the row key of an integer column.
		{"composite key", composite, "id", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mdl := model(t, c.fields)
			if got := isClusteredIntegerPK(mdl, mdl.Field(c.field)); got != c.want {
				t.Errorf("isClusteredIntegerPK(%s) = %v, want %v", c.field, got, c.want)
			}
		})
	}
}

func TestAlterColumnTypeSQLRejectsClusteredIntegerPK(t *testing.T) {
	conn := openFake(t, "8.0.11-TiDB-v8.5.1", true)
	mdl := model(t, m.Fields{
		{Name: "id", Field: m.Field{Type: m.Int, Size: 64, PrimaryKey: true}},
		{Name: "name", Field: m.Field{Type: m.String, Size: 20}},
	})
	narrow := model(t, m.Fields{
		{Name: "id", Field: m.Field{Type: m.Int, Size: 32, PrimaryKey: true}},
		{Name: "name", Field: m.Field{Type: m.String, Size: 40}},
	})
	s, err := ReadServer(conn, conn.Version)
	if err != nil {
		t.Fatalf("ReadServer: %v", err)
	}
	e := NewEditor(conn, s, true, false)

	// The message is the whole API here: the alteration is refused before
	// anything runs, so it has to say what cannot be done and what to do
	// instead.
	_, _, err = e.AlterColumnTypeSQL(mdl, mdl.Field("id"), narrow.Field("id"), "int")
	if err == nil {
		t.Fatal("changing the type of a clustered integer primary key returned no error")
	}
	var nse *m.NotSupportedError
	if !errors.As(err, &nse) {
		t.Errorf("error is %T (%v), want *migrations.NotSupportedError", err, err)
	}
	want := `TiDB cannot change the type of shop_items.id from "bigint" to "int": ` +
		`the column is the clustered integer primary key of the table, ` +
		`which MODIFY rejects with "Unsupported modify column: this column has primary key flag" ` +
		`(and, when AUTO_INCREMENT is involved, "can't set auto_increment"). ` +
		`Recreate the model with the new primary key instead.`
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err, want)
	}

	t.Run("unchanged type", func(t *testing.T) {
		// A column that keeps its type is not being altered at all, even
		// when the field around it changed.
		frag, _, err := e.AlterColumnTypeSQL(mdl, mdl.Field("id"), mdl.Field("id"), "bigint")
		if err != nil {
			t.Fatalf("AlterColumnTypeSQL: %v", err)
		}
		if want := "MODIFY `id` bigint NOT NULL"; frag.SQL != want {
			t.Errorf("fragment = %q, want %q", frag.SQL, want)
		}
	})

	t.Run("ordinary column", func(t *testing.T) {
		// Only the clustered key is refused; every other column is
		// altered the way MySQL does it.
		frag, _, err := e.AlterColumnTypeSQL(mdl, mdl.Field("name"), narrow.Field("name"), "varchar(40)")
		if err != nil {
			t.Fatalf("AlterColumnTypeSQL: %v", err)
		}
		if want := "MODIFY `name` varchar(40) NOT NULL"; frag.SQL != want {
			t.Errorf("fragment = %q, want %q", frag.SQL, want)
		}
	})
}

func TestDetect(t *testing.T) {
	// TiDB shares the "mysql" dialector with MySQL and MariaDB and is told
	// apart by its version string, so its detector has to run first.
	conn := openFake(t, "8.0.11-TiDB-v8.5.1", true)
	if got, want := conn.Backend.Vendor, base.TiDB; got != want {
		t.Errorf("detected vendor = %q, want %q", got, want)
	}
	if !conn.Backend.Features.SupportsTableCheckConstraints {
		t.Error("SupportsTableCheckConstraints = false, want true: the server has them enabled")
	}
	off := openFake(t, "8.0.11-TiDB-v8.5.1", false)
	if off.Backend.Features.SupportsTableCheckConstraints {
		t.Error("SupportsTableCheckConstraints = true, want false: the server has them disabled")
	}
	plain := openFake(t, "8.4.3", true)
	if got, want := plain.Backend.Vendor, base.MySQL; got != want {
		t.Errorf("detected vendor of a MySQL server = %q, want %q", got, want)
	}
}

func TestTemplatesAreMySQLs(t *testing.T) {
	// TiDB writes MySQL's DDL: the editor takes MySQL's templates and
	// changes none of them, so a difference here is an override nobody
	// asked for.
	conn := openFake(t, "8.0.11-TiDB-v8.5.1", true)
	s, err := ReadServer(conn, conn.Version)
	if err != nil {
		t.Fatalf("ReadServer: %v", err)
	}
	// TiDB spells every statement the way MySQL does; it only differs in
	// what it refuses.
	got := NewEditor(conn, s, true, false).Grammar()
	want := mysql.NewEditor(conn, s.MySQL, true, false).Grammar()
	if got != want {
		t.Errorf("grammar = %+v, want MySQL's %+v", got, want)
	}
}
