package executor

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"io"
	"regexp"
	"sort"
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
	"github.com/ctolon/gormgate/internal/recorder"
	m "github.com/ctolon/gormgate/migrations"
)

// This file is the in-memory stand-in for a database backend. It exists so
// that the executor tests can exercise the real executor, loader, recorder
// and operation code paths without a server, the way Django's
// tests/migrations/test_executor.py runs against a real connection.
//
// Three pieces cooperate on one *fakeDB:
//
//   - a database/sql driver that understands exactly the four statements
//     internal/recorder issues;
//   - a base.SchemaEditor whose DDL methods maintain an in-memory
//     table -> columns schema (so "does this table exist?" assertions work);
//   - a base.Introspection that reads that same schema.

// ---------------------------------------------------------------------------
// The in-memory database
// ---------------------------------------------------------------------------

// fakeDB holds the whole state of a fake database: the schema and the rows of
// the migrations table.
type fakeDB struct {
	mu     sync.Mutex
	tables map[string][]string // table name -> ordered column names
	rows   []recorder.Record
	nextID int64

	// log records every schema-editor call in order, e.g.
	// "CreateModel migrations_author".
	log []string

	// hooks let a test make a specific operation fail the way a real
	// database would.
	failCreateTable map[string]bool
	failInsert      bool
	deferredFails   bool
}

func newFakeDB() *fakeDB {
	return &fakeDB{tables: map[string][]string{}, failCreateTable: map[string]bool{}}
}

type fakeSnapshot struct {
	tables map[string][]string
	rows   []recorder.Record
	nextID int64
	log    []string
}

func (d *fakeDB) snapshot() fakeSnapshot {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.snapshotLocked()
}

func (d *fakeDB) snapshotLocked() fakeSnapshot {
	tables := make(map[string][]string, len(d.tables))
	for t, cols := range d.tables {
		tables[t] = append([]string(nil), cols...)
	}
	return fakeSnapshot{
		tables: tables,
		rows:   append([]recorder.Record(nil), d.rows...),
		nextID: d.nextID,
		log:    append([]string(nil), d.log...),
	}
}

// restore rolls the database back to a snapshot. The operation log is kept as
// it is a record of what was attempted, not of what survived.
func (d *fakeDB) restore(s fakeSnapshot) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.tables = s.tables
	d.rows = s.rows
	d.nextID = s.nextID
}

func (d *fakeDB) hasTable(name string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, ok := d.tables[name]
	return ok
}

func (d *fakeDB) columns(name string) []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.tables[name]...)
}

func (d *fakeDB) tableNames() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]string, 0, len(d.tables))
	for t := range d.tables {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// dropTable removes a table the way Django's tests do with raw
// "DROP TABLE" statements through the schema editor.
func (d *fakeDB) dropTable(name string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.tables, name)
}

func (d *fakeDB) logf(format string, a ...any) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.log = append(d.log, fmt.Sprintf(format, a...))
}

func (d *fakeDB) operations() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.log...)
}

func (d *fakeDB) deferredSQLFails() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.deferredFails
}

// fakeDBError stands in for django.db.DatabaseError.
type fakeDBError struct{ msg string }

func (e *fakeDBError) Error() string { return e.msg }

// ---------------------------------------------------------------------------
// database/sql driver
// ---------------------------------------------------------------------------

const fakeDriverName = "gormgate_fake"

var (
	fakeRegistryMu sync.Mutex
	fakeRegistry   = map[string]*fakeDB{}
	fakeSeq        int
)

func init() { sql.Register(fakeDriverName, fakeDriver{}) }

func registerFakeDB(d *fakeDB) string {
	fakeRegistryMu.Lock()
	defer fakeRegistryMu.Unlock()
	fakeSeq++
	dsn := "fake" + strconv.Itoa(fakeSeq)
	fakeRegistry[dsn] = d
	return dsn
}

type fakeDriver struct{}

func (fakeDriver) Open(dsn string) (driver.Conn, error) {
	fakeRegistryMu.Lock()
	d, ok := fakeRegistry[dsn]
	fakeRegistryMu.Unlock()
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

// The four statements internal/recorder issues, and nothing else.
var (
	reInsertRecord = regexp.MustCompile(
		`^INSERT INTO "gormgate_migrations" \("app", "name", "applied"\) VALUES \(\?, \?, \?\)$`)
	reDeleteRecord = regexp.MustCompile(
		`^DELETE FROM "gormgate_migrations" WHERE "app" = \? AND "name" = \?$`)
	reFlushRecords = regexp.MustCompile(`^DELETE FROM "gormgate_migrations"$`)
	reSelectRecord = regexp.MustCompile(
		`^SELECT "id", "app", "name", "applied" FROM "gormgate_migrations" ORDER BY "id"$`)
)

func namedToValues(args []driver.NamedValue) []driver.Value {
	out := make([]driver.Value, len(args))
	for i, a := range args {
		out[i] = a.Value
	}
	return out
}

func (c *fakeDriverConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	vals := namedToValues(args)
	d := c.db
	switch {
	case reInsertRecord.MatchString(query):
		if len(vals) != 3 {
			return nil, fmt.Errorf("fake driver: INSERT wants 3 args, got %d", len(vals))
		}
		d.mu.Lock()
		defer d.mu.Unlock()
		if _, ok := d.tables[recorder.Table]; !ok {
			return nil, &fakeDBError{msg: "no such table: " + recorder.Table}
		}
		if d.failInsert {
			return nil, &fakeDBError{msg: "Recording migration failed."}
		}
		app, _ := vals[0].(string)
		name, _ := vals[1].(string)
		applied, _ := vals[2].(time.Time)
		d.nextID++
		d.rows = append(d.rows, recorder.Record{ID: d.nextID, App: app, Name: name, Applied: applied})
		return fakeResult{lastID: d.nextID, rows: 1}, nil
	case reDeleteRecord.MatchString(query):
		if len(vals) != 2 {
			return nil, fmt.Errorf("fake driver: DELETE wants 2 args, got %d", len(vals))
		}
		app, _ := vals[0].(string)
		name, _ := vals[1].(string)
		d.mu.Lock()
		defer d.mu.Unlock()
		kept := d.rows[:0:0]
		var n int64
		for _, r := range d.rows {
			if r.App == app && r.Name == name {
				n++
				continue
			}
			kept = append(kept, r)
		}
		d.rows = kept
		return fakeResult{rows: n}, nil
	case reFlushRecords.MatchString(query):
		d.mu.Lock()
		defer d.mu.Unlock()
		n := int64(len(d.rows))
		d.rows = nil
		return fakeResult{rows: n}, nil
	}
	return nil, fmt.Errorf("fake driver: unexpected statement %q", query)
}

func (c *fakeDriverConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if !reSelectRecord.MatchString(query) {
		return nil, fmt.Errorf("fake driver: unexpected query %q", query)
	}
	if len(args) != 0 {
		return nil, fmt.Errorf("fake driver: SELECT takes no args, got %d", len(args))
	}
	d := c.db
	d.mu.Lock()
	defer d.mu.Unlock()
	rows := append([]recorder.Record(nil), d.rows...)
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	return &fakeRows{rows: rows}, nil
}

type fakeResult struct {
	lastID int64
	rows   int64
}

func (r fakeResult) LastInsertId() (int64, error) { return r.lastID, nil }
func (r fakeResult) RowsAffected() (int64, error) { return r.rows, nil }

type fakeRows struct {
	rows []recorder.Record
	i    int
}

func (r *fakeRows) Columns() []string { return []string{"id", "app", "name", "applied"} }
func (r *fakeRows) Close() error      { return nil }
func (r *fakeRows) Next(dest []driver.Value) error {
	if r.i >= len(r.rows) {
		return io.EOF
	}
	rec := r.rows[r.i]
	r.i++
	dest[0] = rec.ID
	dest[1] = rec.App
	dest[2] = rec.Name
	dest[3] = rec.Applied
	return nil
}

// ---------------------------------------------------------------------------
// gorm dialector
// ---------------------------------------------------------------------------

const fakeDialectorName = "gormgatefake"

type fakeDialector struct{ sqlDB *sql.DB }

func (fakeDialector) Name() string { return fakeDialectorName }

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
func (fakeDialector) QuoteTo(w clause.Writer, s string) {
	w.WriteByte('"')
	w.WriteString(s)
	w.WriteByte('"')
}
func (fakeDialector) Explain(sql string, _ ...any) string { return sql }

// ---------------------------------------------------------------------------
// base.Backend
// ---------------------------------------------------------------------------

// fakeDBs maps a connection to the in-memory database behind it. The backend
// is registered once for the whole test binary, so its NewEditor /
// NewIntrospection closures look the database up by connection.
var fakeDBs sync.Map // *base.Conn -> *fakeDB

func dbOf(c *base.Conn) *fakeDB {
	v, ok := fakeDBs.Load(c)
	if !ok {
		panic("fake backend: no fake database registered for this connection")
	}
	return v.(*fakeDB)
}

type fakeOps struct{}

func (fakeOps) QuoteName(name string) string { return `"` + name + `"` }
func (fakeOps) QuoteValue(v any) (string, error) {
	switch x := v.(type) {
	case string:
		return "'" + strings.ReplaceAll(x, "'", "''") + "'", nil
	case nil:
		return "NULL", nil
	}
	return fmt.Sprint(v), nil
}
func (fakeOps) PrepareSQLScript(script string) []string { return splitScript(script) }
func (fakeOps) StartTransactionSQL() string             { return "BEGIN;" }
func (fakeOps) EndTransactionSQL() string               { return "COMMIT;" }

func splitScript(script string) []string {
	var out []string
	for _, s := range strings.Split(script, ";") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

var fakeBackend = &base.Backend{
	Vendor:      "fake",
	DisplayName: "Fake",
	Features: base.Features{
		SupportsTransactions:          true,
		CanRollbackDDL:                true,
		SupportsForeignKeys:           true,
		SupportsUniqueConstraints:     true,
		SupportsTableCheckConstraints: true,
		CanRenameIndex:                true,
	},
	Ops: fakeOps{},
	NewEditor: func(c *base.Conn, collectSQL, atomic bool) base.SchemaEditor {
		return &fakeEditor{conn: c, db: dbOf(c), collect: collectSQL, atomic: atomic}
	},
	NewIntrospection: func(c *base.Conn) base.Introspection {
		return fakeIntrospection{db: dbOf(c)}
	},
}

func init() {
	base.Register(base.Detector{
		Name:      "fake",
		Dialector: fakeDialectorName,
		Backend:   fakeBackend,
	})
}

// newFakeConn returns a connection backed by a fresh in-memory database.
func newFakeConn(t *testing.T) (*base.Conn, *fakeDB) {
	t.Helper()
	fdb := newFakeDB()
	dsn := registerFakeDB(fdb)
	sqlDB, err := sql.Open(fakeDriverName, dsn)
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

// ---------------------------------------------------------------------------
// base.SchemaEditor
// ---------------------------------------------------------------------------

type fakeEditor struct {
	conn      *base.Conn
	db        *fakeDB
	collect   bool
	atomic    bool
	snap      fakeSnapshot
	collected []string
	deferred  []*base.Statement
}

var _ base.SchemaEditor = (*fakeEditor)(nil)

func (e *fakeEditor) Connection() m.Connection { return e.conn }
func (e *fakeEditor) Conn() *base.Conn         { return e.conn }

// AtomicMigration is Django's schema_editor.atomic_migration:
// migration.atomic and connection.features.can_rollback_ddl.
func (e *fakeEditor) AtomicMigration() bool {
	return e.atomic && e.conn.Backend.Features.CanRollbackDDL
}

func (e *fakeEditor) Begin() error {
	if e.AtomicMigration() {
		e.snap = e.db.snapshot()
	}
	if e.db.deferredSQLFails() {
		e.deferred = append(e.deferred, base.SetRender(base.NewStatement(), base.BaseGrammar{},
			func(base.Grammar) string { return "BROKEN DEFERRED SQL" }))
	}
	return nil
}

func (e *fakeEditor) Finish(err error) error {
	if err != nil {
		if e.AtomicMigration() {
			e.db.restore(e.snap)
		}
		return err
	}
	if len(e.deferred) > 0 && e.db.deferredSQLFails() {
		if e.AtomicMigration() {
			e.db.restore(e.snap)
		}
		return &fakeDBError{msg: "Failed to apply deferred SQL"}
	}
	return nil
}

func (e *fakeEditor) Deferred() []*base.Statement { return e.deferred }

// Atomic runs fn with a savepoint, rolling the in-memory database back when
// fn fails, which is what a real backend's transaction does.
func (e *fakeEditor) Atomic(fn func() error) error {
	snap := e.db.snapshot()
	if err := fn(); err != nil {
		e.db.restore(snap)
		return err
	}
	return nil
}

func (e *fakeEditor) CollectSQL() bool                   { return e.collect }
func (e *fakeEditor) CollectedSQL() []string             { return e.collected }
func (e *fakeEditor) AddCollected(lines ...string)       { e.collected = append(e.collected, lines...) }
func (e *fakeEditor) PrepareSQLScript(s string) []string { return splitScript(s) }

func (e *fakeEditor) Execute(sql string, args ...any) error {
	if e.collect {
		e.collected = append(e.collected, sql)
		return nil
	}
	e.db.logf("Execute %s", sql)
	return nil
}

func (e *fakeEditor) ddl(format string, a ...any) error {
	if e.collect {
		e.collected = append(e.collected, fmt.Sprintf(format, a...)+";")
		return nil
	}
	e.db.logf(format, a...)
	return nil
}

func (e *fakeEditor) CreateModel(model *m.Model) error {
	if e.collect {
		return e.ddl("CreateModel %s", model.Table)
	}
	e.db.mu.Lock()
	if e.db.failCreateTable[model.Table] {
		e.db.mu.Unlock()
		return &fakeDBError{msg: "cannot create table " + model.Table}
	}
	if _, ok := e.db.tables[model.Table]; ok {
		e.db.mu.Unlock()
		return &fakeDBError{msg: "table " + model.Table + " already exists"}
	}
	e.db.tables[model.Table] = model.Columns()
	e.db.mu.Unlock()
	return e.ddl("CreateModel %s", model.Table)
}

func (e *fakeEditor) DeleteModel(model *m.Model) error {
	if e.collect {
		return e.ddl("DeleteModel %s", model.Table)
	}
	e.db.mu.Lock()
	if _, ok := e.db.tables[model.Table]; !ok {
		e.db.mu.Unlock()
		return &fakeDBError{msg: "no such table: " + model.Table}
	}
	delete(e.db.tables, model.Table)
	e.db.mu.Unlock()
	return e.ddl("DeleteModel %s", model.Table)
}

func (e *fakeEditor) AddField(model *m.Model, f *m.ModelField) error {
	if e.collect {
		return e.ddl("AddField %s.%s", model.Table, f.Column)
	}
	e.db.mu.Lock()
	cols, ok := e.db.tables[model.Table]
	if !ok {
		e.db.mu.Unlock()
		return &fakeDBError{msg: "no such table: " + model.Table}
	}
	for _, c := range cols {
		if c == f.Column {
			e.db.mu.Unlock()
			return &fakeDBError{msg: "duplicate column name: " + f.Column}
		}
	}
	e.db.tables[model.Table] = append(cols, f.Column)
	e.db.mu.Unlock()
	return e.ddl("AddField %s.%s", model.Table, f.Column)
}

func (e *fakeEditor) RemoveField(model *m.Model, f *m.ModelField) error {
	if e.collect {
		return e.ddl("RemoveField %s.%s", model.Table, f.Column)
	}
	e.db.mu.Lock()
	cols, ok := e.db.tables[model.Table]
	if !ok {
		e.db.mu.Unlock()
		return &fakeDBError{msg: "no such table: " + model.Table}
	}
	kept := cols[:0:0]
	found := false
	for _, c := range cols {
		if c == f.Column {
			found = true
			continue
		}
		kept = append(kept, c)
	}
	if !found {
		e.db.mu.Unlock()
		return &fakeDBError{msg: "no such column: " + f.Column}
	}
	e.db.tables[model.Table] = kept
	e.db.mu.Unlock()
	return e.ddl("RemoveField %s.%s", model.Table, f.Column)
}

func (e *fakeEditor) AlterField(model *m.Model, old, new *m.ModelField, _ bool) error {
	if e.collect {
		return e.ddl("AlterField %s.%s -> %s", model.Table, old.Column, new.Column)
	}
	e.db.mu.Lock()
	cols, ok := e.db.tables[model.Table]
	if !ok {
		e.db.mu.Unlock()
		return &fakeDBError{msg: "no such table: " + model.Table}
	}
	found := false
	for i, c := range cols {
		if c == old.Column {
			cols[i] = new.Column
			found = true
			break
		}
	}
	e.db.mu.Unlock()
	if !found {
		return &fakeDBError{msg: "no such column: " + old.Column}
	}
	return e.ddl("AlterField %s.%s -> %s", model.Table, old.Column, new.Column)
}

func (e *fakeEditor) AlterDBTable(model *m.Model, oldTable, newTable string) error {
	if oldTable == newTable {
		return nil
	}
	if e.collect {
		return e.ddl("AlterDBTable %s -> %s", oldTable, newTable)
	}
	e.db.mu.Lock()
	cols, ok := e.db.tables[oldTable]
	if !ok {
		e.db.mu.Unlock()
		return &fakeDBError{msg: "no such table: " + oldTable}
	}
	delete(e.db.tables, oldTable)
	e.db.tables[newTable] = cols
	e.db.mu.Unlock()
	return e.ddl("AlterDBTable %s -> %s", oldTable, newTable)
}

func (e *fakeEditor) AlterDBTableComment(model *m.Model, _, newComment string) error {
	return e.ddl("AlterDBTableComment %s %q", model.Table, newComment)
}

func (e *fakeEditor) AlterUniqueTogether(model *m.Model, old, new [][]string) error {
	return e.ddl("AlterUniqueTogether %s %v -> %v", model.Table, old, new)
}

func (e *fakeEditor) AddIndex(model *m.Model, ix m.Index) error {
	return e.ddl("AddIndex %s %s", model.Table, ix.Name)
}

func (e *fakeEditor) RemoveIndex(model *m.Model, ix m.Index) error {
	return e.ddl("RemoveIndex %s %s", model.Table, ix.Name)
}

func (e *fakeEditor) RenameIndex(model *m.Model, old, new m.Index) error {
	return e.ddl("RenameIndex %s %s -> %s", model.Table, old.Name, new.Name)
}

func (e *fakeEditor) AddConstraint(model *m.Model, c m.Constraint) error {
	return e.ddl("AddConstraint %s %s", model.Table, c.ConstraintName())
}

func (e *fakeEditor) RemoveConstraint(model *m.Model, c m.Constraint) error {
	return e.ddl("RemoveConstraint %s %s", model.Table, c.ConstraintName())
}

// ---------------------------------------------------------------------------
// base.Introspection
// ---------------------------------------------------------------------------

type fakeIntrospection struct{ db *fakeDB }

var _ base.Introspection = fakeIntrospection{}

func (i fakeIntrospection) TableNames(bool) ([]base.TableInfo, error) {
	names := i.db.tableNames()
	out := make([]base.TableInfo, len(names))
	for k, n := range names {
		out[k] = base.TableInfo{Name: n, Type: "t"}
	}
	return out, nil
}

func (i fakeIntrospection) TableDescription(table string) ([]base.ColumnInfo, error) {
	if !i.db.hasTable(table) {
		return nil, &fakeDBError{msg: "no such table: " + table}
	}
	cols := i.db.columns(table)
	out := make([]base.ColumnInfo, len(cols))
	for k, c := range cols {
		out[k] = base.ColumnInfo{Name: c, Type: "text"}
	}
	return out, nil
}

func (fakeIntrospection) Constraints(string) (map[string]base.ConstraintInfo, error) {
	return map[string]base.ConstraintInfo{}, nil
}
func (fakeIntrospection) Sequences(string) ([]base.SequenceInfo, error) { return nil, nil }
func (fakeIntrospection) Relations(string) (map[string]base.RelationInfo, error) {
	return map[string]base.RelationInfo{}, nil
}
func (fakeIntrospection) PrimaryKeyColumns(string) ([]string, error) { return nil, nil }
func (fakeIntrospection) TableComment(string) (string, error)        { return "", nil }
func (fakeIntrospection) IdentifierConverter(name string) string     { return name }
