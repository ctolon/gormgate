package cockroachdb

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

// A CockroachDB connection is reached through the "postgres" dialector, so
// the pieces below stand in for one: a database/sql driver that answers
// SELECT version() and remembers what was executed, and a gorm dialector
// that calls itself "postgres" and maps types the way gorm's PostgreSQL
// driver does. Together they let base.Open run its real detection and let
// the editor ask for a column type without a server.

const fakeDriverName = "gormgate_cockroachdb_fake"

type fakeDB struct {
	mu sync.Mutex
	// version is what SELECT version() answers.
	version string
	// exec holds every statement that was executed, in order.
	exec []string
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

type fakeResult struct{}

func (fakeResult) LastInsertId() (int64, error) { return 0, nil }
func (fakeResult) RowsAffected() (int64, error) { return 0, nil }

func (c *fakeDriverConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	c.db.mu.Lock()
	defer c.db.mu.Unlock()
	c.db.exec = append(c.db.exec, query)
	return fakeResult{}, nil
}

func (c *fakeDriverConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	c.db.mu.Lock()
	defer c.db.mu.Unlock()
	c.db.exec = append(c.db.exec, query)
	if query != "SELECT version()" {
		return nil, fmt.Errorf("fake driver: unexpected query %q", query)
	}
	return &fakeRows{value: c.db.version}, nil
}

type fakeRows struct {
	value string
	done  bool
}

func (r *fakeRows) Columns() []string { return []string{"version"} }
func (r *fakeRows) Close() error      { return nil }
func (r *fakeRows) Next(dest []driver.Value) error {
	if r.done {
		return io.EOF
	}
	r.done = true
	dest[0] = r.value
	return nil
}

type fakeDialector struct{ sqlDB *sql.DB }

func (fakeDialector) Name() string { return "postgres" }

func (d fakeDialector) Initialize(db *gorm.DB) error {
	db.ConnPool = d.sqlDB
	callbacks.RegisterDefaultCallbacks(db, &callbacks.Config{})
	return nil
}

func (fakeDialector) Migrator(*gorm.DB) gorm.Migrator { return nil }

// DataTypeOf maps a field the way gorm.io/driver/postgres does, which is
// what the editor's ColumnType asks the dialector for.
func (fakeDialector) DataTypeOf(f *schema.Field) string {
	switch f.DataType {
	case schema.Int, schema.Uint:
		if f.AutoIncrement {
			if f.Size <= 16 {
				return "smallserial"
			}
			if f.Size <= 32 {
				return "serial"
			}
			return "bigserial"
		}
		switch {
		case f.Size <= 16:
			return "smallint"
		case f.Size <= 32:
			return "integer"
		}
		return "bigint"
	case schema.String:
		if f.Size > 0 {
			return "varchar(" + strconv.Itoa(f.Size) + ")"
		}
		return "text"
	case schema.Time:
		return "timestamptz"
	case schema.Bool:
		return "boolean"
	}
	return "text"
}

func (fakeDialector) DefaultValueOf(*schema.Field) clause.Expression {
	return clause.Expr{SQL: "DEFAULT"}
}
func (fakeDialector) BindVarTo(w clause.Writer, _ *gorm.Statement, _ any) { w.WriteByte('?') }
func (fakeDialector) QuoteTo(w clause.Writer, s string) {
	w.WriteByte('"')
	w.WriteString(s)
	w.WriteByte('"')
}
func (fakeDialector) Explain(sql string, _ ...any) string { return sql }

// statements returns every statement the server was sent, in order.
func (d *fakeDB) statements() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.exec...)
}

// openFake opens a connection to a server reporting version and fails the
// test when the backend does not accept it.
func openFake(t *testing.T, version string) (*base.Conn, *fakeDB) {
	t.Helper()
	conn, fdb, err := tryOpenFake(t, version)
	if err != nil {
		t.Fatalf("base.Open: %v", err)
	}
	return conn, fdb
}

// tryOpenFake opens a connection to a server reporting version, running the
// real backend detection.
func tryOpenFake(t *testing.T, version string) (*base.Conn, *fakeDB, error) {
	t.Helper()
	fdb := &fakeDB{version: version}
	fakeRegistryMu.Lock()
	fakeSeq++
	dsn := "fake" + strconv.Itoa(fakeSeq)
	fakeRegistry[dsn] = fdb
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
		return nil, fdb, err
	}
	t.Cleanup(func() { conn.Close() })
	return conn, fdb, nil
}
