package base

import (
	"cmp"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/schema"

	m "github.com/ctolon/gormgate/migrations"
)

// django: db/backends/base/schema.py

// Fragment is an ALTER TABLE change clause.
type Fragment struct {
	SQL string
}

// Overrides are the steps a backend may replace. The base Editor never
// calls such a step directly: it goes through the Overrides value it was
// constructed with, which is the outermost backend editor. A backend
// therefore gets its override used by the shared algorithms here, and
// implements Overrides by embedding *Editor and defining only what it
// changes.
//
// Only the steps the shared algorithms actually re-enter are listed. A
// method a backend replaces but nothing here calls -- AlterField, which
// Oracle rewrites -- is reached at the operation boundary, through
// migrations.SchemaEditor, and does not belong in this interface.
type Overrides interface {
	Dialect
	Steps
}

// Dialect renders SQL. Nothing in it executes a statement or changes the
// editor, so an implementation is safe to call from anywhere.
type Dialect interface {
	ColumnSQL(model *m.Model, f *m.ModelField, includeDefault bool) (string, error)
	SkipDefault(f *m.ModelField) bool
	SkipDefaultOnAlter(f *m.ModelField) bool
	DBDefaultSQL(f *m.ModelField) (string, error)
	TableSQL(model *m.Model) (string, error)
	UniqueName(model *m.Model, f *m.ModelField) string

	AlterColumnTypeSQL(model *m.Model, old, new *m.ModelField, newType string) (Fragment, []string, error)
	AlterColumnNullSQL(model *m.Model, old, new *m.ModelField) (string, error)
	AlterColumnDefaultSQL(model *m.Model, old, new *m.ModelField, drop bool) (string, error)
	AlterColumnDatabaseDefaultSQL(model *m.Model, old, new *m.ModelField, drop bool) (string, error)
	AlterColumnCommentSQL(model *m.Model, f *m.ModelField, newType, comment string) (string, error)
	RenameFieldSQL(table string, old, new *m.ModelField, newType string) string

	CreateFKSQL(model *m.Model, f *m.ModelField) *Statement
	CompositeFKSQL(model *m.Model, c *m.ForeignKeyConstraint) (*Statement, error)
	CreateIndexSQL(model *m.Model, ix m.Index) (*Statement, error)
	DeleteIndexSQL(model *m.Model, name string) *Statement
	RenameIndexSQL(model *m.Model, oldName, newName string) *Statement
	CreatePrimaryKeySQL(model *m.Model, columns []string) *Statement
	ConstraintSQL(model *m.Model, c m.Constraint) (string, error)
	CreateConstraintSQL(model *m.Model, c m.Constraint) (*Statement, error)
	RemoveConstraintSQL(model *m.Model, c m.Constraint) (*Statement, error)
}

// Steps are the parts of the algorithms that run statements, and that a
// backend may replace wholesale. The last three are methods of
// migrations.SchemaEditor: they are named here because the shared
// algorithms re-enter them -- RenameIndex falls back to remove and add,
// and CreateModel comments the table it has just made.
type Steps interface {
	PerformAlterField(model *m.Model, old, new *m.ModelField, oldType, newType string, strict bool) error
	ExecDrop(table, name string, st *Statement) error
	DeletePrimaryKey(model *m.Model, strict bool) error
	DeleteComposedIndex(model *m.Model, fields []string) error

	AddIndex(model *m.Model, ix m.Index) error
	RemoveIndex(model *m.Model, ix m.Index) error
	AlterDBTableComment(model *m.Model, old, new string) error
}

// Editor renders and runs the DDL of one migration. It holds the shared
// algorithms; the parts a backend varies are reached through Overrides.
//
// django: db/backends/base/schema.py BaseDatabaseSchemaEditor
type Editor struct {
	// outer is the outermost backend editor: every overridable step
	// goes through it, so a backend gets its own override used.
	outer           Overrides
	conn            *Conn
	g               Grammar
	collect         bool
	sqls            []string
	deferred        []*Statement
	atomicMigration bool
	begun           bool
	tx              *gorm.DB
	// PreCommit, when set, runs after the deferred statements and before
	// the migration transaction is committed (SQLite runs PRAGMA
	// foreign_key_check there, like Django's check_constraints()).
	PreCommit func() error
	// dropped records constraints dropped while collecting SQL, so later
	// introspection in the same collection doesn't see them.
	dropped map[string]map[string]bool
}

// EditorOptions are the settings of one migration run.
type EditorOptions struct {
	// CollectSQL makes the editor collect the statements instead of
	// running them (sqlmigrate).
	CollectSQL bool
	// Atomic asks for the migration to run in one transaction; it only
	// takes effect where the backend can roll DDL back.
	Atomic bool
	// Grammar spells the backend's statements. A backend supplies its
	// own, built once; the editor never changes it.
	Grammar Grammar
}

// NewEditor creates the base editor. outer is the outermost backend editor,
// the one whose overrides the shared algorithms must reach; a backend
// passes the value that embeds the returned Editor.
func NewEditor(outer Overrides, conn *Conn, opts EditorOptions) *Editor {
	if opts.Grammar == nil {
		panic("base.NewEditor: the backend supplied no Grammar")
	}
	return &Editor{
		outer:           outer,
		conn:            conn,
		g:               opts.Grammar,
		collect:         opts.CollectSQL,
		atomicMigration: conn.Backend.Features.CanRollbackDDL && opts.Atomic,
	}
}

// Grammar returns the backend's statement grammar, for a backend whose
// own steps need to spell a statement.
func (e *Editor) Grammar() Grammar { return e.g }

// Self returns the outermost backend editor, the one NewEditor was given.
// Backend code calls an overridable step through it so that a backend
// embedding this one gets its own override used.
func (e *Editor) Self() Overrides { return e.outer }

// Conn returns the backend connection this editor runs on.
func (e *Editor) Conn() *Conn { return e.conn }

// Connection returns the connection as the migration operations see it.
func (e *Editor) Connection() m.Connection { return e.conn }

// AtomicMigration reports whether the migration runs in one transaction.
func (e *Editor) AtomicMigration() bool { return e.atomicMigration }

// Atomic runs fn in a transaction.
func (e *Editor) Atomic(fn func() error) error {
	if e.collect {
		return fn()
	}
	return e.conn.Atomic(fn)
}

// CollectSQL reports collect mode.
func (e *Editor) CollectSQL() bool { return e.collect }

// CollectedSQL returns the collected statements.
func (e *Editor) CollectedSQL() []string { return e.sqls }

// AddCollected appends raw lines, such as comments, to the collected SQL.
func (e *Editor) AddCollected(lines ...string) { e.sqls = append(e.sqls, lines...) }

// Begin starts the migration: it clears the deferred statements and, when
// the migration is atomic and SQL is being run rather than collected, opens
// the transaction.
//
// django: base/schema.py BaseDatabaseSchemaEditor.__enter__
func (e *Editor) Begin() error {
	e.deferred = nil
	e.begun = true
	if e.atomicMigration && !e.collect {
		return e.beginTx()
	}
	return nil
}

// beginTx opens the migration transaction.
func (e *Editor) beginTx() error {
	tx := e.conn.db.Begin()
	if tx.Error != nil {
		return tx.Error
	}
	e.tx = tx
	e.conn.db = tx
	e.conn.atomicDepth++
	return nil
}

// Finish ends the migration. When err is nil it runs the deferred
// statements and the PreCommit hook and commits the transaction; otherwise
// it rolls back. It returns err, joined with the error of a failed rollback
// -- a rollback that did not happen leaves the database in a state the
// caller must not take for clean.
//
// A non-nil err is never dropped: the executor reads the result to decide
// whether the migration may be recorded as applied (SchemaEditor.Finish).
//
// django: base/schema.py BaseDatabaseSchemaEditor.__exit__
func (e *Editor) Finish(err error) error {
	if err == nil {
		// The deferred statements that ran are dropped as they go, so that
		// Deferred() holds exactly what still has to run: after a failure
		// it names the statement that failed and the ones behind it.
		for len(e.deferred) > 0 {
			s := e.deferred[0]
			if err = e.Execute(s.String()); err != nil {
				break
			}
			e.deferred = e.deferred[1:]
		}
	}
	if err == nil && e.PreCommit != nil {
		err = e.PreCommit()
	}
	if e.tx != nil {
		if err == nil {
			err = e.tx.Commit().Error
		} else {
			err = errors.Join(err, e.tx.Rollback().Error)
		}
		e.conn.atomicDepth--
		e.conn.db = e.conn.root
		e.tx = nil
	}
	return err
}

// TransactionManagementError reports DDL attempted inside a transaction on
// a backend that cannot roll DDL back.
//
// django: db/transaction.py TransactionManagementError
type TransactionManagementError struct{ Msg string }

// Error implements error.
func (e *TransactionManagementError) Error() string { return e.Msg }

// Execute runs (or collects) one statement.
//
// django: base/schema.py BaseDatabaseSchemaEditor.execute
func (e *Editor) Execute(sql string, args ...any) error {
	if !e.collect && e.conn.InAtomicBlock() && !e.conn.Backend.Features.CanRollbackDDL {
		return &TransactionManagementError{Msg: "executing DDL statements while in a transaction on databases that can't perform a rollback is prohibited"}
	}
	if e.collect {
		if len(args) > 0 {
			var err error
			if sql, err = e.interpolate(sql, args); err != nil {
				return err
			}
		}
		ending := ";"
		if strings.HasSuffix(strings.TrimRight(sql, " \t\r\n"), ";") {
			ending = ""
		}
		e.sqls = append(e.sqls, sql+ending)
		return nil
	}
	return e.conn.Exec(sql, args...)
}

// interpolate substitutes ? placeholders with quoted values for collected
// SQL.
func (e *Editor) interpolate(sql string, args []any) (string, error) {
	var b strings.Builder
	i := 0
	for _, r := range sql {
		if r == '?' && i < len(args) {
			q, err := e.QuoteValue(args[i])
			if err != nil {
				return "", err
			}
			b.WriteString(q)
			i++
			continue
		}
		b.WriteRune(r)
	}
	return b.String(), nil
}

// Defer queues a statement for the end of the migration.
func (e *Editor) Defer(s *Statement) {
	if s != nil {
		e.deferred = append(e.deferred, s)
	}
}

// ExecDrop executes a statement that drops constraint or index name of
// table and remembers the drop while collecting SQL.
func (e *Editor) ExecDrop(table, name string, st *Statement) error {
	if err := e.Execute(st.String()); err != nil {
		return err
	}
	if e.collect {
		if e.dropped == nil {
			e.dropped = map[string]map[string]bool{}
		}
		if e.dropped[table] == nil {
			e.dropped[table] = map[string]bool{}
		}
		e.dropped[table][name] = true
	}
	return nil
}

// IsDropped reports whether the constraint or index name of table was
// already dropped by ExecDrop while collecting SQL. Backends that read the
// database directly instead of through ConstraintNames consult it so that
// they don't drop the same object twice in a collected migration.
func (e *Editor) IsDropped(table, name string) bool { return e.dropped[table][name] }

// DeferFK queues a foreign key creation, replacing a pending creation of
// the same constraint (several alterations in one migration may each need
// to recreate it).
func (e *Editor) DeferFK(s *Statement) {
	if name := s.Name(); name != nil {
		e.RemoveDeferred(func(d *Statement) bool {
			dn := d.Name()
			return dn != nil && d.Kind() == s.Kind() && dn.Table == name.Table && dn.Value == name.Value
		})
	}
	e.Defer(s)
}

// Deferred returns the queued statements.
func (e *Editor) Deferred() []*Statement { return e.deferred }

// RemoveDeferred drops queued statements matching fn.
func (e *Editor) RemoveDeferred(fn func(*Statement) bool) {
	var kept []*Statement
	for _, s := range e.deferred {
		if !fn(s) {
			kept = append(kept, s)
		}
	}
	e.deferred = kept
}

// QuoteName quotes an identifier.
func (e *Editor) QuoteName(name string) string { return e.conn.Backend.Ops.QuoteName(name) }

// QuoteValue renders a literal.
func (e *Editor) QuoteValue(v any) (string, error) { return e.conn.Backend.Ops.QuoteValue(v) }

// PrepareSQLScript splits a script into statements.
func (e *Editor) PrepareSQLScript(script string) []string {
	return e.conn.Backend.Ops.PrepareSQLScript(script)
}

// SchemaField builds the gorm schema field gorm itself would pass to
// Dialector.DataTypeOf for this column.
func SchemaField(model *m.Model, f *m.ModelField) *schema.Field {
	d := f.Field
	tags := map[string]string{}
	for k, v := range d.Tags {
		tags[k] = v
	}
	if d.PrimaryKey {
		tags["PRIMARYKEY"] = "PRIMARYKEY"
	}
	if d.Unique {
		tags["UNIQUE"] = "UNIQUE"
	}
	if d.AutoIncrement {
		tags["AUTOINCREMENT"] = "AUTOINCREMENT"
	}
	if d.Comment != "" {
		tags["COMMENT"] = d.Comment
	}
	if d.Size != 0 {
		tags["SIZE"] = fmt.Sprint(d.Size)
	}
	sf := &schema.Field{
		Name:                   f.Name,
		DBName:                 f.Column,
		DataType:               schema.DataType(d.Type),
		GORMDataType:           schema.DataType(d.Type),
		PrimaryKey:             d.PrimaryKey,
		AutoIncrement:          d.AutoIncrement,
		AutoIncrementIncrement: d.AutoIncrementIncrement,
		NotNull:                !d.Null,
		Unique:                 d.Unique,
		Size:                   d.Size,
		Precision:              d.Precision,
		Scale:                  d.Scale,
		Comment:                d.Comment,
		TagSettings:            tags,
		Schema:                 &schema.Schema{Name: model.Name, Table: model.Table},
	}
	if sf.AutoIncrementIncrement == 0 {
		sf.AutoIncrementIncrement = schema.DefaultAutoIncrementIncrement
	}
	if d.DBDefault != nil {
		sf.HasDefaultValue = true
		if d.DBDefault.Expr != "" {
			sf.DefaultValue = d.DBDefault.Expr
		} else {
			sf.DefaultValue = fmt.Sprint(d.DBDefault.Value)
			sf.DefaultValueInterface = d.DBDefault.Value
		}
	}
	if d.AutoIncrement {
		sf.HasDefaultValue = true
	}
	if d.Custom != nil && d.Custom.Type != nil {
		sf.FieldType = d.Custom.Type
		sf.IndirectFieldType = d.Custom.Type
	} else {
		t := goType(d)
		sf.FieldType, sf.IndirectFieldType = t, t
	}
	return sf
}

func goType(d m.Field) reflect.Type {
	switch d.Type {
	case m.Bool:
		return reflect.TypeOf(false)
	case m.Int:
		switch d.Size {
		case 8:
			return reflect.TypeOf(int8(0))
		case 16:
			return reflect.TypeOf(int16(0))
		case 32:
			return reflect.TypeOf(int32(0))
		}
		return reflect.TypeOf(int64(0))
	case m.Uint:
		switch d.Size {
		case 8:
			return reflect.TypeOf(uint8(0))
		case 16:
			return reflect.TypeOf(uint16(0))
		case 32:
			return reflect.TypeOf(uint32(0))
		}
		return reflect.TypeOf(uint64(0))
	case m.Float:
		if d.Size == 32 {
			return reflect.TypeOf(float32(0))
		}
		return reflect.TypeOf(float64(0))
	case m.Time:
		return reflect.TypeOf(time.Time{})
	case m.Bytes:
		return reflect.TypeOf([]byte(nil))
	}
	return reflect.TypeOf("")
}

var gormDBDataTyper = reflect.TypeOf((*interface {
	GormDBDataType(*gorm.DB, *schema.Field) string
})(nil)).Elem()

// ColumnType returns the column type exactly as gorm's Migrator.DataTypeOf
// computes it: GormDBDataType of custom types, else the dialector's
// DataTypeOf.
func (e *Editor) ColumnType(model *m.Model, f *m.ModelField) (string, error) {
	sf := SchemaField(model, f)
	db := e.conn.DB()
	if f.Field.Custom != nil {
		if f.Field.Custom.Type == nil {
			return "", fmt.Errorf("gormgate: custom type %s of %s.%s is not linked into the binary", f.Field.Custom, model.Table, f.Column)
		}
		v := reflect.New(f.Field.Custom.Type)
		if v.Type().Implements(gormDBDataTyper) {
			if t := v.Interface().(interface {
				GormDBDataType(*gorm.DB, *schema.Field) string
			}).GormDBDataType(db, sf); t != "" {
				return t, nil
			}
		}
	}
	if sf.DataType == "" {
		return "", fmt.Errorf("gormgate: field %s.%s has no data type", model.Table, f.Column)
	}
	return db.Dialector.DataTypeOf(sf), nil
}

// EffectiveDefault is the Go-side default of a field as a concrete value, or
// nil when the field has none. It is what a column added to a table with rows
// in it is populated with, and it is also the value of the transient DEFAULT
// clause a backend attaches to such a column and then drops again. A database
// default (Field.DBDefault) is a separate thing and is not seen here.
//
// django: base/schema.py BaseDatabaseSchemaEditor._effective_default
func EffectiveDefault(f *m.ModelField) any {
	if f.Field.HasDefault() {
		return f.Field.DefaultValue()
	}
	if (f.Field.AutoNow || f.Field.AutoNowAdd) && f.Field.Type == m.Time {
		return time.Now()
	}
	return nil
}

// DBDefaultSQL renders Field.DBDefault as the text of a DEFAULT clause, the
// way gorm's FullDataTypeOf does: an Expr verbatim, a Value as a literal.
func (e *Editor) DBDefaultSQL(f *m.ModelField) (string, error) {
	d := f.Field.DBDefault
	if d.Expr != "" {
		return d.Expr, nil
	}
	return e.QuoteValue(d.Value)
}

// SkipDefault reports whether a DEFAULT clause has to be left out of the
// column's definition because the backend rejects one for its type (MySQL's
// BLOB and TEXT columns).
//
// django: base/schema.py BaseDatabaseSchemaEditor._column_default_sql (skip_default)
func (e *Editor) SkipDefault(*m.ModelField) bool { return false }

// SkipDefaultOnAlter reports whether a DEFAULT clause has to be left out when
// the column is altered rather than created.
//
// django: base/schema.py BaseDatabaseSchemaEditor.skip_default_on_alter
func (e *Editor) SkipDefaultOnAlter(*m.ModelField) bool { return false }

// ColumnSQL returns the column definition.
//
// django: base/schema.py BaseDatabaseSchemaEditor._iter_column_sql
func (e *Editor) ColumnSQL(model *m.Model, f *m.ModelField, includeDefault bool) (string, error) {
	typ, err := e.ColumnType(model, f)
	if err != nil {
		return "", err
	}
	parts := []string{typ}
	if f.Field.DBDefault != nil {
		ds, err := e.outer.DBDefaultSQL(f)
		if err != nil {
			return "", err
		}
		parts = append(parts, "DEFAULT "+ds)
		includeDefault = false
	}
	null := f.Field.Null
	if includeDefault && !e.outer.SkipDefault(f) && !(null && e.outer.SkipDefaultOnAlter(f)) {
		if v := EffectiveDefault(f); v != nil {
			q, err := e.QuoteValue(v)
			if err != nil {
				return "", err
			}
			parts = append(parts, "DEFAULT "+q)
		}
	}
	if isStringLike(f.Field) && !f.Field.PrimaryKey && e.conn.Backend.Features.InterpretsEmptyStringsAsNulls {
		null = true
	}
	if !null {
		parts = append(parts, "NOT NULL")
	}
	if e.conn.Backend.Features.SupportsCommentsInline && f.Field.Comment != "" {
		c, err := e.QuoteValue(f.Field.Comment)
		if err != nil {
			return "", err
		}
		parts = append(parts, "COMMENT "+c)
	}
	return strings.Join(parts, " "), nil
}

func isStringLike(f m.Field) bool {
	return f.Type == m.String || f.Type == m.Bytes
}

func pkColumns(model *m.Model) []string {
	var cols []string
	for _, f := range model.PK() {
		cols = append(cols, f.Column)
	}
	return cols
}

// QuoteColumns quotes a list of column names and joins them with commas.
func (e *Editor) QuoteColumns(cols []string) string {
	q := make([]string, len(cols))
	for i, c := range cols {
		q[i] = e.QuoteName(c)
	}
	return strings.Join(q, ", ")
}

// UniqueName is the name gorm gives a field-level unique constraint.
func (e *Editor) UniqueName(model *m.Model, f *m.ModelField) string {
	return e.conn.DB().NamingStrategy.UniqueName(model.Table, f.Column)
}

// TableSQL returns the CREATE TABLE statement.
//
// django: base/schema.py BaseDatabaseSchemaEditor.table_sql
func (e *Editor) TableSQL(model *m.Model) (string, error) {
	opts := model.Options()
	for _, ut := range opts.UniqueTogether {
		st, err := e.createUniqueTogetherSQL(model, ut)
		if err != nil {
			return "", err
		}
		e.Defer(st)
	}
	var defs []string
	pkInType := false
	for _, f := range model.Fields {
		def, err := e.outer.ColumnSQL(model, f, false)
		if err != nil {
			return "", err
		}
		if strings.Contains(strings.ToUpper(def), "PRIMARY KEY") {
			pkInType = true
		}
		defs = append(defs, e.QuoteName(f.Column)+" "+def)
	}
	var constraints []string
	if pk := pkColumns(model); len(pk) > 0 && !pkInType {
		cols := e.QuoteColumns(pk)
		constraints = append(constraints, e.g.PrimaryKeyConstraint(PrimaryKeyConstraint{Columns: cols}))
	}
	for _, f := range model.Fields {
		if fk := f.Field.ForeignKey; fk != nil && e.conn.Backend.Features.SupportsForeignKeys {
			st := e.outer.CreateFKSQL(model, f)
			if e.conn.Backend.Features.CanCreateInlineFK {
				st.kind = StatementAddInlineForeignKey
				constraints = append(constraints, st.String())
			} else {
				e.DeferFK(st)
			}
		}
	}
	for _, f := range model.Fields {
		if f.Field.Unique && !f.Field.PrimaryKey {
			name := e.QuoteName(e.outer.UniqueName(model, f))
			body := e.g.UniqueConstraint(UniqueConstraint{Columns: e.QuoteName(f.Column)})
			constraints = append(constraints, e.g.NamedConstraint(NamedConstraint{Name: name, Constraint: body}))
		}
	}
	for _, c := range opts.Constraints {
		s, err := e.outer.ConstraintSQL(model, c)
		if err != nil {
			return "", err
		}
		if s != "" {
			constraints = append(constraints, s)
		}
	}
	table, definition := e.QuoteName(model.Table), strings.Join(append(defs, constraints...), ", ")
	return e.g.CreateTable(CreateTable{Table: table, Definition: definition}), nil
}

// CreateModel creates a table with its indexes and constraints.
//
// django: base/schema.py BaseDatabaseSchemaEditor.create_model
func (e *Editor) CreateModel(model *m.Model) error {
	sql, err := e.outer.TableSQL(model)
	if err != nil {
		return err
	}
	if err := e.Execute(sql); err != nil {
		return err
	}
	if e.conn.Backend.Features.SupportsComments {
		if c := model.Options().DBTableComment; c != "" {
			if err := e.outer.AlterDBTableComment(model, "", c); err != nil {
				return err
			}
		}
		if !e.conn.Backend.Features.SupportsCommentsInline {
			for _, f := range model.Fields {
				if f.Field.Comment == "" {
					continue
				}
				typ, err := e.ColumnType(model, f)
				if err != nil {
					return err
				}
				s, err := e.outer.AlterColumnCommentSQL(model, f, typ, f.Field.Comment)
				if err != nil {
					return err
				}
				if err := e.Execute(s); err != nil {
					return err
				}
			}
		}
	}
	return e.deferModelIndexes(model)
}

func (e *Editor) deferModelIndexes(model *m.Model) error {
	if !model.Managed() {
		return nil
	}
	for _, ix := range model.Options().Indexes {
		if ix.HasExpressions() && !e.conn.Backend.Features.SupportsExpressionIndexes {
			continue
		}
		st, err := e.outer.CreateIndexSQL(model, ix)
		if err != nil {
			return err
		}
		e.Defer(st)
	}
	return nil
}

// DeleteModel drops a table.
//
// django: base/schema.py BaseDatabaseSchemaEditor.delete_model
func (e *Editor) DeleteModel(model *m.Model) error {
	if err := e.Execute(e.g.DropTable(DropTable{Table: e.QuoteName(model.Table)})); err != nil {
		return err
	}
	e.RemoveDeferred(func(s *Statement) bool { return s.ReferencesTable(model.Table) })
	return nil
}

// AddIndex adds an index.
func (e *Editor) AddIndex(model *m.Model, ix m.Index) error {
	if ix.HasExpressions() && !e.conn.Backend.Features.SupportsExpressionIndexes {
		return nil
	}
	st, err := e.outer.CreateIndexSQL(model, ix)
	if err != nil {
		return err
	}
	return e.Execute(st.String())
}

// RemoveIndex drops an index.
func (e *Editor) RemoveIndex(model *m.Model, ix m.Index) error {
	if ix.HasExpressions() && !e.conn.Backend.Features.SupportsExpressionIndexes {
		return nil
	}
	return e.Execute(e.outer.DeleteIndexSQL(model, ix.Name).String())
}

// RenameIndex renames an index, or recreates it when the backend can't.
func (e *Editor) RenameIndex(model *m.Model, old, new m.Index) error {
	if e.conn.Backend.Features.CanRenameIndex {
		return e.Execute(e.outer.RenameIndexSQL(model, old.Name, new.Name).String())
	}
	if err := e.outer.RemoveIndex(model, old); err != nil {
		return err
	}
	return e.outer.AddIndex(model, new)
}

// AddConstraint adds a table constraint.
func (e *Editor) AddConstraint(model *m.Model, c m.Constraint) error {
	st, err := e.outer.CreateConstraintSQL(model, c)
	if err != nil || st == nil {
		return err
	}
	return e.Execute(st.String())
}

// RemoveConstraint drops a table constraint.
func (e *Editor) RemoveConstraint(model *m.Model, c m.Constraint) error {
	st, err := e.outer.RemoveConstraintSQL(model, c)
	if err != nil || st == nil {
		return err
	}
	if _, ok := c.(*m.ForeignKeyConstraint); ok {
		// Dropping a foreign key goes through ExecDrop so that MySQL also
		// drops the index it created to support the key, exactly as it
		// does for a field-level foreign key.
		return e.outer.ExecDrop(model.Table, c.ConstraintName(), st)
	}
	return e.Execute(st.String())
}

// ConstraintSQL renders a constraint for CREATE TABLE ("" when it has to
// be created separately, which is then deferred).
//
// django: models/constraints.py *.constraint_sql
func (e *Editor) ConstraintSQL(model *m.Model, c m.Constraint) (string, error) {
	switch x := c.(type) {
	case *m.CheckConstraint:
		if !e.conn.Backend.Features.SupportsTableCheckConstraints {
			return "", nil
		}
		name := e.QuoteName(x.Name)
		body := e.g.CheckConstraint(CheckConstraint{Check: x.Check})
		return e.g.NamedConstraint(NamedConstraint{Name: name, Constraint: body}), nil
	case *m.UniqueConstraint:
		if !e.uniqueSupported(x) {
			return "", nil
		}
		if x.Condition != "" || len(x.Include) > 0 || x.NullsDistinct != nil {
			st, err := e.outer.CreateConstraintSQL(model, x)
			if err != nil {
				return "", err
			}
			e.Defer(st)
			return "", nil
		}
		uName := e.QuoteName(x.Name)
		uArgs := UniqueConstraint{Columns: e.QuoteColumns(x.Fields), Deferrable: deferrableSQL(x.Deferrable)}
		uBody := e.g.UniqueConstraint(uArgs)
		return e.g.NamedConstraint(NamedConstraint{Name: uName, Constraint: uBody}), nil
	case *m.ForeignKeyConstraint:
		if !e.conn.Backend.Features.SupportsForeignKeys {
			return "", nil
		}
		st, err := e.outer.CompositeFKSQL(model, x)
		if err != nil {
			return "", err
		}
		if !e.conn.Backend.Features.CanCreateInlineFK {
			e.DeferFK(st)
			return "", nil
		}
		st.kind = StatementAddInlineForeignKey
		return st.String(), nil
	}
	return "", fmt.Errorf("gormgate: unsupported constraint %T", c)
}

func deferrableSQL(d m.Deferrable) string {
	switch d {
	case m.Deferred:
		return " DEFERRABLE INITIALLY DEFERRED"
	case m.Immediate:
		return " DEFERRABLE INITIALLY IMMEDIATE"
	}
	return ""
}

func nullsDistinctSQL(nd *bool) string {
	if nd == nil {
		return ""
	}
	if *nd {
		return " NULLS DISTINCT"
	}
	return " NULLS NOT DISTINCT"
}

// django: base/schema.py BaseDatabaseSchemaEditor._unique_supported
func (e *Editor) uniqueSupported(c *m.UniqueConstraint) bool {
	f := e.conn.Backend.Features
	return (c.Condition == "" || f.SupportsPartialIndexes) &&
		(c.Deferrable == "" || f.SupportsDeferrableUniqueConstraints) &&
		(len(c.Include) == 0 || f.SupportsCoveringIndexes) &&
		(c.NullsDistinct == nil || f.SupportsNullsDistinctUniqueConstraints)
}

// CreateConstraintSQL renders the statement that adds a constraint.
//
// django: db/models/constraints.py BaseConstraint.create_sql
func (e *Editor) CreateConstraintSQL(model *m.Model, c m.Constraint) (*Statement, error) {
	table := &Table{Name: model.Table, Quote: e.QuoteName}
	switch x := c.(type) {
	case *m.CheckConstraint:
		if !e.conn.Backend.Features.SupportsTableCheckConstraints {
			return nil, nil
		}
		checkName := e.QuoteName(x.Name)
		st := NewStatement(table)
		return e.spell(st, StatementOther, nil, func(g Grammar) string {
			return g.AddCheck(AddCheck{Table: table.String(), Name: checkName, Check: x.Check})
		}), nil
	case *m.UniqueConstraint:
		if !e.uniqueSupported(x) {
			return nil, nil
		}
		cols := &Columns{Table: model.Table, Names: append([]string(nil), x.Fields...), Quote: e.QuoteName}
		name := &Name{Value: x.Name, Quote: e.QuoteName, Table: model.Table, Columns: x.Fields}
		if x.Condition != "" || len(x.Include) > 0 {
			include := e.includeSQL(model, x.Include)
			nulls, cond := nullsDistinctSQL(x.NullsDistinct), conditionSQL(x.Condition)
			st := NewStatement(table, name, cols, refOf(include))
			return e.spell(st, StatementCreateIndex, name, func(g Grammar) string {
				return g.CreateUniqueIndex(CreateIndex{
					Name: name.String(), Table: table.String(), Columns: cols.String(),
					Include: partString(include), NullsDistinct: nulls, Condition: cond,
				})
			}), nil
		}
		defer_, nulls := deferrableSQL(x.Deferrable), nullsDistinctSQL(x.NullsDistinct)
		st := NewStatement(table, name, cols)
		return e.spell(st, StatementAddUnique, name, func(g Grammar) string {
			return g.AddUnique(AddUnique{
				Table: table.String(), Name: name.String(), Columns: cols.String(),
				NullsDistinct: nulls, Deferrable: defer_,
			})
		}), nil
	case *m.ForeignKeyConstraint:
		if !e.conn.Backend.Features.SupportsForeignKeys {
			return nil, nil
		}
		return e.outer.CompositeFKSQL(model, x)
	}
	return nil, fmt.Errorf("gormgate: unsupported constraint %T", c)
}

// RemoveConstraintSQL renders the statement that drops a constraint.
//
// django: db/models/constraints.py BaseConstraint.remove_sql
func (e *Editor) RemoveConstraintSQL(model *m.Model, c m.Constraint) (*Statement, error) {
	switch x := c.(type) {
	case *m.CheckConstraint:
		if !e.conn.Backend.Features.SupportsTableCheckConstraints {
			return nil, nil
		}
		return e.DeleteCheckSQL(model, x.Name), nil
	case *m.UniqueConstraint:
		if !e.uniqueSupported(x) {
			return nil, nil
		}
		if x.Condition != "" || len(x.Include) > 0 {
			return e.outer.DeleteIndexSQL(model, x.Name), nil
		}
		return e.DeleteUniqueSQL(model, x.Name), nil
	case *m.ForeignKeyConstraint:
		if !e.conn.Backend.Features.SupportsForeignKeys {
			return nil, nil
		}
		return e.DeleteFKSQL(model, x.Name), nil
	}
	return nil, fmt.Errorf("gormgate: unsupported constraint %T", c)
}

func conditionSQL(cond string) string {
	if cond == "" {
		return ""
	}
	return " WHERE " + cond
}

func (e *Editor) includeSQL(model *m.Model, cols []string) any {
	if len(cols) == 0 || !e.conn.Backend.Features.SupportsCoveringIndexes {
		return ""
	}
	inc := &Columns{Table: model.Table, Names: append([]string(nil), cols...), Quote: e.QuoteName}
	st := NewStatement(inc)
	return e.spell(st, StatementOther, nil, func(Grammar) string {
		return " INCLUDE (" + inc.String() + ")"
	})
}

// IndexColumnsSQL renders index elements like gorm's BuildIndexOptions.
func (e *Editor) IndexColumnsSQL(ix m.Index, withLength bool) []string {
	var out []string
	for _, f := range ix.Fields {
		s := e.QuoteName(f.Column)
		if f.Expression != "" {
			s = f.Expression
		} else if withLength && f.Length > 0 {
			s += fmt.Sprintf("(%d)", f.Length)
		}
		if f.Collate != "" {
			s += " COLLATE " + f.Collate
		}
		if f.Sort != "" && e.conn.Backend.Features.SupportsIndexColumnOrdering {
			s += " " + string(f.Sort)
		}
		out = append(out, s)
	}
	return out
}

// indexColumns is a Columns reference with per-column suffixes, so renames
// of plain columns propagate into deferred index statements.
func (e *Editor) indexColumns(model *m.Model, ix m.Index, withLength bool) Reference {
	if ix.HasExpressions() {
		return &Expressions{Table: model.Table, SQL: strings.Join(e.IndexColumnsSQL(ix, withLength), ", ")}
	}
	var names, suffixes []string
	for i, f := range ix.Fields {
		names = append(names, f.Column)
		full := e.IndexColumnsSQL(m.Index{Fields: ix.Fields[i : i+1]}, withLength)[0]
		suffixes = append(suffixes, strings.TrimSpace(strings.TrimPrefix(full, e.QuoteName(f.Column))))
	}
	return &Columns{Table: model.Table, Names: names, Quote: e.QuoteName, Suffixes: suffixes}
}

// CreateIndexSQL renders CREATE INDEX like gorm's generic Migrator.
func (e *Editor) CreateIndexSQL(model *m.Model, ix m.Index) (*Statement, error) {
	name := &Name{Value: ix.Name, Quote: e.QuoteName, Table: model.Table, Columns: ix.Columns()}
	table := &Table{Name: model.Table, Quote: e.QuoteName}
	cols := e.indexColumns(model, ix, true)
	include := e.includeSQL(model, ix.Include)
	args := CreateIndex{Unique: ix.Unique, Class: string(ix.Class)}
	if ix.Type != "" {
		args.Using = " USING " + string(ix.Type)
	}
	if ix.Comment != "" {
		q, err := e.QuoteValue(ix.Comment)
		if err != nil {
			return nil, err
		}
		args.Comment = " COMMENT " + q
	}
	if ix.Option != "" {
		args.Option = " " + ix.Option
	}
	if ix.Where != "" && e.conn.Backend.Features.SupportsPartialIndexes {
		args.Condition = " WHERE " + ix.Where
	}
	st := NewStatement(name, table, cols, refOf(include))
	st.index = &args
	return e.spell(st, StatementCreateIndex, name, func(g Grammar) string {
		a := *st.index
		a.Name, a.Table, a.Columns = name.String(), table.String(), cols.String()
		a.Include = partString(include)
		return g.CreateIndex(a)
	}), nil
}

// DeleteIndexSQL renders DROP INDEX and forgets deferred statements that
// would create it.
//
// django: base/schema.py BaseDatabaseSchemaEditor._delete_index_sql
func (e *Editor) DeleteIndexSQL(model *m.Model, name string) *Statement {
	table := &Table{Name: model.Table, Quote: e.QuoteName}
	quoted := e.QuoteName(name)
	st := NewStatement(table)
	st.dropIndex = &DropIndex{Name: quoted}
	e.spell(st, StatementOther, nil, func(g Grammar) string {
		a := *st.dropIndex
		a.Table = table.String()
		return g.DropIndex(a)
	})
	e.RemoveDeferred(func(s *Statement) bool {
		if n := s.Name(); n != nil && n.Table == model.Table && n.String() == quoted {
			return true
		}
		return s.ReferencesIndex(model.Table, quoted)
	})
	return st
}

// RenameIndexSQL renders ALTER INDEX ... RENAME.
func (e *Editor) RenameIndexSQL(model *m.Model, oldName, newName string) *Statement {
	table := &Table{Name: model.Table, Quote: e.QuoteName}
	oldQ, newQ := e.QuoteName(oldName), e.QuoteName(newName)
	st := NewStatement(table)
	return e.spell(st, StatementOther, nil, func(g Grammar) string {
		return g.RenameIndex(RenameIndex{Table: table.String(), OldName: oldQ, NewName: newQ})
	})
}

// CreateFKSQL renders the foreign key constraint of f.
//
// django: base/schema.py BaseDatabaseSchemaEditor._create_fk_sql
func (e *Editor) CreateFKSQL(model *m.Model, f *m.ModelField) *Statement {
	fk := f.Field.ForeignKey
	toTable := f.Remote.Table
	toColumn := f.RemoteField.Column
	onDelete, onUpdate := "", ""
	if fk.OnDelete != "" {
		onDelete = " ON DELETE " + string(fk.OnDelete)
	}
	if fk.OnUpdate != "" {
		onUpdate = " ON UPDATE " + string(fk.OnUpdate)
	}
	table := &Table{Name: model.Table, Quote: e.QuoteName}
	name := &Name{Value: fk.Name, Quote: e.QuoteName, Table: model.Table, Columns: []string{f.Column}, ToTable: toTable, ToColumns: []string{toColumn}}
	col := &Columns{Table: model.Table, Names: []string{f.Column}, Quote: e.QuoteName}
	to := &Table{Name: toTable, Quote: e.QuoteName}
	toCol := &Columns{Table: toTable, Names: []string{toColumn}, Quote: e.QuoteName}
	st := NewStatement(table, name, col, to, toCol)
	st.foreignKey = &AddForeignKey{OnDelete: onDelete, OnUpdate: onUpdate}
	return e.spell(st, StatementAddForeignKey, name, func(g Grammar) string {
		a := *st.foreignKey
		a.Table, a.Name, a.Column = table.String(), name.String(), col.String()
		a.ToTable, a.ToColumn = to.String(), toCol.String()
		a.Inline = st.kind == StatementAddInlineForeignKey
		return g.AddForeignKey(a)
	})
}

// CompositeFKSQL renders the ALTER TABLE ... ADD CONSTRAINT of a
// table-level foreign key. It is CreateFKSQL for a key that spans more
// than one column, so it fills the same template with column lists.
//
// The constraint names the model it points at rather than a resolved
// field, so the referenced table is looked up in the model's registry.
func (e *Editor) CompositeFKSQL(model *m.Model, c *m.ForeignKeyConstraint) (*Statement, error) {
	remote, err := model.RelatedModel(c.To)
	if err != nil {
		return nil, err
	}
	onDelete, onUpdate := "", ""
	if c.OnDelete != "" {
		onDelete = " ON DELETE " + string(c.OnDelete)
	}
	if c.OnUpdate != "" {
		onUpdate = " ON UPDATE " + string(c.OnUpdate)
	}
	table := &Table{Name: model.Table, Quote: e.QuoteName}
	name := &Name{Value: c.Name, Quote: e.QuoteName, Table: model.Table, Columns: slices.Clone(c.Fields), ToTable: remote.Table, ToColumns: slices.Clone(c.ToFields)}
	col := &Columns{Table: model.Table, Names: slices.Clone(c.Fields), Quote: e.QuoteName}
	to := &Table{Name: remote.Table, Quote: e.QuoteName}
	toCol := &Columns{Table: remote.Table, Names: slices.Clone(c.ToFields), Quote: e.QuoteName}
	st := NewStatement(table, name, col, to, toCol)
	st.foreignKey = &AddForeignKey{OnDelete: onDelete, OnUpdate: onUpdate}
	return e.spell(st, StatementAddForeignKey, name, func(g Grammar) string {
		a := *st.foreignKey
		a.Table, a.Name, a.Column = table.String(), name.String(), col.String()
		a.ToTable, a.ToColumn = to.String(), toCol.String()
		a.Inline = st.kind == StatementAddInlineForeignKey
		return g.AddForeignKey(a)
	}), nil
}

// Spell attaches a grammar rendering to a statement a backend built for
// itself, so that it renders the way the rest of them do.
func (e *Editor) Spell(st *Statement, render func(Grammar) string) *Statement {
	return e.spell(st, StatementOther, nil, render)
}

// SpellAs is Spell for a statement the deferred list has to be able to find
// again: kind and name are what DeferFK and DeleteIndexSQL search by.
func (e *Editor) SpellAs(st *Statement, kind StatementKind, name *Name, render func(Grammar) string) *Statement {
	return e.spell(st, kind, name, render)
}

// spell attaches the grammar rendering to a statement that is still built
// from a template, so that the two can be compared while the templates are
// being retired.
func (e *Editor) spell(st *Statement, kind StatementKind, name *Name, render func(Grammar) string) *Statement {
	st.render, st.g, st.kind, st.name = render, e.g, kind, name
	return st
}

// deleteConstraintSQL renders the drop of a named constraint. drop is the
// Grammar method for the kind being dropped: the backends spell a foreign
// key, a unique constraint and a primary key differently.
func (e *Editor) deleteConstraintSQL(kind StatementKind, drop func(Grammar, DropConstraint) string, model *m.Model, name string) *Statement {
	table := &Table{Name: model.Table, Quote: e.QuoteName}
	quoted := e.QuoteName(name)
	st := NewStatement(table)
	return e.spell(st, kind, nil, func(g Grammar) string {
		return drop(g, DropConstraint{Table: table.String(), Name: quoted})
	})
}

// DeleteFKSQL renders the drop of a foreign key.
func (e *Editor) DeleteFKSQL(model *m.Model, name string) *Statement {
	return e.deleteConstraintSQL(StatementDropForeignKey, Grammar.DropForeignKey, model, name)
}

// DeleteUniqueSQL renders the drop of a unique constraint.
func (e *Editor) DeleteUniqueSQL(model *m.Model, name string) *Statement {
	return e.deleteConstraintSQL(StatementOther, Grammar.DropUnique, model, name)
}

// DeleteCheckSQL renders the drop of a check constraint.
func (e *Editor) DeleteCheckSQL(model *m.Model, name string) *Statement {
	return e.deleteConstraintSQL(StatementOther, Grammar.DropCheck, model, name)
}

// CreateUniqueFieldSQL renders the named unique constraint gorm creates for
// a `unique` field.
func (e *Editor) CreateUniqueFieldSQL(model *m.Model, f *m.ModelField) *Statement {
	table := &Table{Name: model.Table, Quote: e.QuoteName}
	name := &Name{Value: e.outer.UniqueName(model, f), Quote: e.QuoteName, Table: model.Table, Columns: []string{f.Column}}
	cols := &Columns{Table: model.Table, Names: []string{f.Column}, Quote: e.QuoteName}
	st := NewStatement(table, name, cols)
	return e.spell(st, StatementAddUnique, name, func(g Grammar) string {
		return g.AddUnique(AddUnique{Table: table.String(), Name: name.String(), Columns: cols.String()})
	})
}

// createUniqueTogetherSQL renders a unique_together entry with Django's
// generated name.
//
// django: base/schema.py BaseDatabaseSchemaEditor._create_unique_sql
func (e *Editor) createUniqueTogetherSQL(model *m.Model, fields []string) (*Statement, error) {
	for _, f := range fields {
		if model.Field(f) == nil {
			return nil, &m.FieldDoesNotExist{Msg: fmt.Sprintf("%s has no field named '%s'", model.Name, f)}
		}
	}
	name := CreateIndexName(e.conn.Backend.MaxNameLength, model.Table, fields, "_uniq")
	table := &Table{Name: model.Table, Quote: e.QuoteName}
	nameRef := &Name{Value: name, Quote: e.QuoteName, Table: model.Table, Columns: fields, Suffix: "_uniq", MaxLength: e.conn.Backend.MaxNameLength}
	cols := &Columns{Table: model.Table, Names: append([]string(nil), fields...), Quote: e.QuoteName}
	st := NewStatement(table, nameRef, cols)
	return e.spell(st, StatementAddUnique, nameRef, func(g Grammar) string {
		return g.AddUnique(AddUnique{Table: table.String(), Name: nameRef.String(), Columns: cols.String()})
	}), nil
}

// CreatePrimaryKeySQL renders ADD PRIMARY KEY.
func (e *Editor) CreatePrimaryKeySQL(model *m.Model, columns []string) *Statement {
	name := CreateIndexName(e.conn.Backend.MaxNameLength, model.Table, columns, "_pk")
	table := &Table{Name: model.Table, Quote: e.QuoteName}
	quoted := e.QuoteName(name)
	cols := &Columns{Table: model.Table, Names: append([]string(nil), columns...), Quote: e.QuoteName}
	st := NewStatement(table, cols)
	return e.spell(st, StatementOther, nil, func(g Grammar) string {
		return g.AddPrimaryKey(AddPrimaryKey{Table: table.String(), Name: quoted, Columns: cols.String()})
	})
}

// DeletePrimaryKey drops the primary key constraint.
//
// django: base/schema.py BaseDatabaseSchemaEditor._delete_primary_key
func (e *Editor) DeletePrimaryKey(model *m.Model, strict bool) error {
	names, err := e.ConstraintNames(model, nil, ConstraintFilter{PrimaryKey: ptr(true)})
	if err != nil {
		return err
	}
	if strict && len(names) != 1 {
		return fmt.Errorf("found wrong number (%d) of PK constraints for %s", len(names), model.Table)
	}
	for _, n := range names {
		if err := e.outer.ExecDrop(model.Table, n, e.deleteConstraintSQL(StatementOther, Grammar.DropPrimaryKey, model, n)); err != nil {
			return err
		}
	}
	return nil
}

func ptr[T any](v T) *T { return &v }

// SameDBDefault reports whether two database defaults are the same one. A
// nil default (the column has none) equals only another nil default.
func SameDBDefault(a, b *m.DBDefault) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return reflect.DeepEqual(a, b)
}

// ConstraintFilter selects constraints in ConstraintNames.
type ConstraintFilter struct {
	Unique     *bool
	PrimaryKey *bool
	Index      *bool
	ForeignKey *bool
	Check      *bool
	Exclude    map[string]bool
}

// ConstraintNames returns the names of the constraints on columns matching
// the filter, from introspection.
//
// django: base/schema.py BaseDatabaseSchemaEditor._constraint_names
func (e *Editor) ConstraintNames(model *m.Model, columns []string, f ConstraintFilter) ([]string, error) {
	intro := e.conn.Introspection()
	var want []string
	for _, c := range columns {
		want = append(want, intro.IdentifierConverter(TruncateName(c, e.conn.Backend.MaxNameLength, 4)))
	}
	cons, err := intro.Constraints(model.Table)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(cons))
	for n := range cons {
		names = append(names, n)
	}
	slices.Sort(names)
	var out []string
	for _, n := range names {
		info := cons[n]
		if columns != nil && !slices.Equal(want, info.Columns) {
			continue
		}
		if f.Unique != nil && info.Unique != *f.Unique {
			continue
		}
		if f.PrimaryKey != nil && info.PrimaryKey != *f.PrimaryKey {
			continue
		}
		if f.Index != nil && info.Index != *f.Index {
			continue
		}
		if f.Check != nil && info.Check != *f.Check {
			continue
		}
		if f.ForeignKey != nil && info.ForeignKey == nil {
			continue
		}
		if f.Exclude[n] || e.dropped[model.Table][n] {
			continue
		}
		out = append(out, n)
	}
	return out, nil
}

// AlterUniqueTogether changes unique_together.
//
// django: base/schema.py BaseDatabaseSchemaEditor.alter_unique_together
func (e *Editor) AlterUniqueTogether(model *m.Model, old, new [][]string) error {
	olds, news := togetherKeys(old), togetherKeys(new)
	for _, k := range slices.Sorted(maps.Keys(olds)) {
		if _, ok := news[k]; ok {
			continue
		}
		if err := e.outer.DeleteComposedIndex(model, olds[k]); err != nil {
			return err
		}
	}
	for _, k := range slices.Sorted(maps.Keys(news)) {
		if _, ok := olds[k]; ok {
			continue
		}
		st, err := e.createUniqueTogetherSQL(model, news[k])
		if err != nil {
			return err
		}
		if err := e.Execute(st.String()); err != nil {
			return err
		}
	}
	return nil
}

func togetherKeys(t [][]string) map[string][]string {
	out := map[string][]string{}
	for _, x := range t {
		out[strings.Join(x, "\x00")] = x
	}
	return out
}

// DeleteComposedIndex drops the unique constraint over fields.
//
// django: base/schema.py BaseDatabaseSchemaEditor._delete_composed_index
func (e *Editor) DeleteComposedIndex(model *m.Model, fields []string) error {
	exclude := map[string]bool{}
	for _, c := range model.Options().Constraints {
		exclude[c.ConstraintName()] = true
	}
	for _, ix := range model.Options().Indexes {
		exclude[ix.Name] = true
	}
	for _, f := range model.Fields {
		if f.Field.Unique {
			exclude[e.outer.UniqueName(model, f)] = true
		}
	}
	names, err := e.ConstraintNames(model, fields, ConstraintFilter{Unique: ptr(true), PrimaryKey: ptr(false), Exclude: exclude})
	if err != nil {
		return err
	}
	if len(names) > 0 && e.conn.Backend.Features.AllowsMultipleConstraintsOnSameFields {
		def := CreateIndexName(e.conn.Backend.MaxNameLength, model.Table, fields, "_uniq")
		for _, n := range names {
			if n == e.conn.Introspection().IdentifierConverter(def) {
				names = []string{n}
				break
			}
		}
	}
	if len(names) != 1 {
		return fmt.Errorf("found wrong number (%d) of constraints for %s(%s)", len(names), model.Table, strings.Join(fields, ", "))
	}
	return e.outer.ExecDrop(model.Table, names[0], e.DeleteUniqueSQL(model, names[0]))
}

// AlterDBTable renames a table.
//
// django: base/schema.py BaseDatabaseSchemaEditor.alter_db_table
func (e *Editor) AlterDBTable(model *m.Model, oldTable, newTable string) error {
	if oldTable == newTable || (e.conn.Backend.Features.IgnoresTableNameCase && strings.EqualFold(oldTable, newTable)) {
		return nil
	}
	oldQ, newQ := e.QuoteName(oldTable), e.QuoteName(newTable)
	if err := e.Execute(e.g.RenameTable(RenameTable{OldTable: oldQ, NewTable: newQ})); err != nil {
		return err
	}
	for _, s := range e.deferred {
		s.RenameTableReferences(oldTable, newTable)
	}
	return nil
}

// AlterDBTableComment sets the table comment.
func (e *Editor) AlterDBTableComment(model *m.Model, oldComment, newComment string) error {
	if !e.conn.Backend.Features.SupportsComments {
		return nil
	}
	q, err := e.QuoteValue(newComment)
	if err != nil {
		return err
	}
	table := e.QuoteName(model.Table)
	sql := e.g.TableComment(TableComment{Table: table, Comment: q})
	// An empty rendering means the backend has no table comment.
	if sql == "" {
		return nil
	}
	return e.Execute(sql)
}

// AlterColumnCommentSQL renders the column comment statement.
func (e *Editor) AlterColumnCommentSQL(model *m.Model, f *m.ModelField, newType, comment string) (string, error) {
	q, err := e.QuoteValue(comment)
	if err != nil {
		return "", err
	}
	table, column := e.QuoteName(model.Table), e.QuoteName(f.Column)
	return e.g.ColumnComment(ColumnComment{Table: table, Column: column, Type: newType, Comment: q}), nil
}

// AddField adds a column.
//
// django: base/schema.py BaseDatabaseSchemaEditor.add_field
func (e *Editor) AddField(model *m.Model, f *m.ModelField) error {
	def, err := e.outer.ColumnSQL(model, f, true)
	if err != nil {
		return err
	}
	if f.Field.ForeignKey != nil && e.conn.Backend.Features.SupportsForeignKeys {
		e.DeferFK(e.outer.CreateFKSQL(model, f))
	}
	table, column := e.QuoteName(model.Table), e.QuoteName(f.Column)
	if err := e.Execute(e.g.AddColumn(AddColumn{Table: table, Column: column, Definition: def})); err != nil {
		return err
	}
	if f.Field.Unique && !f.Field.PrimaryKey {
		if err := e.Execute(e.CreateUniqueFieldSQL(model, f).String()); err != nil {
			return err
		}
	}
	if f.Field.DBDefault == nil && !e.outer.SkipDefaultOnAlter(f) && EffectiveDefault(f) != nil {
		s, err := e.outer.AlterColumnDefaultSQL(model, nil, f, true)
		if err != nil {
			return err
		}
		if s != "" {
			if err := e.Execute(e.g.AlterTable(AlterTable{Table: e.QuoteName(model.Table), Changes: s})); err != nil {
				return err
			}
		}
	}
	if f.Field.Comment != "" && e.conn.Backend.Features.SupportsComments && !e.conn.Backend.Features.SupportsCommentsInline {
		typ, err := e.ColumnType(model, f)
		if err != nil {
			return err
		}
		s, err := e.outer.AlterColumnCommentSQL(model, f, typ, f.Field.Comment)
		if err != nil {
			return err
		}
		if err := e.Execute(s); err != nil {
			return err
		}
	}
	return nil
}

// RemoveField drops a column.
//
// django: base/schema.py BaseDatabaseSchemaEditor.remove_field
func (e *Editor) RemoveField(model *m.Model, f *m.ModelField) error {
	if f.Field.ForeignKey != nil && e.conn.Backend.Features.SupportsForeignKeys {
		names, err := e.ConstraintNames(model, []string{f.Column}, ConstraintFilter{ForeignKey: ptr(true)})
		if err != nil {
			return err
		}
		for _, n := range names {
			if err := e.outer.ExecDrop(model.Table, n, e.DeleteFKSQL(model, n)); err != nil {
				return err
			}
		}
	}
	dropTable, dropColumn := e.QuoteName(model.Table), e.QuoteName(f.Column)
	if err := e.Execute(e.g.DropColumn(DropColumn{Table: dropTable, Column: dropColumn})); err != nil {
		return err
	}
	e.RemoveDeferred(func(s *Statement) bool { return s.ReferencesColumn(model.Table, f.Column) })
	return nil
}

// IgnoredAttrs names the field attributes a comparison leaves out.
//
// django: base/schema.py BaseDatabaseSchemaEditor._field_should_be_altered
// (ignore={"comment"})
type IgnoredAttrs struct {
	// Comment leaves the column comment out of the comparison, so that a
	// field whose comment is the only change is not treated as altered.
	Comment bool
}

// FieldShouldBeAltered compares the database-relevant parts of two fields.
//
// django: base/schema.py BaseDatabaseSchemaEditor._field_should_be_altered
func (e *Editor) FieldShouldBeAltered(old, new *m.ModelField, ignore IgnoredAttrs) bool {
	if old.Column != new.Column {
		return true
	}
	strip := func(f *m.ModelField) m.Field {
		c := f.Field.Clone()
		c.AutoNow, c.AutoNowAdd, c.Serializer, c.Tags = false, false, "", nil
		if c.ForeignKey != nil && old.Field.ForeignKey != nil && new.Field.ForeignKey != nil &&
			old.Remote != nil && new.Remote != nil && old.Remote.Table == new.Remote.Table {
			c.ForeignKey.To = ""
		}
		if ignore.Comment {
			c.Comment = ""
		}
		return c
	}
	return !strip(old).Equal(strip(new))
}

// AlterField alters a column.
//
// django: base/schema.py BaseDatabaseSchemaEditor.alter_field
func (e *Editor) AlterField(model *m.Model, old, new *m.ModelField, strict bool) error {
	// Django stops here when the field attributes are unchanged. gormgate
	// cannot: a gorm field's column type is produced by the dialector from
	// attributes this comparison ignores (the raw `type:` tag, a custom
	// GormDBDataType), so two fields that compare equal can still render
	// different types. The alteration is skipped only when the rendered
	// types agree as well.
	if !e.FieldShouldBeAltered(old, new, IgnoredAttrs{}) {
		oldType, err1 := e.ColumnType(model, old)
		newType, err2 := e.ColumnType(model, new)
		if err1 != nil || err2 != nil || oldType == newType {
			return errors.Join(err1, err2)
		}
	}
	oldType, err := e.ColumnType(model, old)
	if err != nil {
		return err
	}
	newType, err := e.ColumnType(model, new)
	if err != nil {
		return err
	}
	return e.outer.PerformAlterField(model, old, new, oldType, newType, strict)
}

func fkEqual(a, b *m.ForeignKey) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Name == b.Name && a.ToField == b.ToField && a.OnDelete == b.OnDelete && a.OnUpdate == b.OnUpdate
}

// relatedObjects is _related_non_m2m_objects: fields referencing the altered
// field (directly by primary key or by to_field).
func relatedObjects(model *m.Model, f *m.ModelField) []*m.ModelField {
	var out []*m.ModelField
	for _, rel := range model.RelatedFields() {
		if rel.RemoteField == nil || rel.RemoteField.Column != f.Column {
			continue
		}
		out = append(out, rel)
	}
	slices.SortStableFunc(out, func(a, b *m.ModelField) int { return cmp.Compare(a.Name, b.Name) })
	return out
}

// PerformAlterField is Django's _alter_field: drop what depends on the old
// column, change type/null/default/comment, then recreate.
//
// django: base/schema.py BaseDatabaseSchemaEditor._alter_field
func (e *Editor) PerformAlterField(model *m.Model, old, new *m.ModelField, oldType, newType string, strict bool) error {
	feat := e.conn.Backend.Features
	quote := e.QuoteName
	table := quote(model.Table)
	// Drop the table-level foreign keys that cover the column, and the
	// incoming ones that reference it; both are recreated at the end. A
	// key over several columns is not found by a search for the
	// constraints of one column, so it is taken from the model's options.
	var readdFKCs []fkcRecreate
	if feat.SupportsForeignKeys && e.FieldShouldBeAltered(old, new, IgnoredAttrs{Comment: true}) {
		for _, c := range compositeFKsOn(model, old.Column, new.Column) {
			if err := e.outer.ExecDrop(model.Table, c.Name, e.DeleteFKSQL(model, c.Name)); err != nil {
				return err
			}
			readdFKCs = append(readdFKCs, fkcRecreate{model, c})
		}
	}
	// Drop foreign keys of the column.
	fksDropped := false
	if feat.SupportsForeignKeys && old.Field.ForeignKey != nil && e.FieldShouldBeAltered(old, new, IgnoredAttrs{Comment: true}) {
		names, err := e.ConstraintNames(model, []string{old.Column}, ConstraintFilter{ForeignKey: ptr(true)})
		if err != nil {
			return err
		}
		if strict && len(names) != 1 {
			return fmt.Errorf("found wrong number (%d) of foreign key constraints for %s.%s", len(names), model.Table, old.Column)
		}
		for _, n := range names {
			fksDropped = true
			if err := e.outer.ExecDrop(model.Table, n, e.DeleteFKSQL(model, n)); err != nil {
				return err
			}
		}
	}
	// Drop the field-level unique constraint.
	if old.Field.Unique && (!new.Field.Unique || (!old.Field.PrimaryKey && new.Field.PrimaryKey)) {
		exclude := map[string]bool{}
		for _, c := range model.Options().Constraints {
			exclude[c.ConstraintName()] = true
		}
		for _, ix := range model.Options().Indexes {
			exclude[ix.Name] = true
		}
		names, err := e.ConstraintNames(model, []string{old.Column}, ConstraintFilter{Unique: ptr(true), PrimaryKey: ptr(false), Exclude: exclude})
		if err != nil {
			return err
		}
		if strict && len(names) != 1 {
			return fmt.Errorf("found wrong number (%d) of unique constraints for %s.%s", len(names), model.Table, old.Column)
		}
		for _, n := range names {
			if err := e.outer.ExecDrop(model.Table, n, e.DeleteUniqueSQL(model, n)); err != nil {
				return err
			}
		}
	}
	// Drop incoming foreign keys when a referenced key changes type.
	dropForeignKeys := feat.SupportsForeignKeys &&
		((old.Field.PrimaryKey && new.Field.PrimaryKey) || (old.Field.Unique && new.Field.Unique)) &&
		oldType != newType
	var rels []*m.ModelField
	if dropForeignKeys {
		for _, r := range compositeFKsReferencing(new.Model, new.Column) {
			// A self-referential key may already have been dropped above.
			if slices.ContainsFunc(readdFKCs, func(x fkcRecreate) bool {
				return x.model.Table == r.model.Table && x.c.Name == r.c.Name
			}) {
				continue
			}
			if err := e.outer.ExecDrop(r.model.Table, r.c.Name, e.DeleteFKSQL(r.model, r.c.Name)); err != nil {
				return err
			}
			readdFKCs = append(readdFKCs, r)
		}
		rels = relatedObjects(new.Model, new)
		for _, rel := range rels {
			names, err := e.ConstraintNames(rel.Model, []string{rel.Column}, ConstraintFilter{ForeignKey: ptr(true)})
			if err != nil {
				return err
			}
			for _, n := range names {
				if err := e.outer.ExecDrop(rel.Model.Table, n, e.DeleteFKSQL(rel.Model, n)); err != nil {
					return err
				}
			}
		}
	}
	// Rename the column.
	if old.Column != new.Column {
		if err := e.Execute(e.outer.RenameFieldSQL(model.Table, old, new, newType)); err != nil {
			return err
		}
		for _, s := range e.deferred {
			s.RenameColumnReferences(model.Table, old.Column, new.Column)
		}
	}
	var actions, nullActions, postActions []string
	if oldType != newType || (feat.SupportsComments && old.Field.Comment != new.Field.Comment) {
		frag, other, err := e.outer.AlterColumnTypeSQL(model, old, new, newType)
		if err != nil {
			return err
		}
		if frag.SQL != "" {
			actions = append(actions, frag.SQL)
		}
		postActions = append(postActions, other...)
	}
	// Database default.
	if new.Field.DBDefault != nil {
		if !SameDBDefault(old.Field.DBDefault, new.Field.DBDefault) {
			s, err := e.outer.AlterColumnDatabaseDefaultSQL(model, old, new, false)
			if err != nil {
				return err
			}
			actions = append(actions, s)
		}
	} else if old.Field.DBDefault != nil {
		s, err := e.outer.AlterColumnDatabaseDefaultSQL(model, old, new, true)
		if err != nil {
			return err
		}
		actions = append(actions, s)
	}
	// One-off default for NULL -> NOT NULL.
	needsDatabaseDefault := false
	newDefault := EffectiveDefault(new)
	if old.Field.Null && !new.Field.Null && new.Field.DBDefault == nil {
		oldDefault := EffectiveDefault(old)
		if !e.outer.SkipDefaultOnAlter(new) && !reflect.DeepEqual(oldDefault, newDefault) && newDefault != nil {
			needsDatabaseDefault = true
			s, err := e.outer.AlterColumnDefaultSQL(model, old, new, false)
			if err != nil {
				return err
			}
			actions = append(actions, s)
		}
	}
	if old.Field.Null != new.Field.Null {
		s, err := e.outer.AlterColumnNullSQL(model, old, new)
		if err != nil {
			return err
		}
		if s != "" {
			nullActions = append(nullActions, s)
		}
	}
	fourWay := (new.Field.HasDefault() || new.Field.DBDefault != nil) && old.Field.Null && !new.Field.Null
	if len(actions) > 0 || len(nullActions) > 0 {
		if !fourWay {
			actions = append(actions, nullActions...)
		}
		if feat.SupportsCombinedAlters && len(actions) > 0 {
			actions = []string{strings.Join(actions, ", ")}
		}
		for _, a := range actions {
			if err := e.Execute(e.g.AlterTable(AlterTable{Table: table, Changes: a})); err != nil {
				return err
			}
		}
		if fourWay {
			var def string
			var err error
			if new.Field.DBDefault == nil {
				def, err = e.QuoteValue(newDefault)
			} else {
				def, err = e.outer.DBDefaultSQL(new)
			}
			if err != nil {
				return err
			}
			fill := FillColumnDefault{Table: table, Column: quote(new.Column), Default: def}
			if err := e.Execute(e.g.FillColumnDefault(fill)); err != nil {
				return err
			}
			for _, a := range nullActions {
				if err := e.Execute(e.g.AlterTable(AlterTable{Table: table, Changes: a})); err != nil {
					return err
				}
			}
		}
	}
	for _, s := range postActions {
		if err := e.Execute(s); err != nil {
			return err
		}
	}
	// Primary key changes.
	if old.Field.PrimaryKey && !new.Field.PrimaryKey {
		if err := e.outer.DeletePrimaryKey(model, strict); err != nil {
			return err
		}
	}
	if e.UniqueShouldBeAdded(old, new) {
		if err := e.Execute(e.CreateUniqueFieldSQL(new.Model, new).String()); err != nil {
			return err
		}
	}
	if !old.Field.PrimaryKey && new.Field.PrimaryKey {
		if len(pkColumns(model)) > 0 {
			// Adding a column to an existing primary key: drop and recreate
			// it over all primary-key columns of the new model.
			if err := e.outer.DeletePrimaryKey(model, false); err != nil {
				return err
			}
		}
		if err := e.Execute(e.outer.CreatePrimaryKeySQL(model, pkColumns(new.Model)).String()); err != nil {
			return err
		}
		rels = relatedObjects(new.Model, new)
	}
	// Alter the referencing columns to the new type.
	if dropForeignKeys || (!old.Field.PrimaryKey && new.Field.PrimaryKey) {
		for _, rel := range rels {
			relType, err := e.ColumnType(rel.Model, rel)
			if err != nil {
				return err
			}
			if relType == oldType || relType == newType {
				continue
			}
			// Django passes the referencing field in both its old and
			// its new state here, because there a foreign key's column
			// type is derived from the column it points at, so changing
			// the target changes the referencing column too. In gormgate
			// the referencing column is a field the user declared, with
			// its own type, and the autodetector emits its own AlterField
			// when it changes -- so there is only one version of it, and
			// the fragment a backend builds from old == new is usually
			// empty. Emitting the ALTER anyway would render
			// "ALTER TABLE t " with nothing after it.
			frag, other, err := e.outer.AlterColumnTypeSQL(rel.Model, rel, rel, relType)
			if err != nil {
				return err
			}
			if frag.SQL != "" {
				if err := e.Execute(e.g.AlterTable(AlterTable{Table: quote(rel.Model.Table), Changes: frag.SQL})); err != nil {
					return err
				}
			}
			for _, s := range other {
				if err := e.Execute(s); err != nil {
					return err
				}
			}
		}
	}
	// Recreate the column's foreign key.
	if feat.SupportsForeignKeys && new.Field.ForeignKey != nil &&
		(fksDropped || old.Field.ForeignKey == nil || !fkEqual(old.Field.ForeignKey, new.Field.ForeignKey)) {
		if old.Field.ForeignKey != nil && !fksDropped {
			names, err := e.ConstraintNames(model, []string{old.Column}, ConstraintFilter{ForeignKey: ptr(true)})
			if err != nil {
				return err
			}
			for _, n := range names {
				if err := e.outer.ExecDrop(model.Table, n, e.DeleteFKSQL(model, n)); err != nil {
					return err
				}
			}
		}
		e.DeferFK(e.outer.CreateFKSQL(new.Model, new))
	}
	// Recreate the incoming foreign keys (deferred so that referencing
	// columns altered later in the same migration have their new type).
	if dropForeignKeys {
		for _, rel := range rels {
			if rel.Field.ForeignKey != nil {
				e.DeferFK(e.outer.CreateFKSQL(rel.Model, rel))
			}
		}
	}
	// Recreate the table-level foreign keys dropped above.
	for _, r := range readdFKCs {
		st, err := e.outer.CompositeFKSQL(r.model, r.c)
		if err != nil {
			return err
		}
		if st != nil {
			e.DeferFK(st)
		}
	}
	if needsDatabaseDefault {
		s, err := e.outer.AlterColumnDefaultSQL(model, old, new, true)
		if err != nil {
			return err
		}
		if err := e.Execute(e.g.AlterTable(AlterTable{Table: table, Changes: s})); err != nil {
			return err
		}
	}
	return nil
}

// fkcRecreate is one table-level foreign key that an alteration had to drop,
// with the model it is declared on, so it can be added back afterwards.
type fkcRecreate struct {
	model *m.Model
	c     *m.ForeignKeyConstraint
}

// compositeFKsOn returns the table-level foreign keys of model that
// constrain any of the named columns.
func compositeFKsOn(model *m.Model, columns ...string) []*m.ForeignKeyConstraint {
	var out []*m.ForeignKeyConstraint
	for _, x := range model.Options().Constraints {
		c, ok := x.(*m.ForeignKeyConstraint)
		if !ok {
			continue
		}
		for _, col := range columns {
			if col != "" && slices.Contains(c.Fields, col) {
				out = append(out, c)
				break
			}
		}
	}
	return out
}

// compositeFKsReferencing returns the table-level foreign keys of the whole
// registry that reference the named column of model.
func compositeFKsReferencing(model *m.Model, column string) []fkcRecreate {
	var out []fkcRecreate
	for _, other := range model.Apps().Models() {
		for _, x := range other.Options().Constraints {
			c, ok := x.(*m.ForeignKeyConstraint)
			if !ok || c.Target(other.App) != model.Key() || !slices.Contains(c.ToFields, column) {
				continue
			}
			out = append(out, fkcRecreate{other, c})
		}
	}
	return out
}

// UniqueShouldBeAdded reports whether altering a field from old to new has
// to add a unique constraint.
//
// django: base/schema.py BaseDatabaseSchemaEditor._unique_should_be_added
func (e *Editor) UniqueShouldBeAdded(old, new *m.ModelField) bool {
	return !new.Field.PrimaryKey && new.Field.Unique && (!old.Field.Unique || old.Field.PrimaryKey)
}

// AlterColumnNullSQL renders the clause that makes a column nullable or
// not nullable.
//
// django: base/schema.py BaseDatabaseSchemaEditor._alter_column_null_sql
func (e *Editor) AlterColumnNullSQL(model *m.Model, old, new *m.ModelField) (string, error) {
	if e.conn.Backend.Features.InterpretsEmptyStringsAsNulls && isStringLike(new.Field) {
		return "", nil
	}
	typ, err := e.ColumnType(model, new)
	if err != nil {
		return "", err
	}
	args := AlterColumnNullity{Column: e.QuoteName(new.Column), Type: typ}
	if new.Field.Null {
		return e.g.AlterColumnNull(args), nil
	}
	return e.g.AlterColumnNotNull(args), nil
}

// AlterColumnDefaultSQL renders the clause that sets or, with drop, removes
// a column's default.
//
// django: base/schema.py
// BaseDatabaseSchemaEditor._alter_column_default_sql
func (e *Editor) AlterColumnDefaultSQL(model *m.Model, old, new *m.ModelField, drop bool) (string, error) {
	typ, err := e.ColumnType(model, new)
	if err != nil {
		return "", err
	}
	args := AlterColumnDefault{Column: e.QuoteName(new.Column), Type: typ}
	if drop {
		if new.Field.Null {
			return e.g.AlterColumnDropDefaultNull(args), nil
		}
		return e.g.AlterColumnDropDefault(args), nil
	}
	def, err := e.QuoteValue(EffectiveDefault(new))
	if err != nil {
		return "", err
	}
	args.Default = def
	return e.g.AlterColumnDefault(args), nil
}

// AlterColumnDatabaseDefaultSQL renders the clause that sets or, with
// drop, removes a column's database default.
//
// django: base/schema.py
// BaseDatabaseSchemaEditor._alter_column_database_default_sql
func (e *Editor) AlterColumnDatabaseDefaultSQL(model *m.Model, old, new *m.ModelField, drop bool) (string, error) {
	typ, err := e.ColumnType(model, new)
	if err != nil {
		return "", err
	}
	dbArgs := AlterColumnDefault{Column: e.QuoteName(new.Column), Type: typ}
	if drop {
		return e.g.AlterColumnDropDefault(dbArgs), nil
	}
	def, err := e.outer.DBDefaultSQL(new)
	if err != nil {
		return "", err
	}
	dbArgs.Default = def
	return e.g.AlterColumnDefault(dbArgs), nil
}

// AlterColumnTypeSQL renders the clause that changes a column's type,
// together with any statements that have to run after it.
//
// django: base/schema.py BaseDatabaseSchemaEditor._alter_column_type_sql
func (e *Editor) AlterColumnTypeSQL(model *m.Model, old, new *m.ModelField, newType string) (Fragment, []string, error) {
	var other []string
	if e.conn.Backend.Features.SupportsComments && old.Field.Comment != new.Field.Comment {
		s, err := e.outer.AlterColumnCommentSQL(model, new, newType, new.Field.Comment)
		if err != nil {
			return Fragment{}, nil, err
		}
		if s != "" {
			other = append(other, s)
		}
	}
	oldType, err := e.ColumnType(model, old)
	if err != nil {
		return Fragment{}, nil, err
	}
	if oldType == newType {
		return Fragment{}, other, nil
	}
	typeArgs := AlterColumnType{Column: e.QuoteName(new.Column), Type: newType}
	return Fragment{SQL: e.g.AlterColumnType(typeArgs)}, other, nil
}

// RenameFieldSQL renders the statement that renames a column.
//
// django: base/schema.py BaseDatabaseSchemaEditor._rename_field_sql
func (e *Editor) RenameFieldSQL(table string, old, new *m.ModelField, newType string) string {
	args := RenameColumn{Table: e.QuoteName(table), OldColumn: e.QuoteName(old.Column), NewColumn: e.QuoteName(new.Column)}
	return e.g.RenameColumn(args)
}
