package postgres

import (
	"gorm.io/gorm"

	m "github.com/ctolon/gormgate/migrations"
)

// The vendor names the tests build fake servers for.
const (
	vendorPostgreSQL  = "postgresql"
	vendorCockroachDB = "cockroachdb"
	vendorGaussDB     = "gaussdb"
)

// fakeConn is a migrations.Connection of a chosen vendor, with the
// in_atomic_block flag the NotInTransactionMixin reads.
type fakeConn struct {
	vendor  string
	inBlock bool
	allow   *bool
	// hints records what the last router question passed through.
	app   string
	model string
	hints map[string]any
}

func (c *fakeConn) Alias() string  { return "default" }
func (c *fakeConn) Vendor() string { return c.vendor }
func (c *fakeConn) DB() *gorm.DB   { return nil }
func (c *fakeConn) AllowMigrate(app string, h m.Hints) bool {
	c.app, c.model, c.hints = app, h.ModelName, h.Extra
	if c.allow != nil {
		return *c.allow
	}
	return true
}
func (c *fakeConn) InAtomicBlock() bool { return c.inBlock }

// fakeEditor is a schema editor that records the SQL it is given. The
// embedded interface is nil: a method this test does not expect to be
// called panics rather than passing silently.
type fakeEditor struct {
	m.SchemaEditor
	conn *fakeConn
	sqls []string
	// calls records the PostgreSQL-only steps, so that a test can tell a
	// concurrent index from an ordinary one without an SQL dialect.
	calls []string
	// What this server has, which is what the capability methods report.
	extensions bool
	collations bool
}

// newFakeEditor builds an editor for a server of the named vendor. The
// PostgreSQL forks speak the same dialect and implement every step below,
// but have neither extensions nor collations.
func newFakeEditor(vendor string) *fakeEditor {
	pg := vendor == vendorPostgreSQL
	return &fakeEditor{conn: &fakeConn{vendor: vendor}, extensions: pg, collations: pg}
}

func (e *fakeEditor) HasExtensions() bool { return e.extensions }
func (e *fakeEditor) HasCollations() bool { return e.collations }

func (e *fakeEditor) Connection() m.Connection { return e.conn }
func (e *fakeEditor) Execute(sql string, _ ...any) error {
	e.sqls = append(e.sqls, sql)
	return nil
}

// QuoteName quotes like PostgreSQL does.
func (e *fakeEditor) QuoteName(name string) string { return `"` + name + `"` }

func (e *fakeEditor) AddIndexConcurrently(model *m.Model, ix m.Index) error {
	e.calls = append(e.calls, "AddIndexConcurrently "+model.Table+" "+ix.Name)
	return nil
}

func (e *fakeEditor) RemoveIndexConcurrently(model *m.Model, ix m.Index) error {
	e.calls = append(e.calls, "RemoveIndexConcurrently "+model.Table+" "+ix.Name)
	return nil
}

func (e *fakeEditor) AddConstraintNotValid(model *m.Model, c m.Constraint) error {
	e.calls = append(e.calls, "AddConstraintNotValid "+model.Table+" "+c.ConstraintName())
	return nil
}

func (e *fakeEditor) ValidateConstraint(model *m.Model, name string) error {
	e.calls = append(e.calls, "ValidateConstraint "+model.Table+" "+name)
	return nil
}

func (e *fakeEditor) RemoveConstraint(model *m.Model, c m.Constraint) error {
	e.calls = append(e.calls, "RemoveConstraint "+model.Table+" "+c.ConstraintName())
	return nil
}

var _ Editor = (*fakeEditor)(nil)

// plainEditor is a schema editor from outside the PostgreSQL family: it is
// a migrations.SchemaEditor and nothing more, which is exactly what these
// operations test for before they do anything.
type plainEditor struct {
	m.SchemaEditor
	conn *fakeConn
	sqls []string
}

func newPlainEditor(vendor string) *plainEditor {
	return &plainEditor{conn: &fakeConn{vendor: vendor}}
}

func (e *plainEditor) Connection() m.Connection { return e.conn }
func (e *plainEditor) Execute(sql string, _ ...any) error {
	e.sqls = append(e.sqls, sql)
	return nil
}

// stateWithPony is a project state holding one model with one index.
func stateWithPony(app string, indexes ...m.Index) *m.ProjectState {
	s := m.NewProjectState()
	s.AddModel(&m.ModelState{
		App: app, Name: "Pony", Table: app + "_pony",
		Fields: m.Fields{
			{Name: "id", Field: m.Field{Type: m.Int, Size: 64, PrimaryKey: true, AutoIncrement: true}},
			{Name: "weight", Field: m.Field{Type: m.Int, Size: 64}},
		},
		Options: m.Options{Indexes: indexes},
	})
	return s
}
