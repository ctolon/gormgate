// Package recorder reads and writes the table in which gormgate records
// which migrations have been applied.
//
// django: db/migrations/recorder.py
package recorder

import (
	"fmt"
	"time"

	"github.com/ctolon/gormgate/backends/base"
	m "github.com/ctolon/gormgate/migrations"
)

// Table is the name of the table the applied migrations are recorded in.
//
// django: recorder.py MigrationRecorder.Migration (django_migrations)
const Table = "gormgate_migrations"

// Record is one row of the migrations table: an applied migration and when
// it was applied.
type Record struct {
	ID      int64
	App     string
	Name    string
	Applied time.Time
}

// Recorder reads and writes the applied-migration table of one connection,
// creating it on first write.
//
// django: recorder.py MigrationRecorder
type Recorder struct {
	conn     *base.Conn
	hasTable bool
}

// New returns a recorder writing to conn.
func New(conn *base.Conn) *Recorder { return &Recorder{conn: conn} }

// ModelState describes the migrations table as a model state, so that the
// schema editor can create it like any other table.
//
// Where a sized string column pads what it stores, app and name are
// stored unsized: the recorder compares the values it reads back with the
// names of the migrations on disk, and padding would break that.
func ModelState(f base.Features) *m.ModelState {
	size := 255
	if f.PadsSizedStrings {
		size = 0
	}
	return &m.ModelState{
		App:   "migrations",
		Name:  "Migration",
		Table: Table,
		Fields: m.Fields{
			{Name: "id", Field: m.Field{Type: m.Int, Size: 64, PrimaryKey: true, AutoIncrement: true}},
			{Name: "app", Field: m.Field{Type: m.String, Size: size}},
			{Name: "name", Field: m.Field{Type: m.String, Size: size}},
			{Name: "applied", Field: m.Field{Type: m.Time}},
		},
		Options: m.Options{ClickHouse: &m.ClickHouseTable{Engine: "MergeTree()", OrderBy: "(app, name)"}},
	}
}

// Model renders the migrations table as a model the schema editor can
// create.
func Model(f base.Features) *m.Model {
	st := m.NewProjectState()
	st.AddModel(ModelState(f))
	return st.MustApps().MustModel("migrations", "migration")
}

// HasTable reports whether the migrations table exists.
//
// A true answer is cached, a false one is not: within one command the table
// can only come into existence (EnsureSchema creates it), never go away, so
// the answer is only ever re-asked while it is still false. A Recorder is
// therefore not reusable across a drop of the table.
//
// django: recorder.py MigrationRecorder.has_table
func (r *Recorder) HasTable() (bool, error) {
	if r.hasTable {
		return true, nil
	}
	tables, err := r.conn.Introspection().TableNames(false)
	if err != nil {
		return false, err
	}
	want := r.conn.Introspection().IdentifierConverter(Table)
	for _, t := range tables {
		if t.Name == want || t.Name == Table {
			r.hasTable = true
			break
		}
	}
	return r.hasTable, nil
}

// EnsureSchema creates the migrations table if needed.
//
// django: recorder.py MigrationRecorder.ensure_schema
func (r *Recorder) EnsureSchema() error {
	ok, err := r.HasTable()
	if err != nil {
		return err
	}
	if ok {
		return nil
	}
	ed := r.conn.Backend.NewEditor(r.conn, false, true)
	err = ed.Begin()
	if err == nil {
		err = ed.CreateModel(Model(r.conn.Backend.Features))
	}
	if err = ed.Finish(err); err != nil {
		return &m.MigrationSchemaMissing{Msg: fmt.Sprintf("unable to create the %s table (%s)", Table, err)}
	}
	r.hasTable = true
	return nil
}

// Records returns every row of the migrations table, in recording order.
// It returns nothing when the table does not exist yet.
func (r *Recorder) Records() ([]Record, error) {
	ok, err := r.HasTable()
	if err != nil || !ok {
		return nil, err
	}
	q := r.conn.Backend.Ops.QuoteName
	rows, err := r.conn.Rows(fmt.Sprintf("SELECT %s, %s, %s, %s FROM %s ORDER BY %s",
		q("id"), q("app"), q("name"), q("applied"), q(Table), q("id")))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Record
	for rows.Next() {
		var rec Record
		if err := rows.Scan(&rec.ID, &rec.App, &rec.Name, &rec.Applied); err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// AppliedMigrations lists applied migration keys in recording order.
//
// django: recorder.py MigrationRecorder.applied_migrations
func (r *Recorder) AppliedMigrations() ([]m.Key, error) {
	recs, err := r.Records()
	if err != nil {
		return nil, err
	}
	keys := make([]m.Key, len(recs))
	for i, rec := range recs {
		keys[i] = m.Key{App: rec.App, Name: rec.Name}
	}
	return keys, nil
}

// RecordApplied records that a migration was applied.
//
// django: recorder.py MigrationRecorder.record_applied
func (r *Recorder) RecordApplied(app, name string) error {
	if err := r.EnsureSchema(); err != nil {
		return err
	}
	q := r.conn.Backend.Ops.QuoteName
	if r.conn.Backend.Features.NoAutoIncrementOnInsert {
		return r.conn.Exec(fmt.Sprintf("INSERT INTO %s (%s, %s, %s, %s) SELECT coalesce(max(%s), 0) + 1, ?, ?, ? FROM %s",
			q(Table), q("id"), q("app"), q("name"), q("applied"), q("id"), q(Table)), app, name, time.Now().UTC())
	}
	return r.conn.Exec(fmt.Sprintf("INSERT INTO %s (%s, %s, %s) VALUES (?, ?, ?)", q(Table), q("app"), q("name"), q("applied")),
		app, name, time.Now().UTC())
}

// RecordUnapplied removes a migration's record, so that it counts as
// unapplied.
//
// django: recorder.py MigrationRecorder.record_unapplied
func (r *Recorder) RecordUnapplied(app, name string) error {
	if err := r.EnsureSchema(); err != nil {
		return err
	}
	q := r.conn.Backend.Ops.QuoteName
	stmt := fmt.Sprintf("DELETE FROM %s WHERE %s = ? AND %s = ?", q(Table), q("app"), q("name"))
	stmt += r.conn.Backend.RecorderDeleteSettings
	return r.conn.Exec(stmt, app, name)
}
