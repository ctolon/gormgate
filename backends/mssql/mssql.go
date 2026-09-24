// Package mssql is the Microsoft SQL Server backend.
//
// Django has no SQL Server backend; the reference implementation ported here
// is mssql-django (github.com/microsoft/mssql-django), whose files are cited
// as "django: mssql/<file>.py".
package mssql

import (
	"encoding/hex"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ctolon/gormgate/backends/base"
	m "github.com/ctolon/gormgate/migrations"
)

// Features are SQL Server's DatabaseFeatures values.
//
// django: mssql/features.py DatabaseFeatures
var Features = base.Features{
	SupportsTransactions: true,
	// DDL is transactional on SQL Server: a CREATE TABLE inside a rolled
	// back transaction leaves no table behind (verified on 2022).
	CanRollbackDDL: true,
	// ALTER TABLE takes a single ALTER COLUMN clause.
	SupportsCombinedAlters:    false,
	SupportsForeignKeys:       true,
	SupportsUniqueConstraints: true,
	CanCreateInlineFK:         false,
	// Comments are extended properties, never part of the column DDL.
	SupportsComments:       true,
	SupportsCommentsInline: false,
	// Filtered indexes.
	SupportsPartialIndexes: true,
	// Only computed columns can be indexed, not expressions.
	SupportsExpressionIndexes: false,
	// INCLUDE columns.
	SupportsCoveringIndexes:                true,
	SupportsDeferrableUniqueConstraints:    false,
	SupportsNullsDistinctUniqueConstraints: false,
	SupportsIndexColumnOrdering:            true,
	SupportsTableCheckConstraints:          true,
	// sp_rename ... 'INDEX'.
	CanRenameIndex:                        true,
	InterpretsEmptyStringsAsNulls:         false,
	AllowsMultipleConstraintsOnSameFields: true,
	// The default collation is case insensitive.
	IgnoresTableNameCase: true,
	// IDENTITY is not a column property ALTER TABLE can add or remove.
	CannotAlterAutoIncrement: true,
	// A UNIQUE constraint treats NULLs as equal: only one row of a nullable
	// unique column may be NULL.
	UniqueConstraintsRejectMultipleNulls: true,
}

// Backend is the SQL Server backend definition.
var Backend = &base.Backend{
	Vendor:           "microsoft",
	DisplayName:      "SQL Server",
	Features:         Features,
	MaxNameLength:    128,
	NewIntrospection: func(c *base.Conn) base.Introspection { return &Introspection{Conn: c} },
	SequenceResetSQL: SequenceResetSQL,
	Ops:              Ops{},
}

// minVersion is the oldest supported major version (SQL Server 2016, which
// introduced DROP CONSTRAINT IF EXISTS and is mssql-django's minimum).
const minVersion = 13

func init() {
	Backend.NewEditor = func(c *base.Conn, collect, atomic bool) base.SchemaEditor {
		return NewEditor(c, collect, atomic)
	}
	base.Register(base.Detector{
		Name:         "mssql",
		Dialector:    "sqlserver",
		VersionQuery: "SELECT CAST(SERVERPROPERTY('ProductVersion') AS nvarchar(128))",
		Check: func(c *base.Conn, version string) error {
			major, _, _ := strings.Cut(version, ".")
			if n, err := strconv.Atoi(major); err == nil && n < minVersion {
				return fmt.Errorf("gormgate: SQL Server %s is too old; %d.0 (2016) or newer is required", version, minVersion)
			}
			return nil
		},
		Backend: Backend,
	})
}

// Ops are SQL Server's DatabaseOperations helpers.
//
// django: mssql/operations.py DatabaseOperations
type Ops struct{}

// QuoteName quotes an identifier exactly like gorm's sqlserver dialector
// (Dialector.QuoteTo): double quotes, with a quote inside a part doubled,
// and dots separating the parts of a qualified name.
//
// Splitting on dots means a name that really contains a dot cannot be
// addressed: "a.b" is quoted as the two-part name "a"."b", never as one
// identifier. That is gorm's own behaviour, and gormgate has to address the
// very objects gorm's AutoMigrate creates, so it is kept.
func (Ops) QuoteName(name string) string {
	if strings.HasPrefix(name, `"`) && strings.HasSuffix(name, `"`) {
		return name
	}
	parts := strings.Split(name, ".")
	for i, p := range parts {
		parts[i] = strings.ReplaceAll(p, `"`, `""`)
	}
	return `"` + strings.Join(parts, `"."`) + `"`
}

// QuoteValue renders a literal. Strings become national (Unicode) literals.
//
// django: mssql/schema.py DatabaseSchemaEditor.quote_value
func (Ops) QuoteValue(v any) (string, error) {
	switch x := v.(type) {
	case string:
		return "N" + base.QuoteString(x), nil
	case time.Time:
		return base.QuoteString(x.Format(timeFormat)), nil
	}
	return base.StandardQuoteValue(v, base.QuoteValueOptions{
		True: "1", False: "0", TimeFormat: timeFormat, Bytes: hexLiteral,
	})
}

// timeFormat is a datetimeoffset literal (SQL Server takes at most seven
// fractional digits).
const timeFormat = "2006-01-02 15:04:05.9999999 -07:00"

// PrepareSQLScript passes the script through unsplit: SQL Server executes a
// batch of statements in one go.
//
// django: mssql/operations.py DatabaseOperations.prepare_sql_script
func (Ops) PrepareSQLScript(script string) []string {
	if strings.TrimSpace(script) == "" {
		return nil
	}
	return []string{script}
}

// StartTransactionSQL is used by sqlmigrate.
//
// django: mssql/operations.py DatabaseOperations.start_transaction_sql
func (Ops) StartTransactionSQL() string { return "BEGIN TRANSACTION;" }

// EndTransactionSQL is used by sqlmigrate.
func (Ops) EndTransactionSQL() string { return "COMMIT;" }

// SequenceResetSQL returns no statements: SQL Server's IDENTITY property has
// no sequence to reset with SQL (mssql-django's supports_sequence_reset is
// False; DBCC CHECKIDENT is the server's own tool for it).
//
// django: mssql/features.py DatabaseFeatures.supports_sequence_reset
func SequenceResetSQL(*base.Conn, base.Style, []*m.Model) ([]string, error) { return nil, nil }

// Editor is the SQL Server schema editor.
//
// django: mssql/schema.py DatabaseSchemaEditor
type Editor struct {
	*base.Editor
	schema string
}

// A method whose name does not match a step of base.Overrides is a
// method nothing calls; this makes that a build failure.
var _ base.Overrides = (*Editor)(nil)

// NewEditor builds a SQL Server schema editor.
func NewEditor(c *base.Conn, collect, atomic bool) *Editor {
	e := &Editor{}
	e.Editor = base.NewEditor(e, c, base.EditorOptions{
		CollectSQL: collect, Atomic: atomic, Grammar: Grammar{},
	})
	return e
}

// intro returns the SQL Server introspection of the connection. A backend
// embedding this one has to keep an introspection this editor can read; the
// alternative to the check is a panic in the middle of a migration.
func (e *Editor) intro() (*Introspection, error) {
	i, ok := e.Conn().Introspection().(*Introspection)
	if !ok {
		return nil, fmt.Errorf("gormgate: the SQL Server schema editor needs a *mssql.Introspection, not %T", e.Conn().Introspection())
	}
	return i, nil
}

// q quotes an identifier through the outermost editor, so that a backend
// embedding this one gets its own quoting used.
func (e *Editor) q(name string) string { return e.QuoteName(name) }

// literal renders a name as an N'...' string literal, for the system procedures
// that take object names as strings.
func literal(s string) string { return "N" + base.QuoteString(s) }

// schemaName is the default schema of the connection, needed by the extended
// property procedures (their arguments must be literals, so SCHEMA_NAME()
// cannot be used inline).
func (e *Editor) schemaName() (string, error) {
	if e.schema != "" {
		return e.schema, nil
	}
	var name string
	if err := e.Conn().DB().Raw("SELECT SCHEMA_NAME()").Row().Scan(&name); err != nil {
		return "", err
	}
	e.schema = name
	return name, nil
}

// ---------------------------------------------------------------------------
// Names
// ---------------------------------------------------------------------------

// defaultConstraintName is the name given to DEFAULT constraints that are
// added by ALTER TABLE, so that they can be dropped again without having to
// look them up. Defaults written inside CREATE TABLE keep the name SQL Server
// generates for them (DF__...), which is what gorm's AutoMigrate produces.
func (e *Editor) defaultConstraintName(table, column string) string {
	return base.CreateIndexName(e.Conn().Backend.MaxNameLength, table, []string{column}, "_df")
}

// existingDefaultConstraintName is the name of the DEFAULT constraint of a
// column as the database has it, falling back to the name this editor would
// have given it (the constraint may have been created earlier in the same
// migration, which introspection does not see while SQL is being collected).
//
// django: mssql/schema.py DatabaseSchemaEditor._sql_select_default_constraint_name
func (e *Editor) existingDefaultConstraintName(table, column string) (string, error) {
	intro, err := e.intro()
	if err != nil {
		return "", err
	}
	name, err := intro.DefaultConstraintName(table, column)
	if err != nil {
		return "", err
	}
	if name == "" {
		name = e.defaultConstraintName(table, column)
	}
	return name, nil
}

// ---------------------------------------------------------------------------
// Column definitions and defaults
// ---------------------------------------------------------------------------

// ColumnSQL returns the column definition. On the ALTER TABLE ... ADD path
// (includeDefault, the only caller being AddField) the DEFAULT constraint is
// given an explicit name so that it can be dropped again by name.
//
// django: mssql/schema.py DatabaseSchemaEditor.add_field
func (e *Editor) ColumnSQL(model *m.Model, f *m.ModelField, includeDefault bool) (string, error) {
	sql, err := e.Editor.ColumnSQL(model, f, includeDefault)
	if err != nil || !includeDefault {
		return sql, err
	}
	if f.Field.DBDefault == nil && base.EffectiveDefault(f) == nil {
		return sql, nil
	}
	i := strings.Index(sql, " DEFAULT ")
	if i < 0 {
		return sql, nil
	}
	name := e.q(e.defaultConstraintName(model.Table, f.Column))
	return sql[:i] + " CONSTRAINT " + name + sql[i:], nil
}

// DBDefaultSQL renders a database default exactly like gorm's
// Migrator.FullDataTypeOf, so that a column created by gormgate and one
// created by gorm's AutoMigrate carry the same default definition.
func (e *Editor) DBDefaultSQL(f *m.ModelField) (string, error) {
	d := f.Field.DBDefault
	if d.Expr != "" {
		return d.Expr, nil
	}
	return gormLiteral(d.Value)
}

// gormLiteralOptions render a value the way gorm's sqlserver dialector
// renders a bound variable: Dialector.Explain turns booleans into 1/0 and
// then calls logger.ExplainSQL with ' as the escaper.
var gormLiteralOptions = base.GormLiteralOptions{
	Escaper: "'", True: "1", False: "0", Bytes: hexLiteral,
}

// gormLiteral renders a value as gorm's sqlserver dialector would.
func gormLiteral(v any) (string, error) { return base.GormLiteral(v, gormLiteralOptions) }

// hexLiteral is SQL Server's binary literal.
func hexLiteral(b []byte) string { return "0x" + hex.EncodeToString(b) }

// AlterColumnDefaultSQL adds or drops the DEFAULT constraint holding the
// one-off default of a column.
//
// django: mssql/schema.py DatabaseSchemaEditor._alter_column_default_sql
func (e *Editor) AlterColumnDefaultSQL(model *m.Model, old, new *m.ModelField, drop bool) (string, error) {
	if drop {
		return e.dropDefaultSQL(model.Table, new.Column)
	}
	def, err := e.QuoteValue(base.EffectiveDefault(new))
	if err != nil {
		return "", err
	}
	return e.addDefaultSQL(model.Table, new.Column, def), nil
}

// AlterColumnDatabaseDefaultSQL adds or drops the DEFAULT constraint holding
// the database default of a column.
//
// django: mssql/schema.py DatabaseSchemaEditor._alter_column_database_default_sql
func (e *Editor) AlterColumnDatabaseDefaultSQL(model *m.Model, old, new *m.ModelField, drop bool) (string, error) {
	if drop {
		return e.dropDefaultSQL(model.Table, new.Column)
	}
	def, err := e.Self().DBDefaultSQL(new)
	if err != nil {
		return "", err
	}
	return e.addDefaultSQL(model.Table, new.Column, def), nil
}

func (e *Editor) addDefaultSQL(table, column, value string) string {
	return fmt.Sprintf("ADD CONSTRAINT %s DEFAULT %s FOR %s",
		e.q(e.defaultConstraintName(table, column)), value, e.q(column))
}

// dropDefaultSQL drops a column's DEFAULT constraint. IF EXISTS keeps the
// statement valid when the constraint was already dropped to make way for an
// ALTER COLUMN.
func (e *Editor) dropDefaultSQL(table, column string) (string, error) {
	name, err := e.existingDefaultConstraintName(table, column)
	if err != nil {
		return "", err
	}
	return "DROP CONSTRAINT IF EXISTS " + e.q(name), nil
}

// ---------------------------------------------------------------------------
// Renames
// ---------------------------------------------------------------------------

// AlterDBTable renames a table with sp_rename.
//
// django: mssql/schema.py DatabaseSchemaEditor.sql_rename_table
func (e *Editor) AlterDBTable(model *m.Model, oldTable, newTable string) error {
	if oldTable == newTable ||
		(e.Conn().Backend.Features.IgnoresTableNameCase && strings.EqualFold(oldTable, newTable)) {
		return nil
	}
	if err := e.Execute(fmt.Sprintf("EXEC sp_rename %s, %s", literal(e.q(oldTable)), literal(newTable))); err != nil {
		return err
	}
	for _, s := range e.Deferred() {
		s.RenameTableReferences(oldTable, newTable)
	}
	return nil
}

// RenameFieldSQL renames a column with sp_rename.
//
// django: mssql/schema.py DatabaseSchemaEditor.sql_rename_column
func (e *Editor) RenameFieldSQL(table string, old, new *m.ModelField, newType string) string {
	return fmt.Sprintf("EXEC sp_rename %s, %s, 'COLUMN'",
		literal(e.q(table)+"."+e.q(old.Column)), literal(new.Column))
}

// RenameIndexSQL renames an index with sp_rename.
func (e *Editor) RenameIndexSQL(model *m.Model, oldName, newName string) *base.Statement {
	table := &base.Table{Name: model.Table, Quote: e.q}
	oldRef, newRef := literal(e.q(model.Table)+"."+e.q(oldName)), literal(newName)
	return e.Spell(base.NewStatement(table), func(g base.Grammar) string {
		return g.RenameIndex(base.RenameIndex{Table: table.String(), OldName: string(oldRef), NewName: string(newRef)})
	})
}

// ---------------------------------------------------------------------------
// Comments
// ---------------------------------------------------------------------------

// AlterDBTableComment sets the MS_Description extended property of a table.
//
// django: mssql/schema.py DatabaseSchemaEditor.sql_alter_table_comment
func (e *Editor) AlterDBTableComment(model *m.Model, oldComment, newComment string) error {
	sql, err := e.extendedPropertySQL(model.Table, "", newComment)
	if err != nil {
		return err
	}
	return e.Execute(sql)
}

// AlterColumnCommentSQL sets the MS_Description extended property of a
// column.
//
// django: mssql/schema.py DatabaseSchemaEditor.sql_alter_column_comment
func (e *Editor) AlterColumnCommentSQL(model *m.Model, f *m.ModelField, newType, comment string) (string, error) {
	return e.extendedPropertySQL(model.Table, f.Column, comment)
}

// extendedPropertySQL adds or updates the MS_Description property of a table
// (column == "") or of one of its columns.
func (e *Editor) extendedPropertySQL(table, column, comment string) (string, error) {
	schema, err := e.schemaName()
	if err != nil {
		return "", err
	}
	value, err := e.QuoteValue(comment)
	if err != nil {
		return "", err
	}
	level2, minor := "", "0"
	if column != "" {
		level2 = fmt.Sprintf(", @level2type = 'COLUMN', @level2name = %s", literal(column))
		minor = fmt.Sprintf("(SELECT column_id FROM sys.columns WHERE object_id = OBJECT_ID(%s) AND name = %s)",
			literal(table), literal(column))
	}
	args := fmt.Sprintf("@name = 'MS_Description', @value = %s, @level0type = 'SCHEMA', @level0name = %s, @level1type = 'TABLE', @level1name = %s%s",
		value, literal(schema), literal(table), level2)
	return fmt.Sprintf(`IF NOT EXISTS (SELECT 1 FROM sys.extended_properties WHERE class = 1 AND major_id = OBJECT_ID(%s) AND minor_id = %s AND name = 'MS_Description')
    EXEC sp_addextendedproperty %s
ELSE
    EXEC sp_updateextendedproperty %s`, literal(table), minor, args, args), nil
}

// ---------------------------------------------------------------------------
// Indexes
// ---------------------------------------------------------------------------

// CreateIndexSQL renders CREATE INDEX the way gorm's sqlserver migrator does:
// the filter and the raw option come last, and SQL Server has neither an
// access method nor index comments.
func (e *Editor) CreateIndexSQL(model *m.Model, ix m.Index) (*base.Statement, error) {
	st, err := e.Editor.CreateIndexSQL(model, ix)
	if err != nil {
		return nil, err
	}
	option := ""
	if ix.Option != "" {
		option = " " + ix.Option
	}
	if a := st.Index(); a != nil {
		a.Using, a.Comment, a.Option, a.Trailing = "", "", "", option
	}
	return st, nil
}

// ---------------------------------------------------------------------------
// Tables
// ---------------------------------------------------------------------------

// DeleteModel drops a table, dropping the foreign keys pointing at it first:
// SQL Server refuses to drop a table that is still referenced.
//
// django: mssql/schema.py DatabaseSchemaEditor.sql_delete_table
func (e *Editor) DeleteModel(model *m.Model) error {
	intro, err := e.intro()
	if err != nil {
		return err
	}
	fks, err := intro.ReferencingForeignKeys(model.Table)
	if err != nil {
		return err
	}
	for _, fk := range fks {
		if fk.Table == model.Table || e.IsDropped(fk.Table, fk.Name) {
			continue // dropped with the table, or dropped earlier already
		}
		fkTable, fkName := &base.Table{Name: fk.Table, Quote: e.q}, e.q(fk.Name)
		st := e.Spell(base.NewStatement(fkTable), func(g base.Grammar) string {
			return g.DropForeignKey(base.DropConstraint{Table: fkTable.String(), Name: fkName})
		})
		if err := e.ExecDrop(fk.Table, fk.Name, st); err != nil {
			return err
		}
	}
	return e.Editor.DeleteModel(model)
}

// ---------------------------------------------------------------------------
// Columns
// ---------------------------------------------------------------------------

// RemoveField drops a column. SQL Server refuses to drop a column that is
// still used by a default constraint, a check constraint, an index, a unique
// constraint, the primary key or a foreign key, so all of them are dropped
// first.
//
// django: mssql/schema.py DatabaseSchemaEditor.remove_field
func (e *Editor) RemoveField(model *m.Model, f *m.ModelField) error {
	if _, err := e.dropColumnObjects(model, f.Column, true); err != nil {
		return err
	}
	if err := e.dropForeignKeysOn(model, f.Column); err != nil {
		return err
	}
	return e.Editor.RemoveField(model, f)
}

// savedObjects holds the objects dropped to make an ALTER COLUMN possible,
// with everything needed to create them again.
type savedObjects struct {
	indexes []IndexDefinition
	checks  []CheckDefinition
	// incoming are foreign keys of other tables that pointed at the column.
	incoming []ForeignKeyDefinition
	// defaultDropped reports whether the column's DEFAULT constraint was
	// dropped.
	defaultDropped bool
}

// dropColumnObjects drops everything that depends on a column and would make
// an ALTER TABLE on it fail: the indexes (including those backing the primary
// key and unique constraints), the foreign keys pointing at the column, the
// check constraints covering it and, when withDefault, its default
// constraint.
//
// django: mssql/schema.py DatabaseSchemaEditor._delete_indexes,
// _delete_unique_constraints
func (e *Editor) dropColumnObjects(model *m.Model, column string, withDefault bool) (*savedObjects, error) {
	saved := &savedObjects{}
	intro, err := e.intro()
	if err != nil {
		return nil, err
	}
	ixs, err := intro.Indexes(model.Table)
	if err != nil {
		return nil, err
	}
	for _, ix := range ixs {
		if !slices.Contains(ix.Columns, column) && !slices.Contains(ix.Included, column) {
			continue
		}
		if e.IsDropped(model.Table, ix.Name) {
			continue
		}
		saved.indexes = append(saved.indexes, ix)
	}
	// A primary key or unique constraint cannot be dropped while a foreign
	// key depends on it, so when any of the indexes about to go backs one,
	// the foreign keys pointing at the column are dropped first.
	backsAKey := func(ix IndexDefinition) bool {
		return ix.PrimaryKey || ix.UniqueConstraint || ix.Unique
	}
	if slices.ContainsFunc(saved.indexes, backsAKey) {
		fks, err := intro.ReferencingForeignKeys(model.Table)
		if err != nil {
			return nil, err
		}
		for _, fk := range fks {
			if !slices.Contains(fk.ToColumns, column) || e.IsDropped(fk.Table, fk.Name) {
				continue
			}
			saved.incoming = append(saved.incoming, fk)
			inTable, inName := &base.Table{Name: fk.Table, Quote: e.q}, e.q(fk.Name)
			st := e.Spell(base.NewStatement(inTable), func(g base.Grammar) string {
				return g.DropForeignKey(base.DropConstraint{Table: inTable.String(), Name: inName})
			})
			if err := e.ExecDrop(fk.Table, fk.Name, st); err != nil {
				return nil, err
			}
		}
	}
	for _, ix := range saved.indexes {
		if err := e.dropIndexObject(model, ix); err != nil {
			return nil, err
		}
	}
	checks, err := intro.Checks(model.Table)
	if err != nil {
		return nil, err
	}
	for _, c := range checks {
		if !slices.Contains(c.Columns, column) || e.IsDropped(model.Table, c.Name) {
			continue
		}
		saved.checks = append(saved.checks, c)
		if err := e.ExecDrop(model.Table, c.Name, e.DeleteCheckSQL(model, c.Name)); err != nil {
			return nil, err
		}
	}
	if withDefault {
		name, err := intro.DefaultConstraintName(model.Table, column)
		if err != nil {
			return nil, err
		}
		if name != "" && !e.IsDropped(model.Table, name) {
			saved.defaultDropped = true
			dcTable, dcName := &base.Table{Name: model.Table, Quote: e.q}, e.q(name)
			st := e.Spell(base.NewStatement(dcTable), func(g base.Grammar) string {
				return g.DropConstraint(base.DropConstraint{Table: dcTable.String(), Name: dcName})
			})
			if err := e.ExecDrop(model.Table, name, st); err != nil {
				return nil, err
			}
		}
	}
	return saved, nil
}

// dropIndexObject drops one index, using ALTER TABLE for the indexes owned by
// a primary key or unique constraint.
func (e *Editor) dropIndexObject(model *m.Model, ix IndexDefinition) error {
	drop := func(g base.Grammar, c base.DropConstraint) string {
		return g.DropIndex(base.DropIndex{Table: c.Table, Name: c.Name})
	}
	switch {
	case ix.PrimaryKey:
		drop = base.Grammar.DropPrimaryKey
	case ix.UniqueConstraint:
		drop = base.Grammar.DropUnique
	}
	ixTable, ixName := &base.Table{Name: model.Table, Quote: e.q}, e.q(ix.Name)
	st := e.Spell(base.NewStatement(ixTable), func(g base.Grammar) string {
		return drop(g, base.DropConstraint{Table: ixTable.String(), Name: ixName})
	})
	return e.ExecDrop(model.Table, ix.Name, st)
}

// dropForeignKeysOn drops the foreign keys declared on a column.
func (e *Editor) dropForeignKeysOn(model *m.Model, column string) error {
	intro, err := e.intro()
	if err != nil {
		return err
	}
	fks, err := intro.ForeignKeys(model.Table)
	if err != nil {
		return err
	}
	for _, fk := range fks {
		if !slices.Contains(fk.Columns, column) || e.IsDropped(model.Table, fk.Name) {
			continue
		}
		if err := e.ExecDrop(model.Table, fk.Name, e.DeleteFKSQL(model, fk.Name)); err != nil {
			return err
		}
	}
	return nil
}

// PerformAlterField is Django's _alter_field with SQL Server's restrictions:
// the IDENTITY property cannot be altered at all, and every index, key and
// constraint depending on a column has to be dropped before its type or
// nullability can be changed, and created again afterwards.
//
// django: mssql/schema.py DatabaseSchemaEditor._alter_field
func (e *Editor) PerformAlterField(model *m.Model, old, new *m.ModelField, oldType, newType string, strict bool) error {
	if old.Field.AutoIncrement != new.Field.AutoIncrement {
		return m.NotSupportedf(
			"SQL Server cannot add or remove the IDENTITY property of an existing column: %s.%s has to be removed and added again",
			model.Table, new.Column)
	}
	typeChanged := oldType != newType
	nullChanged := old.Field.Null != new.Field.Null
	// The DEFAULT constraint blocks a type change, and it has to go when the
	// database default itself changes: Django only adds the new one.
	dropDefault := typeChanged || !base.SameDBDefault(old.Field.DBDefault, new.Field.DBDefault)
	var saved *savedObjects
	if typeChanged || nullChanged || dropDefault {
		var err error
		if saved, err = e.dropColumnObjects(model, old.Column, dropDefault); err != nil {
			return err
		}
	}
	if err := e.Editor.PerformAlterField(model, old, new, oldType, newType, strict); err != nil {
		return err
	}
	if saved == nil {
		return nil
	}
	return e.restoreColumnObjects(model, old, new, saved)
}

// restoreColumnObjects recreates what dropColumnObjects removed, leaving out
// what the base editor creates itself and what the new field no longer has.
func (e *Editor) restoreColumnObjects(model *m.Model, old, new *m.ModelField, saved *savedObjects) error {
	rename := func(cols []string) []string {
		out := make([]string, len(cols))
		for i, c := range cols {
			if c == old.Column {
				c = new.Column
			}
			out[i] = c
		}
		return out
	}
	// SQL Server writes column references of filter and check expressions
	// with bracket quoting.
	renameExpr := func(sql string) string {
		if old.Column == new.Column {
			return sql
		}
		return strings.ReplaceAll(sql, "["+old.Column+"]", "["+new.Column+"]")
	}
	for _, ix := range saved.indexes {
		cols := rename(ix.Columns)
		single := len(cols) == 1 && cols[0] == new.Column
		switch {
		case ix.PrimaryKey:
			// The base editor creates the primary key when the field became
			// one and drops it when it stopped being one.
			if !old.Field.PrimaryKey || !new.Field.PrimaryKey {
				continue
			}
			clustered := " NONCLUSTERED"
			if ix.Clustered {
				clustered = " CLUSTERED"
			}
			sql := fmt.Sprintf("ALTER TABLE %s ADD CONSTRAINT %s PRIMARY KEY%s (%s)",
				e.q(model.Table), e.q(ix.Name), clustered, e.QuoteColumns(cols))
			if err := e.Execute(sql); err != nil {
				return err
			}
		case ix.UniqueConstraint:
			// A single-column unique constraint only comes back when the
			// field is still unique and did not become the primary key; the
			// base editor creates it when the field only now became unique.
			if single && (!new.Field.Unique || new.Field.PrimaryKey || e.UniqueShouldBeAdded(old, new)) {
				continue
			}
			sql := fmt.Sprintf("ALTER TABLE %s ADD CONSTRAINT %s UNIQUE (%s)",
				e.q(model.Table), e.q(ix.Name), e.QuoteColumns(cols))
			if err := e.Execute(sql); err != nil {
				return err
			}
		default:
			if err := e.Execute(e.createIndexSQL(model.Table, ix, cols, rename(ix.Included), renameExpr(ix.Filter))); err != nil {
				return err
			}
		}
	}
	for _, c := range saved.checks {
		sql := fmt.Sprintf("ALTER TABLE %s ADD CONSTRAINT %s CHECK %s",
			e.q(model.Table), e.q(c.Name), renameExpr(c.Definition))
		if err := e.Execute(sql); err != nil {
			return err
		}
	}
	// The default comes back only when it did not change; when it did, the
	// base editor has already added the new one.
	if saved.defaultDropped && new.Field.DBDefault != nil && base.SameDBDefault(old.Field.DBDefault, new.Field.DBDefault) {
		def, err := e.Self().DBDefaultSQL(new)
		if err != nil {
			return err
		}
		alter := base.AlterTable{Table: e.q(model.Table), Changes: e.addDefaultSQL(model.Table, new.Column, def)}
		sql := e.Grammar().AlterTable(alter)
		if err := e.Execute(sql); err != nil {
			return err
		}
	}
	for _, fk := range saved.incoming {
		e.DeferFK(e.foreignKeyStatement(fk, rename(fk.ToColumns)))
	}
	return nil
}

// createIndexSQL renders CREATE INDEX for an introspected index.
func (e *Editor) createIndexSQL(table string, ix IndexDefinition, columns, included []string, filter string) string {
	unique := ""
	if ix.Unique {
		unique = "UNIQUE "
	}
	clustered := "NONCLUSTERED "
	if ix.Clustered {
		clustered = "CLUSTERED "
	}
	var cols []string
	for i, c := range columns {
		s := e.q(c)
		if i < len(ix.Orders) && ix.Orders[i] == m.SortDesc {
			s += " DESC"
		}
		cols = append(cols, s)
	}
	sql := fmt.Sprintf("CREATE %s%sINDEX %s ON %s (%s)",
		unique, clustered, e.q(ix.Name), e.q(table), strings.Join(cols, ", "))
	if len(included) > 0 {
		sql += " INCLUDE (" + e.QuoteColumns(included) + ")"
	}
	if filter != "" {
		sql += " WHERE " + filter
	}
	return sql
}

// foreignKeyStatement renders the creation of an introspected foreign key
// with the same template and parts as the base editor, so that a pending
// creation of the same constraint replaces it (DeferFK).
func (e *Editor) foreignKeyStatement(fk ForeignKeyDefinition, toColumns []string) *base.Statement {
	onDelete, onUpdate := "", ""
	if fk.OnDelete != "" {
		onDelete = " ON DELETE " + string(fk.OnDelete)
	}
	if fk.OnUpdate != "" {
		onUpdate = " ON UPDATE " + string(fk.OnUpdate)
	}
	fkTable := &base.Table{Name: fk.Table, Quote: e.q}
	fkName := &base.Name{Value: fk.Name, Quote: e.q, Table: fk.Table,
		Columns: fk.Columns, ToTable: fk.ToTable, ToColumns: toColumns}
	fkCols := &base.Columns{Table: fk.Table, Names: fk.Columns, Quote: e.q}
	fkTo := &base.Table{Name: fk.ToTable, Quote: e.q}
	fkToCols := &base.Columns{Table: fk.ToTable, Names: toColumns, Quote: e.q}
	return e.SpellAs(base.NewStatement(fkTable, fkName, fkCols, fkTo, fkToCols),
		base.StatementAddForeignKey, fkName, func(g base.Grammar) string {
			return g.AddForeignKey(base.AddForeignKey{
				Table: fkTable.String(), Name: fkName.String(), Column: fkCols.String(),
				ToTable: fkTo.String(), ToColumn: fkToCols.String(),
				OnDelete: onDelete, OnUpdate: onUpdate,
			})
		})
}

// AlterColumnTypeSQL renders ALTER COLUMN. SQL Server needs the full column
// definition, so the nullability of the old field is repeated; a change of it
// is applied by a separate statement.
//
// django: mssql/schema.py DatabaseSchemaEditor._set_field_new_type_null_status
func (e *Editor) AlterColumnTypeSQL(model *m.Model, old, new *m.ModelField, newType string) (base.Fragment, []string, error) {
	var other []string
	if e.Conn().Backend.Features.SupportsComments && old.Field.Comment != new.Field.Comment {
		s, err := e.Self().AlterColumnCommentSQL(model, new, newType, new.Field.Comment)
		if err != nil {
			return base.Fragment{}, nil, err
		}
		other = append(other, s)
	}
	oldType, err := e.ColumnType(model, old)
	if err != nil {
		return base.Fragment{}, nil, err
	}
	if oldType == newType {
		return base.Fragment{}, other, nil
	}
	null := " NOT NULL"
	if old.Field.Null {
		null = " NULL"
	}
	args := base.AlterColumnType{Column: e.q(new.Column), Type: newType + null}
	return base.Fragment{SQL: e.Grammar().AlterColumnType(args)}, other, nil
}

// Grammar is SQL Server's spelling of the statements gormgate emits.
type Grammar struct{ base.BaseGrammar }

func (Grammar) AddColumn(c base.AddColumn) string {
	return "ALTER TABLE " + c.Table + " ADD " + c.Column + " " + c.Definition
}

// AlterColumnType and the two nullity clauses below restate the whole
// column: SQL Server changes any of its properties that way, so the type
// comes back in each of them.
func (Grammar) AlterColumnType(c base.AlterColumnType) string {
	return "ALTER COLUMN " + c.Column + " " + c.Type
}

func (Grammar) AlterColumnNull(c base.AlterColumnNullity) string {
	return "ALTER COLUMN " + c.Column + " " + c.Type + " NULL"
}

func (Grammar) AlterColumnNotNull(c base.AlterColumnNullity) string {
	return "ALTER COLUMN " + c.Column + " " + c.Type + " NOT NULL"
}

// DropIndex names the table: an index name is scoped to one here.
func (Grammar) DropIndex(c base.DropIndex) string {
	return "DROP INDEX " + c.Name + " ON " + c.Table
}

// RenameIndex goes through sp_rename, whose arguments the caller has
// already quoted: the old name qualified by its table, the new one bare.
func (Grammar) RenameIndex(c base.RenameIndex) string {
	return "EXEC sp_rename " + c.OldName + ", " + c.NewName + ", 'INDEX'"
}
