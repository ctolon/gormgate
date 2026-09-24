// Package sqlite3 is the SQLite backend (dialector "sqlite", either the cgo
// driver gorm.io/driver/sqlite or the pure-Go github.com/glebarez/sqlite).
//
// SQLite's ALTER TABLE can add a column, rename a column or a table, and,
// from 3.35.5, drop a column. Everything else -- a type change, a
// nullability change, a default, a constraint, an index over a changed
// column -- is done by remaking the table: create one with the wanted
// shape under a temporary name, copy the rows across, drop the original
// and rename. remakeTable is that procedure and most of this package hangs
// off it. parse.go is there for the same reason: a remake has to preserve
// the CHECK constraints of the old table, and the only place they exist is
// the CREATE TABLE text SQLite kept.
//
// django: db/backends/sqlite3/
package sqlite3

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"gorm.io/gorm"

	"github.com/ctolon/gormgate/backends/base"
	m "github.com/ctolon/gormgate/migrations"
)

// Features are SQLite's DatabaseFeatures values.
//
// django: db/backends/sqlite3/features.py
var Features = base.Features{
	SupportsTransactions:      true,
	CanRollbackDDL:            true,
	SupportsCombinedAlters:    false,
	SupportsForeignKeys:       true,
	SupportsUniqueConstraints: true,
	// Django declares can_create_inline_fk = False and puts a column-level
	// "REFERENCES ..." clause in the column definition instead. gormgate
	// emits the named table constraint gorm's AutoMigrate emits
	// ("CONSTRAINT fk_x FOREIGN KEY (c) REFERENCES t(c)"), which is what
	// CanCreateInlineFK renders in CREATE TABLE.
	CanCreateInlineFK:                      true,
	SupportsComments:                       false,
	SupportsCommentsInline:                 false,
	SupportsPartialIndexes:                 true,
	SupportsExpressionIndexes:              true,
	SupportsCoveringIndexes:                false,
	SupportsDeferrableUniqueConstraints:    false,
	SupportsNullsDistinctUniqueConstraints: false,
	SupportsIndexColumnOrdering:            true,
	SupportsTableCheckConstraints:          true,
	CanRenameIndex:                         false,
	InterpretsEmptyStringsAsNulls:          false,
	AllowsMultipleConstraintsOnSameFields:  true,
	IgnoresTableNameCase:                   true,
}

// minVersion is Django's DatabaseFeatures.minimum_database_version.
var minVersion = [3]int{3, 31, 0}

// dropColumnVersion is the first SQLite version supporting ALTER TABLE ...
// DROP COLUMN (Django's can_alter_table_drop_column).
var dropColumnVersion = [3]int{3, 35, 5}

// Backend is the SQLite backend definition.
var Backend = &base.Backend{
	Vendor:      "sqlite",
	DisplayName: "SQLite",
	Features:    Features,
	// SQLite imposes no limit on identifier length.
	MaxNameLength:       0,
	NewIntrospection:    func(c *base.Conn) base.Introspection { return &Introspection{Conn: c} },
	SequenceResetSQL:    SequenceResetSQL,
	Ops:                 Ops{},
	InitConnectionState: InitConnectionState,
}

func init() {
	Backend.NewEditor = func(c *base.Conn, collect, atomic bool) base.SchemaEditor {
		return NewEditor(c, collect, atomic)
	}
	base.Register(base.Detector{
		Name:         "sqlite",
		Dialector:    "sqlite",
		VersionQuery: "select sqlite_version()",
		Check: func(c *base.Conn, version string) error {
			if compareVersion(version, minVersion) < 0 {
				return fmt.Errorf("gormgate: SQLite %d.%d.%d or later is required (found %s)",
					minVersion[0], minVersion[1], minVersion[2], version)
			}
			return nil
		},
		Backend: Backend,
	})
}

// parseVersion splits a "3.45.1" version string into its numbers. A part
// that is not a number, such as the "1-rc2" of a pre-release, counts as the
// number it starts with.
func parseVersion(v string) [3]int {
	var out [3]int
	rest := strings.TrimSpace(v)
	for i := range out {
		part, tail, _ := strings.Cut(rest, ".")
		rest = tail
		if end := strings.IndexFunc(part, func(r rune) bool { return r < '0' || r > '9' }); end >= 0 {
			part = part[:end]
		}
		out[i], _ = strconv.Atoi(part)
	}
	return out
}

// compareVersion compares a version string with a version triple.
func compareVersion(v string, want [3]int) int {
	got := parseVersion(v)
	return slices.Compare(got[:], want[:])
}

// InitConnectionState prepares the pinned session.
//
// django: sqlite3/base.py DatabaseWrapper.get_new_connection
func InitConnectionState(c *base.Conn, session *gorm.DB) (*gorm.DB, error) {
	if err := c.Exec("PRAGMA foreign_keys = ON"); err != nil {
		return nil, err
	}
	// Some builds default legacy_alter_table to ON, which prevents atomic
	// table renames (references to the renamed table are not updated).
	if err := c.Exec("PRAGMA legacy_alter_table = OFF"); err != nil {
		return nil, err
	}
	return nil, nil
}

// Ops are SQLite's DatabaseOperations helpers.
type Ops struct{}

// QuoteName quotes an identifier.
//
// django: sqlite3/operations.py DatabaseOperations.quote_name
func (Ops) QuoteName(name string) string {
	if strings.HasPrefix(name, `"`) && strings.HasSuffix(name, `"`) {
		return name
	}
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// QuoteValue renders a literal.
//
// django: sqlite3/schema.py DatabaseSchemaEditor.quote_value
func (Ops) QuoteValue(v any) (string, error) {
	return base.StandardQuoteValue(v, base.QuoteValueOptions{
		True: "1", False: "0", TimeFormat: "2006-01-02 15:04:05.999999",
	})
}

// SplitOptions are SQLite's lexical rules: backtick-quoted identifiers
// (SQLite accepts MySQL's spelling), no backslash escapes inside string
// literals and no dollar quoting.
var SplitOptions = base.SplitOptions{BacktickQuotes: true}

// PrepareSQLScript splits a script into statements.
func (Ops) PrepareSQLScript(script string) []string { return base.SplitSQL(script, SplitOptions) }

// StartTransactionSQL is used by sqlmigrate.
func (Ops) StartTransactionSQL() string { return "BEGIN;" }

// EndTransactionSQL is used by sqlmigrate.
func (Ops) EndTransactionSQL() string { return "COMMIT;" }

// SequenceResetSQL returns no statements: SQLite's DatabaseOperations has
// no sequence_reset_sql, only sequence_reset_by_name_sql (used by flush).
//
// django: base/operations.py BaseDatabaseOperations.sequence_reset_sql
func SequenceResetSQL(c *base.Conn, style base.Style, models []*m.Model) ([]string, error) {
	return nil, nil
}

// Editor is the SQLite schema editor.
//
// django: sqlite3/schema.py DatabaseSchemaEditor
type Editor struct {
	*base.Editor
	// remakeTemp and remakeOrig are the temporary and the final name of
	// the table being remade; names gorm derives from the table name must
	// use the final one.
	remakeTemp, remakeOrig string
}

// A method whose name does not match a step of base.Overrides is a
// method nothing calls; this makes that a build failure.
var _ base.Overrides = (*Editor)(nil)

// UniqueName is the name gorm gives a field-level unique constraint. While
// a table is being remade under its temporary name, the constraint keeps
// the name it has on the final table.
func (e *Editor) UniqueName(model *m.Model, f *m.ModelField) string {
	if e.remakeTemp != "" && model.Table == e.remakeTemp {
		clone := *model
		clone.Table = e.remakeOrig
		return e.Editor.UniqueName(&clone, f)
	}
	return e.Editor.UniqueName(model, f)
}

// NewEditor builds a SQLite schema editor.
func NewEditor(c *base.Conn, collect, atomic bool) *Editor {
	e := &Editor{}
	e.Editor = base.NewEditor(e, c, base.EditorOptions{
		CollectSQL: collect, Atomic: atomic, Grammar: Grammar{},
	})
	e.PreCommit = e.checkConstraints
	return e
}

// Begin disables foreign key checks for the duration of the schema
// edition. The PRAGMA cannot be changed inside a transaction, so it must
// happen before the migration transaction is opened.
//
// django: sqlite3/schema.py DatabaseSchemaEditor.__enter__
func (e *Editor) Begin() error {
	if e.CollectSQL() {
		// A collected script runs on another connection later on, so it
		// carries the PRAGMA that Django applies to the live connection.
		e.AddCollected("PRAGMA foreign_keys = OFF;")
	} else if err := e.disableConstraintChecking(); err != nil {
		return err
	}
	return e.Editor.Begin()
}

// Finish runs the deferred SQL, checks the foreign keys, commits and
// re-enables foreign key checks.
//
// django: sqlite3/schema.py DatabaseSchemaEditor.__exit__
func (e *Editor) Finish(err error) error {
	err = e.Editor.Finish(err)
	if e.CollectSQL() {
		e.AddCollected("PRAGMA foreign_keys = ON;")
		return err
	}
	if perr := e.Conn().Exec("PRAGMA foreign_keys = ON"); perr != nil && err == nil {
		err = perr
	}
	return err
}

// disableConstraintChecking turns foreign key checks off and verifies that
// they really are off (they cannot be changed inside a transaction).
//
// django: sqlite3/base.py DatabaseWrapper.disable_constraint_checking
func (e *Editor) disableConstraintChecking() error {
	if err := e.Conn().Exec("PRAGMA foreign_keys = OFF"); err != nil {
		return err
	}
	var enabled int
	if err := e.Conn().DB().Raw("PRAGMA foreign_keys").Row().Scan(&enabled); err != nil {
		return err
	}
	if enabled != 0 {
		return &m.NotSupportedError{Msg: "SQLite schema editor cannot be used while foreign key constraint checks are enabled. Make sure to disable them before entering a transaction.atomic() context because SQLite does not support disabling them in the middle of a multi-statement transaction"}
	}
	return nil
}

// checkConstraints reports rows with invalid foreign key references that
// were entered while the constraint checks were off.
//
// django: sqlite3/base.py DatabaseWrapper.check_constraints
func (e *Editor) checkConstraints() error {
	if e.CollectSQL() {
		return nil
	}
	type violation struct {
		table   string
		rowid   any
		refTo   string
		fkIndex int
	}
	rows, err := e.Conn().Rows("PRAGMA foreign_key_check")
	if err != nil {
		return err
	}
	var vs []violation
	for rows.Next() {
		var v violation
		if err := rows.Scan(&v.table, &v.rowid, &v.refTo, &v.fkIndex); err != nil {
			rows.Close()
			return err
		}
		vs = append(vs, v)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(vs) == 0 {
		return nil
	}
	v := vs[0]
	intro := &Introspection{Conn: e.Conn()}
	rels, err := intro.foreignKeyList(v.table)
	if err != nil {
		return err
	}
	column, refColumn := "", ""
	for _, r := range rels {
		if r.ID == v.fkIndex {
			column, refColumn = r.From, r.To
			break
		}
	}
	return fmt.Errorf("the row in table '%s' with rowid '%v' has an invalid foreign key: %s.%s does not have a corresponding value in %s.%s",
		v.table, v.rowid, v.table, column, v.refTo, refColumn)
}

// canAlterTableDropColumn reports whether the server supports ALTER TABLE
// ... DROP COLUMN.
//
// django: sqlite3/features.py DatabaseFeatures.can_alter_table_drop_column
func (e *Editor) canAlterTableDropColumn() bool {
	return compareVersion(e.Conn().Version, dropColumnVersion) >= 0
}

// ColumnSQL returns the column definition.
//
// Django spells a column "<type> DEFAULT <x> NOT NULL"; gorm's
// FullDataTypeOf spells it "<type> NOT NULL DEFAULT <x>". SQLite stores
// the CREATE TABLE statement verbatim and gorm's migrator reads the
// column back by parsing that text: with Django's order gorm reads the
// default as "<x> NOT NULL" and re-alters the column on every
// AutoMigrate. The clauses are therefore emitted in gorm's order.
//
// django: base/schema.py BaseDatabaseSchemaEditor._iter_column_sql
func (e *Editor) ColumnSQL(model *m.Model, f *m.ModelField, includeDefault bool) (string, error) {
	typ, err := e.ColumnType(model, f)
	if err != nil {
		return "", err
	}
	parts := []string{typ}
	if !f.Field.Null {
		parts = append(parts, "NOT NULL")
	}
	switch {
	case f.Field.DBDefault != nil:
		d, err := e.Self().DBDefaultSQL(f)
		if err != nil {
			return "", err
		}
		parts = append(parts, "DEFAULT "+d)
	case includeDefault && !e.Self().SkipDefault(f) && !(f.Field.Null && e.Self().SkipDefaultOnAlter(f)):
		if v := base.EffectiveDefault(f); v != nil {
			q, err := e.QuoteValue(v)
			if err != nil {
				return "", err
			}
			parts = append(parts, "DEFAULT "+q)
		}
	}
	return strings.Join(parts, " "), nil
}

// DBDefaultSQL renders a database default the way gorm's
// Migrator.FullDataTypeOf does: gorm explains the value with the SQLite
// dialector, whose escaper is a double quote, so a string default reads
// DEFAULT "x" (SQLite accepts a double-quoted string literal there). The
// schema gormgate creates is therefore identical to gorm's, down to the
// text PRAGMA table_info reports as the default.
//
// django: base/schema.py BaseDatabaseSchemaEditor.db_default_sql
func (e *Editor) DBDefaultSQL(f *m.ModelField) (string, error) {
	d := f.Field.DBDefault
	if d.Expr != "" {
		return d.Expr, nil
	}
	return gormLiteral(d.Value)
}

// gormLiteralOptions render a value like gorm's logger.ExplainSQL with the
// double quote escaper the SQLite dialector passes.
var gormLiteralOptions = base.GormLiteralOptions{Escaper: `"`}

// gormLiteral renders a value as gorm's sqlite dialector would.
func gormLiteral(v any) (string, error) { return base.GormLiteral(v, gormLiteralOptions) }

// dbDefaultValueSQL renders a database default as an SQL value (not as the
// DDL text gorm writes), for the data-copying INSERT of a remake.
func (e *Editor) dbDefaultValueSQL(f *m.ModelField) (string, error) {
	d := f.Field.DBDefault
	if d.Expr != "" {
		return d.Expr, nil
	}
	return e.QuoteValue(d.Value)
}

// AddField creates a field on a model.
//
// ALTER TABLE ADD COLUMN cannot express primary keys, unique constraints,
// NOT NULL columns without a default, one-off defaults (they would have to
// be dropped afterwards, which SQLite cannot do), non-constant database
// defaults, or the named foreign key constraint gorm writes; everything
// else remakes the table.
//
// django: sqlite3/schema.py DatabaseSchemaEditor.add_field
func (e *Editor) AddField(model *m.Model, f *m.ModelField) error {
	if f.Field.PrimaryKey ||
		f.Field.Unique ||
		!f.Field.Null ||
		base.EffectiveDefault(f) != nil ||
		(f.Field.DBDefault != nil && f.Field.DBDefault.Expr != "") ||
		f.Field.ForeignKey != nil {
		return e.remakeTable(model, f, nil, nil)
	}
	return e.Editor.AddField(model, f)
}

// RemoveField removes a field from a model.
//
// django: sqlite3/schema.py DatabaseSchemaEditor.remove_field
func (e *Editor) RemoveField(model *m.Model, f *m.ModelField) error {
	if e.canAlterTableDropColumn() &&
		// Primary keys, unique fields, indexed fields, fields used by a
		// table constraint and foreign keys are not supported in ALTER
		// TABLE DROP COLUMN.
		!f.Field.PrimaryKey &&
		!f.Field.Unique &&
		!columnIsIndexed(model, f.Column) &&
		!columnInConstraint(model, f.Column) &&
		f.Field.ForeignKey == nil {
		return e.Editor.RemoveField(model, f)
	}
	return e.remakeTable(model, nil, f, nil)
}

// columnIsIndexed reports whether an index of the model covers the column
// (Django's field.db_index, which gormgate keeps in Options.Indexes).
func columnIsIndexed(model *m.Model, column string) bool {
	for _, ix := range model.Options().Indexes {
		for _, f := range ix.Fields {
			if f.Column == column {
				return true
			}
			if f.Expression != "" && mentionsColumn(f.Expression, column) {
				return true
			}
		}
		if ix.Where != "" && mentionsColumn(ix.Where, column) {
			return true
		}
	}
	for _, ut := range model.Options().UniqueTogether {
		for _, c := range ut {
			if c == column {
				return true
			}
		}
	}
	return false
}

// columnInConstraint reports whether a table constraint of the model uses
// the column.
func columnInConstraint(model *m.Model, column string) bool {
	for _, c := range model.Options().Constraints {
		switch x := c.(type) {
		case *m.CheckConstraint:
			if mentionsColumn(x.Check, column) {
				return true
			}
		case *m.UniqueConstraint:
			for _, f := range append(append([]string(nil), x.Fields...), x.Include...) {
				if f == column {
					return true
				}
			}
			if mentionsColumn(x.Condition, column) {
				return true
			}
		case *m.ForeignKeyConstraint:
			for _, f := range x.Fields {
				if f == column {
					return true
				}
			}
		}
	}
	return false
}

// mentionsColumn reports whether an SQL fragment contains the column name
// as a whole word (quoted or not).
func mentionsColumn(sql, column string) bool {
	if column == "" {
		return false
	}
	isWord := func(b byte) bool {
		return b == '_' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
	}
	for i := 0; i+len(column) <= len(sql); i++ {
		if sql[i:i+len(column)] != column {
			continue
		}
		if i > 0 && isWord(sql[i-1]) {
			continue
		}
		if j := i + len(column); j < len(sql) && isWord(sql[j]) {
			continue
		}
		return true
	}
	return false
}

// PerformAlterField performs a "physical" field update.
//
// django: sqlite3/schema.py DatabaseSchemaEditor._alter_field
func (e *Editor) PerformAlterField(model *m.Model, old, new *m.ModelField, oldType, newType string, strict bool) error {
	// Use "ALTER TABLE ... RENAME COLUMN" if only the column name changed
	// and there aren't any constraints.
	if old.Column != new.Column && old.Field.ForeignKey == nil && new.Field.ForeignKey == nil {
		oldSQL, err := e.Self().ColumnSQL(model, old, false)
		if err != nil {
			return err
		}
		newSQL, err := e.Self().ColumnSQL(model, new, false)
		if err != nil {
			return err
		}
		if oldSQL == newSQL {
			return e.Execute(e.Self().RenameFieldSQL(model.Table, old, new, newType))
		}
	}
	// Alter by remaking the table.
	if err := e.remakeTable(model, nil, nil, [][2]*m.ModelField{{old, new}}); err != nil {
		return err
	}
	// Rebuild tables with foreign keys pointing to this field.
	if !(new.Field.Unique || new.Field.PrimaryKey) || oldType == newType {
		return nil
	}
	done := map[string]bool{model.Table: true}
	for _, rel := range new.Model.RelatedFields() {
		if rel.RemoteField == nil || rel.RemoteField.Column != new.Column {
			continue
		}
		// Ignore self-relationships since the table was already rebuilt.
		if done[rel.Model.Table] {
			continue
		}
		done[rel.Model.Table] = true
		if err := e.remakeTable(rel.Model, nil, nil, nil); err != nil {
			return err
		}
	}
	return nil
}

// remakeOnly reports whether a constraint has to be part of CREATE TABLE
// (and therefore needs a table remake) instead of being a unique index.
//
// django: sqlite3/schema.py DatabaseSchemaEditor.add_constraint
func remakeOnly(c m.Constraint) bool {
	// A check constraint and a foreign key can only be declared inside
	// CREATE TABLE on SQLite, so both need the table to be remade.
	u, ok := c.(*m.UniqueConstraint)
	if !ok {
		return true
	}
	return u.Condition == "" && len(u.Include) == 0 && u.Deferrable == ""
}

// AddConstraint adds a table constraint.
//
// django: sqlite3/schema.py DatabaseSchemaEditor.add_constraint
func (e *Editor) AddConstraint(model *m.Model, c m.Constraint) error {
	if !remakeOnly(c) {
		return e.Editor.AddConstraint(model, c)
	}
	return e.remakeTable(model, nil, nil, nil)
}

// RemoveConstraint drops a table constraint.
//
// django: sqlite3/schema.py DatabaseSchemaEditor.remove_constraint
func (e *Editor) RemoveConstraint(model *m.Model, c m.Constraint) error {
	if !remakeOnly(c) {
		return e.Editor.RemoveConstraint(model, c)
	}
	return e.remakeTable(model, nil, nil, nil)
}

// CompositeFKSQL renders a table-level foreign key.
//
// While a table is being remade under a temporary name, a key that points
// back at the table itself has to name the temporary table: SQLite rewrites
// the REFERENCES clause of the copy when it is renamed back, exactly as it
// does for a self-referential field.
func (e *Editor) CompositeFKSQL(model *m.Model, c *m.ForeignKeyConstraint) (*base.Statement, error) {
	st, err := e.Editor.CompositeFKSQL(model, c)
	if err != nil {
		return nil, err
	}
	if e.remakeTemp != "" && model.Table == e.remakeTemp {
		// A self-referencing key on the table being remade has to point
		// at the temporary copy, not at the original.
		for _, t := range st.Tables() {
			if t.Name == e.remakeOrig {
				t.Name = e.remakeTemp
			}
		}
	}
	return st, nil
}

// mapping is the ordered column -> value map of the data-copying INSERT.
type mapping struct {
	cols  []string
	exprs []string
}

func (mp *mapping) set(col, expr string) {
	if i := slices.Index(mp.cols, col); i >= 0 {
		mp.exprs[i] = expr
		return
	}
	mp.cols = append(mp.cols, col)
	mp.exprs = append(mp.exprs, expr)
}

// replace overwrites the entry of oldCol with (newCol, expr), keeping its
// position.
func (mp *mapping) replace(oldCol, newCol, expr string) {
	i := slices.Index(mp.cols, oldCol)
	if i < 0 {
		mp.set(newCol, expr)
		return
	}
	mp.cols[i], mp.exprs[i] = newCol, expr
}

func (mp *mapping) remove(col string) {
	if i := slices.Index(mp.cols, col); i >= 0 {
		mp.cols = slices.Delete(mp.cols, i, i+1)
		mp.exprs = slices.Delete(mp.exprs, i, i+1)
	}
}

// remakeTable transforms a table by creating a new one with the updated
// definition, copying the data over, dropping the old table and renaming
// the new one, as SQLite's documentation prescribes:
// https://www.sqlite.org/lang_altertable.html#caution
//
// django: sqlite3/schema.py DatabaseSchemaEditor._remake_table
func (e *Editor) remakeTable(model *m.Model, createField, deleteField *m.ModelField, alterFields [][2]*m.ModelField) error {
	q := e.QuoteName
	clone := func(f *m.ModelField) *m.ModelField {
		c := *f
		c.Field = f.Field.Clone()
		return &c
	}
	// Work out the new fields and the copy map.
	var body []*m.ModelField
	mp := &mapping{}
	for _, f := range model.Fields {
		body = append(body, clone(f))
		mp.set(f.Column, q(f.Column))
	}
	indexOf := func(name string) int {
		for i, f := range body {
			if f.Name == name {
				return i
			}
		}
		return -1
	}
	// If any of the new or altered fields is introducing a new primary
	// key, remove the old one.
	introducesPK := createField != nil && createField.Field.PrimaryKey
	for _, af := range alterFields {
		introducesPK = introducesPK || af[1].Field.PrimaryKey
	}
	if introducesPK {
		for _, f := range body {
			if !f.Field.PrimaryKey {
				continue
			}
			// Do not remove the old primary key when an altered field that
			// introduces a primary key is the same field.
			altered := false
			for _, af := range alterFields {
				altered = altered || af[1].Name == f.Name
			}
			if !altered {
				f.Field.PrimaryKey = false
			}
		}
	}
	// Add in any created field.
	if createField != nil {
		body = append(body, clone(createField))
		if createField.Field.DBDefault == nil {
			def, err := e.QuoteValue(base.EffectiveDefault(createField))
			if err != nil {
				return err
			}
			mp.set(createField.Column, def)
		}
	}
	// Add in any altered fields.
	renames := map[string]string{}
	for _, af := range alterFields {
		old, new := af[0], af[1]
		nf := clone(new)
		if i := indexOf(old.Name); i >= 0 {
			body[i] = nf
		} else {
			body = append(body, nf)
		}
		renames[old.Name] = new.Name
		expr := q(old.Column)
		if old.Field.Null && !new.Field.Null {
			var def string
			var err error
			if new.Field.DBDefault == nil {
				def, err = e.QuoteValue(base.EffectiveDefault(new))
			} else {
				def, err = e.dbDefaultValueSQL(new)
			}
			if err != nil {
				return err
			}
			expr = fmt.Sprintf("coalesce(%s, %s)", q(old.Column), def)
		}
		mp.replace(old.Column, new.Column, expr)
	}
	// Remove any deleted field.
	if deleteField != nil {
		if i := indexOf(deleteField.Name); i >= 0 {
			body = append(body[:i], body[i+1:]...)
		}
		mp.remove(deleteField.Column)
	}

	// Work out the new options, taking renames into account.
	opts := model.Options().Clone()
	rename := func(n string) string {
		if nn, ok := renames[n]; ok {
			return nn
		}
		return n
	}
	for _, ut := range opts.UniqueTogether {
		for i, n := range ut {
			ut[i] = rename(n)
		}
	}
	// Django only renames unique_together; renaming the columns of the
	// indexes too keeps them valid when a renamed field is remade.
	var indexes []m.Index
	for _, ix := range opts.Indexes {
		if deleteField != nil && indexCovers(ix, deleteField.Column) {
			continue
		}
		for i := range ix.Fields {
			ix.Fields[i].Column = rename(ix.Fields[i].Column)
		}
		indexes = append(indexes, ix)
	}
	opts.Indexes = indexes

	// Construct the model with the renamed table.
	newTable := "new__" + model.Table
	state := &m.ModelState{App: model.App, Name: model.Name, Table: newTable, Options: opts}
	for _, f := range body {
		state.Fields = append(state.Fields, m.NamedField{Name: f.Name, Field: f.Field})
	}
	newModel := m.NewModel(model, state)
	for _, f := range body {
		newModel.Fields = append(newModel.Fields, &m.ModelField{
			Model: newModel, Name: f.Name, Column: f.Column, Field: f.Field,
			Remote: f.Remote, RemoteField: f.RemoteField,
		})
	}
	// Self-referential fields must point at the new model so that their
	// REFERENCES clause names the new table (SQLite rewrites it when the
	// table is renamed back).
	for _, f := range newModel.Fields {
		if f.Remote == nil || f.Remote.Table != model.Table {
			continue
		}
		f.Remote = newModel
		if f.RemoteField != nil {
			target := rename(f.RemoteField.Name)
			for _, nf := range newModel.Fields {
				if nf.Name == target {
					f.RemoteField = nf
					break
				}
			}
		}
	}

	// Create a new table with the updated schema.
	e.remakeTemp, e.remakeOrig = newTable, model.Table
	err := e.CreateModel(newModel)
	e.remakeTemp, e.remakeOrig = "", ""
	if err != nil {
		return err
	}
	// Copy the data from the old table into the new table.
	if len(mp.cols) > 0 {
		cols := make([]string, len(mp.cols))
		for i, c := range mp.cols {
			cols[i] = q(c)
		}
		if err := e.Execute(fmt.Sprintf("INSERT INTO %s (%s) SELECT %s FROM %s",
			q(newTable), strings.Join(cols, ", "), strings.Join(mp.exprs, ", "), q(model.Table))); err != nil {
			return err
		}
	}
	// Delete the old table to make way for the new one.
	if err := e.Editor.DeleteModel(model); err != nil {
		return err
	}
	// Rename the new table to make way for the old one.
	if err := e.AlterDBTable(newModel, newTable, model.Table); err != nil {
		return err
	}
	// Run the deferred SQL on the correct table.
	for _, s := range e.Deferred() {
		if err := e.Execute(s.String()); err != nil {
			return err
		}
	}
	e.RemoveDeferred(func(*base.Statement) bool { return true })
	return nil
}

// indexCovers reports whether an index uses the column.
func indexCovers(ix m.Index, column string) bool {
	for _, f := range ix.Fields {
		if f.Column == column {
			return true
		}
		if f.Expression != "" && mentionsColumn(f.Expression, column) {
			return true
		}
	}
	return ix.Where != "" && mentionsColumn(ix.Where, column)
}

// Grammar is SQLite's spelling of the statements gormgate emits.
type Grammar struct{ base.BaseGrammar }

// TableComment and ColumnComment render nothing: SQLite has no comments,
// on a table or on a column.
func (Grammar) TableComment(base.TableComment) string   { return "" }
func (Grammar) ColumnComment(base.ColumnComment) string { return "" }

// AddUnique creates an index: a unique constraint cannot be added to an
// existing table here, and dropping it is dropping that index.
func (Grammar) AddUnique(c base.AddUnique) string {
	return "CREATE UNIQUE INDEX " + c.Name + " ON " + c.Table + " (" + c.Columns + ")"
}

func (Grammar) DropUnique(c base.DropConstraint) string { return "DROP INDEX " + c.Name }
