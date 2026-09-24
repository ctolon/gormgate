package recorder

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/callbacks"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/schema"

	"github.com/ctolon/gormgate/backends/base"
	"github.com/ctolon/gormgate/backends/clickhouse"
	"github.com/ctolon/gormgate/backends/postgresql"
)

// The recorder builds its statements itself and runs them on a connection,
// so the pieces below stand in for a server: a database/sql driver that
// remembers every statement and its arguments and replays canned rows, a
// gorm dialector, and a backend whose vendor and quoting are the real
// ones of the vendor under test. They let a test read the SQL the recorder
// writes without a database.

// fakeName is the name of both the driver and the gorm dialector.
const fakeName = "gormgate_recorder_fake"

// statement is one statement the recorder sent, with its arguments.
type statement struct {
	sql  string
	args []driver.Value
}

type fakeDB struct {
	mu sync.Mutex
	// vendor is what the detector reads, and so the vendor of the backend.
	vendor string
	// tables is what the introspection reports.
	tables []base.TableInfo
	// upper folds the table names the introspection is compared against to
	// upper case, the way Oracle stores unquoted names.
	upper bool
	// rows is what a SELECT answers.
	rows []Record

	statements  []string
	args        [][]driver.Value
	tableLookup int
}

func (d *fakeDB) log(sql string, args []driver.Value) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.statements = append(d.statements, sql)
	d.args = append(d.args, args)
}

// sent returns the statements the recorder sent, in order.
func (d *fakeDB) sent() []statement {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]statement, len(d.statements))
	for i, s := range d.statements {
		out[i] = statement{sql: s, args: d.args[i]}
	}
	return out
}

// only returns the single statement the recorder sent, failing the test
// when it sent anything else.
func (d *fakeDB) only(t *testing.T) statement {
	t.Helper()
	sent := d.sent()
	if len(sent) != 1 {
		t.Fatalf("the recorder sent %d statements, want 1: %v", len(sent), sent)
	}
	return sent[0]
}

var (
	fakeRegistryMu sync.Mutex
	fakeRegistry   = map[string]*fakeDB{}
	fakeSeq        int
)

func init() { sql.Register(fakeName, fakeDriver{}) }

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

func values(args []driver.NamedValue) []driver.Value {
	out := make([]driver.Value, len(args))
	for i, a := range args {
		out[i] = a.Value
	}
	return out
}

// vendorQuery is what the detector reads to learn which backend this
// connection is.
const vendorQuery = "SELECT gormgate_test_vendor()"

func (c *fakeDriverConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.db.log(query, values(args))
	return fakeResult{}, nil
}

func (c *fakeDriverConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if query == vendorQuery {
		return &vendorRows{vendor: c.db.vendor}, nil
	}
	c.db.log(query, values(args))
	if !strings.HasPrefix(query, "SELECT ") {
		return nil, fmt.Errorf("fake driver: unexpected query %q", query)
	}
	c.db.mu.Lock()
	defer c.db.mu.Unlock()
	return &recordRows{rows: append([]Record(nil), c.db.rows...)}, nil
}

type vendorRows struct {
	vendor string
	done   bool
}

func (r *vendorRows) Columns() []string { return []string{"vendor"} }
func (r *vendorRows) Close() error      { return nil }
func (r *vendorRows) Next(dest []driver.Value) error {
	if r.done {
		return io.EOF
	}
	r.done = true
	dest[0] = r.vendor
	return nil
}

type recordRows struct {
	rows []Record
	i    int
}

func (r *recordRows) Columns() []string { return []string{"id", "app", "name", "applied"} }
func (r *recordRows) Close() error      { return nil }
func (r *recordRows) Next(dest []driver.Value) error {
	if r.i >= len(r.rows) {
		return io.EOF
	}
	rec := r.rows[r.i]
	r.i++
	dest[0], dest[1], dest[2], dest[3] = rec.ID, rec.App, rec.Name, rec.Applied
	return nil
}

type fakeDialector struct{ sqlDB *sql.DB }

func (fakeDialector) Name() string { return fakeName }

func (d fakeDialector) Initialize(db *gorm.DB) error {
	db.ConnPool = d.sqlDB
	callbacks.RegisterDefaultCallbacks(db, &callbacks.Config{})
	return nil
}

func (fakeDialector) Migrator(*gorm.DB) gorm.Migrator { return nil }
func (fakeDialector) DataTypeOf(*schema.Field) string { return "text" }
func (fakeDialector) DefaultValueOf(*schema.Field) clause.Expression {
	return clause.Expr{SQL: "DEFAULT"}
}
func (fakeDialector) BindVarTo(w clause.Writer, _ *gorm.Statement, _ any) { w.WriteByte('?') }
func (fakeDialector) QuoteTo(w clause.Writer, s string)                   { w.WriteString(s) }
func (fakeDialector) Explain(sql string, _ ...any) string                 { return sql }

// fakeIntrospection answers the table lookup HasTable makes; nothing else
// of it is reached.
type fakeIntrospection struct{ db *fakeDB }

func (i fakeIntrospection) TableNames(bool) ([]base.TableInfo, error) {
	i.db.mu.Lock()
	defer i.db.mu.Unlock()
	i.db.tableLookup++
	return append([]base.TableInfo(nil), i.db.tables...), nil
}

func (i fakeIntrospection) IdentifierConverter(name string) string {
	if i.db.upper {
		return strings.ToUpper(name)
	}
	return name
}

func (fakeIntrospection) TableDescription(string) ([]base.ColumnInfo, error) {
	return nil, fmt.Errorf("fake introspection: TableDescription is not implemented")
}
func (fakeIntrospection) Constraints(string) (map[string]base.ConstraintInfo, error) {
	return nil, fmt.Errorf("fake introspection: Constraints is not implemented")
}
func (fakeIntrospection) Sequences(string) ([]base.SequenceInfo, error) {
	return nil, fmt.Errorf("fake introspection: Sequences is not implemented")
}
func (fakeIntrospection) Relations(string) (map[string]base.RelationInfo, error) {
	return nil, fmt.Errorf("fake introspection: Relations is not implemented")
}
func (fakeIntrospection) PrimaryKeyColumns(string) ([]string, error) {
	return nil, fmt.Errorf("fake introspection: PrimaryKeyColumns is not implemented")
}
func (fakeIntrospection) TableComment(string) (string, error) {
	return "", fmt.Errorf("fake introspection: TableComment is not implemented")
}

// fakeDBs maps a connection to the fake database behind it, so that the
// backend built for it can find the database it is to read.
var fakeDBs sync.Map // *base.Conn -> *fakeDB

func init() {
	base.Register(base.Detector{
		Name:         "gormgate_recorder_fake",
		Dialector:    fakeName,
		VersionQuery: vendorQuery,
		Resolve: func(c *base.Conn, vendor string) (*base.Backend, error) {
			var ops base.Ops = postgresql.Ops{}
			var features base.Features
			deleteSettings := ""
			if vendor == "clickhouse" {
				ops = clickhouse.Ops{}
				// What the recorder actually depends on, spelled as the
				// capabilities rather than as the vendor's name.
				features = base.Features{PadsSizedStrings: true, NoAutoIncrementOnInsert: true}
				deleteSettings = " SETTINGS mutations_sync = 2"
			}
			return &base.Backend{
				Vendor:                 base.Vendor(vendor),
				Ops:                    ops,
				Features:               features,
				RecorderDeleteSettings: deleteSettings,
				NewIntrospection: func(c *base.Conn) base.Introspection {
					v, ok := fakeDBs.Load(c)
					if !ok {
						panic("no fake database registered for this connection")
					}
					return fakeIntrospection{db: v.(*fakeDB)}
				},
			}, nil
		},
	})
}

// openFake opens a connection to a fake server of the given vendor whose
// migrations table is already there.
func openFake(t *testing.T, vendor string) (*base.Conn, *fakeDB) {
	t.Helper()
	return openFakeTables(t, vendor, []base.TableInfo{{Name: Table, Type: "t"}})
}

// openFakeTables opens a connection to a fake server whose introspection
// reports tables.
func openFakeTables(t *testing.T, vendor string, tables []base.TableInfo) (*base.Conn, *fakeDB) {
	t.Helper()
	fdb := &fakeDB{vendor: vendor, tables: tables}
	fakeRegistryMu.Lock()
	fakeSeq++
	dsn := "fake" + strconv.Itoa(fakeSeq)
	fakeRegistry[dsn] = fdb
	fakeRegistryMu.Unlock()
	sqlDB, err := sql.Open(fakeName, dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	gdb, err := gorm.Open(fakeDialector{sqlDB: sqlDB}, &gorm.Config{SkipDefaultTransaction: true})
	if err != nil {
		t.Fatalf("gorm.Open: %v", err)
	}
	conn, err := base.Open(context.Background(), "default", gdb, nil)
	if err != nil {
		t.Fatalf("base.Open: %v", err)
	}
	fakeDBs.Store(conn, fdb)
	t.Cleanup(func() {
		fakeDBs.Delete(conn)
		conn.Close()
		sqlDB.Close()
		fakeRegistryMu.Lock()
		delete(fakeRegistry, dsn)
		fakeRegistryMu.Unlock()
	})
	return conn, fdb
}

// day is a fixed applied timestamp.
var day = time.Date(2024, 3, 1, 12, 0, 0, 0, time.UTC)
