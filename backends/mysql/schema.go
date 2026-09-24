package mysql

import (
	"fmt"
	"strings"

	"github.com/ctolon/gormgate/backends/base"
	m "github.com/ctolon/gormgate/migrations"
)

// django: db/backends/mysql/schema.py DatabaseSchemaEditor

// limitedDataTypes are the column types MySQL cannot give a plain default.
//
// django: mysql/base.py DatabaseWrapper._limited_data_types
var limitedDataTypes = map[string]bool{
	"tinyblob": true, "blob": true, "mediumblob": true, "longblob": true,
	"tinytext": true, "text": true, "mediumtext": true, "longtext": true,
	"json": true,
}

// Editor is the MySQL / MariaDB schema editor.
type Editor struct {
	*base.Editor
	// Server is the server the editor talks to.
	Server Server
}

// A method whose name does not match a step of base.Overrides is a
// method nothing calls; this makes that a build failure.
var _ base.Overrides = (*Editor)(nil)

// NewEditor builds a MySQL schema editor.
func NewEditor(c *base.Conn, s Server, collect, atomic bool) *Editor {
	e := &Editor{Server: s}
	e.Editor = base.NewEditor(e, c, base.EditorOptions{
		CollectSQL: collect, Atomic: atomic, Grammar: Grammar{MariaDB: s.MariaDB},
	})
	return e
}

// NewEmbeddedEditor builds a MySQL editor for a backend that embeds it.
// outer is that backend's own editor, so the shared algorithms reach its
// overrides rather than MySQL's; TiDB is built this way.
func NewEmbeddedEditor(outer base.Overrides, c *base.Conn, s Server, opts base.EditorOptions) *Editor {
	e := &Editor{Server: s}
	// An embedding backend that spells nothing differently gets MySQL's
	// grammar; TiDB is that backend.
	if opts.Grammar == nil {
		opts.Grammar = Grammar{MariaDB: s.MariaDB}
	}
	e.Editor = base.NewEditor(outer, c, opts)
	return e
}

// stripNullSuffix removes the trailing " NULL" gorm's mysql driver appends
// to the type of a nullable datetime column, so that the NULL / NOT NULL
// attribute of a column definition can be set explicitly.
func stripNullSuffix(t string) string {
	s := strings.TrimRight(t, " \t")
	if len(s) > 5 && strings.EqualFold(s[len(s)-5:], " NULL") &&
		!(len(s) >= 9 && strings.EqualFold(s[len(s)-9:], " NOT NULL")) {
		return strings.TrimRight(s[:len(s)-5], " \t")
	}
	return t
}

// isLimitedDataType reports whether the column type cannot carry a plain
// DEFAULT clause.
//
// django: mysql/schema.py DatabaseSchemaEditor._is_limited_data_type
func (e *Editor) isLimitedDataType(f *m.ModelField) (bool, error) {
	t, err := e.ColumnType(f.Model, f)
	if err != nil {
		return false, err
	}
	return limitedDataTypes[strings.ToLower(stripNullSuffix(t))], nil
}

// isTextOrBlob reports whether the column type is a TEXT or BLOB variant.
//
// django: mysql/schema.py DatabaseSchemaEditor._is_text_or_blob
func (e *Editor) isTextOrBlob(f *m.ModelField) (bool, error) {
	t, err := e.ColumnType(f.Model, f)
	if err != nil {
		return false, err
	}
	t = strings.ToLower(stripNullSuffix(t))
	return strings.HasSuffix(t, "blob") || strings.HasSuffix(t, "text"), nil
}

// defaultIsEmpty reports whether the field's effective default is the empty
// string or the empty byte slice.
func defaultIsEmpty(f *m.ModelField) bool {
	switch v := base.EffectiveDefault(f).(type) {
	case string:
		return v == ""
	case []byte:
		return len(v) == 0
	}
	return false
}

// SkipDefault reports whether the column definition must leave the default
// out.
//
// django: mysql/schema.py DatabaseSchemaEditor.skip_default
func (e *Editor) SkipDefault(f *m.ModelField) bool {
	if defaultIsEmpty(f) {
		if blob, err := e.isTextOrBlob(f); err == nil && blob {
			return true
		}
	}
	if !SupportsExpressionDefaults(e.Server) {
		limited, err := e.isLimitedDataType(f)
		return err == nil && limited
	}
	return false
}

// SkipDefaultOnAlter reports whether an ALTER may not carry the default.
//
// django: mysql/schema.py DatabaseSchemaEditor.skip_default_on_alter
func (e *Editor) SkipDefaultOnAlter(f *m.ModelField) bool {
	if defaultIsEmpty(f) {
		if blob, err := e.isTextOrBlob(f); err == nil && blob {
			return true
		}
	}
	if e.Server.MariaDB {
		return false
	}
	// MySQL doesn't support defaults for BLOB and TEXT in the ALTER COLUMN
	// statement.
	limited, err := e.isLimitedDataType(f)
	return err == nil && limited
}

// DBDefaultSQL renders a database default. MySQL only accepts a default on
// a TEXT, BLOB or JSON column when it is written as an expression, i.e. in
// parentheses.
//
// django: mysql/schema.py DatabaseSchemaEditor._column_default_sql
func (e *Editor) DBDefaultSQL(f *m.ModelField) (string, error) {
	sql, err := e.Editor.DBDefaultSQL(f)
	if err != nil {
		return "", err
	}
	if e.Server.MariaDB || !SupportsExpressionDefaults(e.Server) || f.Field.DBDefault.Expr != "" {
		return sql, nil
	}
	limited, err := e.isLimitedDataType(f)
	if err != nil {
		return "", err
	}
	if limited {
		return "(" + sql + ")", nil
	}
	return sql, nil
}

// setFieldNewType keeps the DEFAULT and NULL properties of the field in a
// MODIFY clause: MODIFY replaces the whole column definition, so anything
// left out is dropped. A property that changes is set afterwards by its own
// action.
//
// django: mysql/schema.py DatabaseSchemaEditor._set_field_new_type
func (e *Editor) setFieldNewType(f *m.ModelField, newType string) (string, error) {
	t := stripNullSuffix(newType)
	if f.Field.DBDefault != nil {
		def, err := e.Self().DBDefaultSQL(f)
		if err != nil {
			return "", err
		}
		t += " DEFAULT " + def
	}
	if f.Field.Null {
		t += " NULL"
	} else {
		t += " NOT NULL"
	}
	return t, nil
}

// commentSQL renders the inline comment clause of a column definition.
//
// django: mysql/schema.py DatabaseSchemaEditor._comment_sql
func (e *Editor) commentSQL(comment string) (string, error) {
	if comment == "" {
		return "", nil
	}
	q, err := e.QuoteValue(comment)
	if err != nil {
		return "", err
	}
	return " COMMENT " + q, nil
}

// AlterColumnTypeSQL renders the MODIFY clause that changes a column's type
// (and, because MODIFY rewrites the definition, restates its comment).
//
// django: mysql/schema.py DatabaseSchemaEditor._alter_column_type_sql
func (e *Editor) AlterColumnTypeSQL(model *m.Model, old, new *m.ModelField, newType string) (base.Fragment, []string, error) {
	typ, err := e.setFieldNewType(old, newType)
	if err != nil {
		return base.Fragment{}, nil, err
	}
	comment, err := e.commentSQL(new.Field.Comment)
	if err != nil {
		return base.Fragment{}, nil, err
	}
	args := base.AlterColumnType{Column: e.QuoteName(new.Column), Type: typ, Comment: comment}
	return base.Fragment{SQL: e.Grammar().AlterColumnType(args)}, nil, nil
}

// AlterColumnCommentSQL returns no statement: a column comment is part of
// the column definition and is written by AlterColumnTypeSQL.
//
// django: mysql/schema.py DatabaseSchemaEditor._alter_column_comment_sql
func (e *Editor) AlterColumnCommentSQL(*m.Model, *m.ModelField, string, string) (string, error) {
	return "", nil
}

// AlterColumnNullSQL renders the MODIFY clause that changes a column's
// nullability. MODIFY rewrites the whole definition, so the database
// default and the comment are restated.
//
// django: mysql/schema.py DatabaseSchemaEditor._alter_column_null_sql
func (e *Editor) AlterColumnNullSQL(model *m.Model, old, new *m.ModelField) (string, error) {
	typ, err := e.ColumnType(model, new)
	if err != nil {
		return "", err
	}
	comment, err := e.commentSQL(new.Field.Comment)
	if err != nil {
		return "", err
	}
	if new.Field.DBDefault == nil {
		args := base.AlterColumnNullity{Column: e.QuoteName(new.Column), Type: stripNullSuffix(typ)}
		if new.Field.Null {
			return e.Grammar().AlterColumnNull(args) + comment, nil
		}
		return e.Grammar().AlterColumnNotNull(args) + comment, nil
	}
	full, err := e.setFieldNewType(new, typ)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("MODIFY %s %s%s", e.QuoteName(new.Column), full, comment), nil
}

// AddField adds a column and, when the default could not be written into
// the column definition, applies it to the existing rows.
//
// django: mysql/schema.py DatabaseSchemaEditor.add_field
func (e *Editor) AddField(model *m.Model, f *m.ModelField) error {
	if err := e.Editor.AddField(model, f); err != nil {
		return err
	}
	// Simulate the effect of a one-off default.
	if !e.Self().SkipDefault(f) || f.Field.Default == nil {
		return nil
	}
	return e.Execute(fmt.Sprintf("UPDATE %s SET %s = ?",
		e.QuoteName(model.Table), e.QuoteName(f.Column)), base.EffectiveDefault(f))
}

// ExecDrop executes a constraint drop. Dropping a foreign key leaves the
// index MySQL created for it behind, which a table created from scratch
// would not have, so the index goes with the constraint.
func (e *Editor) ExecDrop(table, name string, st *base.Statement) error {
	drop := false
	if st.Kind() == base.StatementDropForeignKey {
		var err error
		if drop, err = e.hasOwnFKIndex(table, name); err != nil {
			return err
		}
	}
	if err := e.Editor.ExecDrop(table, name, st); err != nil {
		return err
	}
	if !drop {
		return nil
	}
	tableRef := &base.Table{Name: table, Quote: e.QuoteName}
	quoted := e.QuoteName(name)
	st2 := e.Spell(base.NewStatement(tableRef), func(g base.Grammar) string {
		return g.DropIndex(base.DropIndex{Table: tableRef.String(), Name: quoted})
	})
	return e.Editor.ExecDrop(table, name, st2)
}

// hasOwnFKIndex reports whether the foreign key called name owns the index
// of the same name, i.e. whether that index exists only to support it.
func (e *Editor) hasOwnFKIndex(table, name string) (bool, error) {
	cons, err := e.Conn().Introspection().Constraints(table)
	if err != nil {
		return false, err
	}
	fk, ok := cons[name]
	if !ok || !fk.Index || fk.ForeignKey == nil || len(fk.Columns) == 0 {
		return false, nil
	}
	// Another foreign key on the same leading column would keep needing it.
	for other, c := range cons {
		if other != name && c.ForeignKey != nil && len(c.Columns) > 0 && c.Columns[0] == fk.Columns[0] {
			return false, nil
		}
	}
	return true, nil
}

// createMissingFKIndex recreates the index that supports a foreign key
// before a wider index starting with the same column is dropped.
//
// MySQL can remove an implicit FK index on a field when that field is
// covered by another index like a unique_together. "covered" here means
// that the more complex index has the FK field as its first field (see
// https://bugs.mysql.com/bug.php?id=37910).
//
// django: mysql/schema.py DatabaseSchemaEditor._create_missing_fk_index
func (e *Editor) createMissingFKIndex(model *m.Model, fields []string) error {
	if len(fields) == 0 {
		return nil
	}
	first := model.Field(fields[0])
	if first == nil || first.Field.ForeignKey == nil {
		return nil
	}
	column := e.Conn().Introspection().IdentifierConverter(first.Column)
	cons, err := e.Conn().Introspection().Constraints(model.Table)
	if err != nil {
		return err
	}
	n := 0
	for _, c := range cons {
		if c.Index && len(c.Columns) > 0 && c.Columns[0] == column {
			n++
		}
	}
	// There are no other indexes that start with the FK field, only the
	// index that is expected to be deleted.
	if n != 1 {
		return nil
	}
	st, err := e.Self().CreateIndexSQL(model, m.Index{
		Name:   base.CreateIndexName(MaxNameLength, model.Table, []string{first.Column}, ""),
		Fields: []m.IndexField{{Column: first.Column}},
	})
	if err != nil {
		return err
	}
	return e.Execute(st.String())
}

// RemoveIndex drops an index, keeping the foreign key index it covered.
//
// django: mysql/schema.py DatabaseSchemaEditor.remove_index
func (e *Editor) RemoveIndex(model *m.Model, ix m.Index) error {
	if err := e.createMissingFKIndex(model, ix.Columns()); err != nil {
		return err
	}
	return e.Editor.RemoveIndex(model, ix)
}

// RemoveConstraint drops a constraint, keeping the foreign key index a
// unique constraint covered.
//
// django: mysql/schema.py DatabaseSchemaEditor.remove_constraint
func (e *Editor) RemoveConstraint(model *m.Model, c m.Constraint) error {
	if uc, ok := c.(*m.UniqueConstraint); ok {
		st, err := e.Self().CreateConstraintSQL(model, uc)
		if err != nil {
			return err
		}
		if st != nil {
			if err := e.createMissingFKIndex(model, uc.Fields); err != nil {
				return err
			}
		}
	}
	return e.Editor.RemoveConstraint(model, c)
}

// DeleteComposedIndex drops a unique_together constraint, keeping the
// foreign key index it covered.
//
// django: mysql/schema.py DatabaseSchemaEditor._delete_composed_index
func (e *Editor) DeleteComposedIndex(model *m.Model, fields []string) error {
	if err := e.createMissingFKIndex(model, fields); err != nil {
		return err
	}
	return e.Editor.DeleteComposedIndex(model, fields)
}

// Grammar is MySQL's spelling of the statements gormgate emits. MariaDB
// differs in a few statements, so the server description is a field rather
// than a separate type.
type Grammar struct {
	base.BaseGrammar
	MariaDB bool
}

func (Grammar) RenameTable(c base.RenameTable) string {
	return "RENAME TABLE " + c.OldTable + " TO " + c.NewTable
}

// AlterColumnType restates the whole column, which is how MySQL changes
// any of its properties: the type, the collation and the comment all come
// back here.
func (Grammar) AlterColumnType(c base.AlterColumnType) string {
	return "MODIFY " + c.Column + " " + c.Type + c.Collation + c.Comment
}

func (Grammar) AlterColumnNull(c base.AlterColumnNullity) string {
	return "MODIFY " + c.Column + " " + c.Type + " NULL"
}

func (Grammar) AlterColumnNotNull(c base.AlterColumnNullity) string {
	return "MODIFY " + c.Column + " " + c.Type + " NOT NULL"
}

// AlterColumnDropDefaultNull sets the default to NULL: MySQL has no DROP
// DEFAULT for a nullable column.
func (Grammar) AlterColumnDropDefaultNull(c base.AlterColumnDefault) string {
	return "ALTER COLUMN " + c.Column + " SET DEFAULT NULL"
}

func (Grammar) TableComment(c base.TableComment) string {
	return "ALTER TABLE " + c.Table + " COMMENT = " + c.Comment
}

// ColumnComment renders nothing: a comment is written with the column
// definition (MODIFY), so there is no statement of its own.
func (Grammar) ColumnComment(base.ColumnComment) string { return "" }

// DropUnique drops an index: a unique constraint is one here.
func (Grammar) DropUnique(c base.DropConstraint) string {
	return "ALTER TABLE " + c.Table + " DROP INDEX " + c.Name
}

func (Grammar) DropForeignKey(c base.DropConstraint) string {
	return "ALTER TABLE " + c.Table + " DROP FOREIGN KEY " + c.Name
}

// DropPrimaryKey does not name the key: a table has exactly one.
func (Grammar) DropPrimaryKey(c base.DropConstraint) string {
	return "ALTER TABLE " + c.Table + " DROP PRIMARY KEY"
}

// DropCheck tolerates a constraint that is already gone on MariaDB, where
// a column check is named after its column and is removed by the MODIFY
// that rewrites it.
func (g Grammar) DropCheck(c base.DropConstraint) string {
	if g.MariaDB {
		return "ALTER TABLE " + c.Table + " DROP CONSTRAINT IF EXISTS " + c.Name
	}
	return "ALTER TABLE " + c.Table + " DROP CHECK " + c.Name
}

// DropIndex names the table, and so does RenameIndex below: an index name
// is scoped to its table here.
func (Grammar) DropIndex(c base.DropIndex) string {
	return "DROP INDEX " + c.Name + " ON " + c.Table
}

func (Grammar) RenameIndex(c base.RenameIndex) string {
	return "ALTER TABLE " + c.Table + " RENAME INDEX " + c.OldName + " TO " + c.NewName
}
