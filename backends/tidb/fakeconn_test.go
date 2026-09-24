package tidb

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"io"
	"strconv"
	"sync"
	"testing"

	"gorm.io/gorm"
	"gorm.io/gorm/callbacks"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/schema"

	"github.com/ctolon/gormgate/backends/base"
)

// TiDB is reached through the "mysql" dialector, so the pieces below stand
// in for one: a database/sql driver that answers the three queries the
// backend asks while it is being detected, and a gorm dialector calling
// itself "mysql" that maps types the way gorm.io/driver/mysql does. They
// let base.Open run the real detection, and the editor ask for a column
// type, without a server.

const fakeDriverName = "gormgate_tidb_fake"

type fakeDB struct {
	version string
	// checkConstraints is what @@tidb_enable_check_constraint answers.
	checkConstraints bool
}

var (
	fakeRegistryMu sync.Mutex
	fakeRegistry   = map[string]*fakeDB{}
	fakeSeq        int
)

func init() { sql.Register(fakeDriverName, fakeDriver{}) }

type fakeDriver struct{}

func (fakeDriver) Open(dsn string) (driver.Conn, error) {
	fakeRegistryMu.Lock()
	defer fakeRegistryMu.Unlock()
	d, ok := fakeRegistry[dsn]
	if !ok {
		return nil, fmt.Errorf("fake driver: unknown dsn %q", dsn)
	}
	return &fakeDriverConn{db: d}, nil
}

type fakeDriverConn struct{ db *fakeDB }

func (c *fakeDriverConn) Prepare(string) (driver.Stmt, error) {
	return nil, fmt.Errorf("fake driver: prepared statements are not supported")
}
func (c *fakeDriverConn) Close() error              { return nil }
func (c *fakeDriverConn) Begin() (driver.Tx, error) { return fakeTx{}, nil }

type fakeTx struct{}

func (fakeTx) Commit() error   { return nil }
func (fakeTx) Rollback() error { return nil }

func (c *fakeDriverConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	switch query {
	case "SELECT VERSION()":
		return &fakeRows{cols: []string{"version"}, values: []driver.Value{c.db.version}}, nil
	case "SELECT @@lower_case_table_names, @@default_storage_engine":
		return &fakeRows{
			cols:   []string{"@@lower_case_table_names", "@@default_storage_engine"},
			values: []driver.Value{int64(0), "InnoDB"},
		}, nil
	case "SELECT @@tidb_enable_check_constraint":
		return &fakeRows{
			cols:   []string{"@@tidb_enable_check_constraint"},
			values: []driver.Value{c.db.checkConstraints},
		}, nil
	}
	return nil, fmt.Errorf("fake driver: unexpected query %q", query)
}

type fakeRows struct {
	cols   []string
	values []driver.Value
	done   bool
}

func (r *fakeRows) Columns() []string { return r.cols }
func (r *fakeRows) Close() error      { return nil }
func (r *fakeRows) Next(dest []driver.Value) error {
	if r.done {
		return io.EOF
	}
	r.done = true
	copy(dest, r.values)
	return nil
}

type fakeDialector struct{ sqlDB *sql.DB }

func (fakeDialector) Name() string { return "mysql" }

func (d fakeDialector) Initialize(db *gorm.DB) error {
	db.ConnPool = d.sqlDB
	callbacks.RegisterDefaultCallbacks(db, &callbacks.Config{})
	return nil
}

func (fakeDialector) Migrator(*gorm.DB) gorm.Migrator { return nil }

// DataTypeOf maps a field the way gorm.io/driver/mysql does, which is what
// the editor's ColumnType asks the dialector for.
func (fakeDialector) DataTypeOf(f *schema.Field) string {
	switch f.DataType {
	case schema.Int, schema.Uint:
		t := "bigint"
		if f.Size <= 32 {
			t = "int"
		}
		if f.DataType == schema.Uint {
			t += " unsigned"
		}
		if f.AutoIncrement {
			t += " AUTO_INCREMENT"
		}
		return t
	case schema.String:
		if f.Size > 0 {
			return "varchar(" + strconv.Itoa(f.Size) + ")"
		}
		return "longtext"
	case schema.Time:
		return "datetime(3) NULL"
	case schema.Bool:
		return "boolean"
	}
	return "longtext"
}

func (fakeDialector) DefaultValueOf(*schema.Field) clause.Expression {
	return clause.Expr{SQL: "DEFAULT"}
}
func (fakeDialector) BindVarTo(w clause.Writer, _ *gorm.Statement, _ any) { w.WriteByte('?') }
func (fakeDialector) QuoteTo(w clause.Writer, s string) {
	w.WriteByte('`')
	w.WriteString(s)
	w.WriteByte('`')
}
func (fakeDialector) Explain(sql string, _ ...any) string { return sql }

// openFake opens a connection to a server reporting version, running the
// real backend detection.
func openFake(t *testing.T, version string, checkConstraints bool) *base.Conn {
	t.Helper()
	fakeRegistryMu.Lock()
	fakeSeq++
	dsn := "fake" + strconv.Itoa(fakeSeq)
	fakeRegistry[dsn] = &fakeDB{version: version, checkConstraints: checkConstraints}
	fakeRegistryMu.Unlock()
	sqlDB, err := sql.Open(fakeDriverName, dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	gdb, err := gorm.Open(fakeDialector{sqlDB: sqlDB}, &gorm.Config{SkipDefaultTransaction: true})
	if err != nil {
		t.Fatalf("gorm.Open: %v", err)
	}
	t.Cleanup(func() {
		sqlDB.Close()
		fakeRegistryMu.Lock()
		delete(fakeRegistry, dsn)
		fakeRegistryMu.Unlock()
	})
	conn, err := base.Open(context.Background(), "default", gdb, nil)
	if err != nil {
		t.Fatalf("base.Open: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}
