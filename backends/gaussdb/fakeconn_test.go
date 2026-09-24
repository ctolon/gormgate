package gaussdb

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

// The pieces below stand in for an openGauss connection: a database/sql
// driver that answers the two queries the backend asks while it is being
// detected, and a gorm dialector calling itself "gaussdb" that maps types
// the way gorm.io/driver/gaussdb does. They let base.Open run the real
// detection, and the compatibility check, without a server.

const fakeDriverName = "gormgate_gaussdb_fake"

const compatQuery = "SELECT datcompatibility FROM pg_database WHERE datname = current_database()"

type fakeDB struct {
	// compat is what the compatibility query answers; empty makes the
	// query fail, as it does on a server that does not have the column.
	compat string
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
	case "SELECT version()":
		return &fakeRows{column: "version", value: "(openGauss 7.0.0-RC1 build 12345) compiled at 2025-01-01"}, nil
	case compatQuery:
		if c.db.compat == "" {
			return nil, fmt.Errorf(`column "datcompatibility" does not exist`)
		}
		return &fakeRows{column: "datcompatibility", value: c.db.compat}, nil
	}
	return nil, fmt.Errorf("fake driver: unexpected query %q", query)
}

type fakeRows struct {
	column string
	value  string
	done   bool
}

func (r *fakeRows) Columns() []string { return []string{r.column} }
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

func (fakeDialector) Name() string { return "gaussdb" }

func (d fakeDialector) Initialize(db *gorm.DB) error {
	db.ConnPool = d.sqlDB
	callbacks.RegisterDefaultCallbacks(db, &callbacks.Config{})
	return nil
}

func (fakeDialector) Migrator(*gorm.DB) gorm.Migrator { return nil }

// DataTypeOf maps a field the way the PostgreSQL-derived gaussdb driver
// does, which is what the editor's ColumnType asks the dialector for.
func (fakeDialector) DataTypeOf(f *schema.Field) string {
	switch f.DataType {
	case schema.Int, schema.Uint:
		switch {
		case f.Size <= 32 && f.AutoIncrement:
			return "serial"
		case f.AutoIncrement:
			return "bigserial"
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

// openFake opens a connection to a database whose DBCOMPATIBILITY is
// compat, running the real backend detection.
func openFake(t *testing.T, compat string) (*base.Conn, error) {
	t.Helper()
	fakeRegistryMu.Lock()
	fakeSeq++
	dsn := "fake" + strconv.Itoa(fakeSeq)
	fakeRegistry[dsn] = &fakeDB{compat: compat}
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
		return nil, err
	}
	t.Cleanup(func() { conn.Close() })
	return conn, nil
}
