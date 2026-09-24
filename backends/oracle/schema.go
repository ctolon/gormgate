package oracle

import (
	"database/sql"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ctolon/gormgate/backends/base"
	m "github.com/ctolon/gormgate/migrations"
)

// django: db/backends/oracle/schema.py DatabaseSchemaEditor

// Editor is the Oracle schema editor.
type Editor struct {
	*base.Editor
}

// A method whose name does not match a step of base.Overrides is a
// method nothing calls; this makes that a build failure.
var _ base.Overrides = (*Editor)(nil)

// NewEditor builds an Oracle schema editor.
func NewEditor(c *base.Conn, collect, atomic bool) *Editor {
	e := &Editor{}
	e.Editor = base.NewEditor(e, c, base.EditorOptions{
		CollectSQL: collect, Atomic: atomic, Grammar: Grammar{},
	})
	return e
}

// splitType separates a column type into its base type and the identity
// clause gorm-oracle appends for auto-increment columns.
func splitType(t string) (baseType, identity string) {
	if i := strings.Index(strings.ToUpper(t), " GENERATED "); i >= 0 {
		return strings.TrimSpace(t[:i]), strings.TrimSpace(t[i:])
	}
	return strings.TrimSpace(t), ""
}

var (
	clobRe    = regexp.MustCompile(`(?i)^N?CLOB`)
	varcharRe = regexp.MustCompile(`(?i)^N?VARCHAR2`)
	lobRe     = regexp.MustCompile(`(?i)^(N?CLOB|BLOB|BFILE|LONG)`)
)

// onDeleteSQL maps a foreign key's OnDelete action to Oracle's REFERENCES
// clause. Oracle knows CASCADE and SET NULL only; NO ACTION and RESTRICT
// are its default behaviour and are therefore not spelled out.
func onDeleteSQL(action m.ReferentialAction) (string, bool) {
	switch m.ReferentialAction(strings.ToUpper(strings.TrimSpace(string(action)))) {
	case "":
		return "", true
	case m.Cascade:
		return " ON DELETE CASCADE", true
	case m.SetNull:
		return " ON DELETE SET NULL", true
	case m.NoAction, m.Restrict:
		return "", true
	}
	return "", false
}

// checkForeignKey rejects foreign key actions Oracle cannot express.
func (e *Editor) checkForeignKey(f *m.ModelField) error {
	fk := f.Field.ForeignKey
	if fk == nil {
		return nil
	}
	if fk.OnUpdate != "" {
		return m.NotSupportedf(
			"Oracle foreign keys have no ON UPDATE action, but %s.%s (constraint %s) is declared with OnUpdate %q. "+
				"gorm-oracle emulates ON UPDATE with an AFTER UPDATE trigger on the referenced table; gormgate does not, "+
				"because such a trigger is not part of the migration state. Drop OnUpdate from the field, or create the "+
				"trigger explicitly with a RunSQL operation.",
			f.Model.Table, f.Column, fk.Name, fk.OnUpdate)
	}
	if _, ok := onDeleteSQL(fk.OnDelete); !ok {
		return m.NotSupportedf(
			"Oracle foreign keys only support ON DELETE CASCADE and ON DELETE SET NULL, but %s.%s (constraint %s) is "+
				"declared with OnDelete %q.",
			f.Model.Table, f.Column, fk.Name, fk.OnDelete)
	}
	return nil
}

// checkForeignKeyConstraint rejects the actions of a table-level foreign
// key Oracle cannot express, with the same reasons as checkForeignKey.
func (e *Editor) checkForeignKeyConstraint(model *m.Model, c *m.ForeignKeyConstraint) error {
	if c.OnUpdate != "" {
		return m.NotSupportedf(
			"Oracle foreign keys have no ON UPDATE action, but %s (constraint %s) is declared with OnUpdate %q. "+
				"gorm-oracle emulates ON UPDATE with an AFTER UPDATE trigger on the referenced table; gormgate does not, "+
				"because such a trigger is not part of the migration state. Drop OnUpdate from the constraint, or create "+
				"the trigger explicitly with a RunSQL operation.",
			model.Table, c.Name, c.OnUpdate)
	}
	if _, ok := onDeleteSQL(c.OnDelete); !ok {
		return m.NotSupportedf(
			"Oracle foreign keys only support ON DELETE CASCADE and ON DELETE SET NULL, but %s (constraint %s) is "+
				"declared with OnDelete %q.",
			model.Table, c.Name, c.OnDelete)
	}
	return nil
}

// CompositeFKSQL renders a table-level foreign key without the ON UPDATE
// clause and with only the ON DELETE actions Oracle accepts.
func (e *Editor) CompositeFKSQL(model *m.Model, c *m.ForeignKeyConstraint) (*base.Statement, error) {
	if err := e.checkForeignKeyConstraint(model, c); err != nil {
		return nil, err
	}
	st, err := e.Editor.CompositeFKSQL(model, c)
	if err != nil {
		return nil, err
	}
	od, _ := onDeleteSQL(c.OnDelete)
	if a := st.ForeignKey(); a != nil {
		a.OnDelete, a.OnUpdate = od, ""
	}
	return st, nil
}

func (e *Editor) checkForeignKeys(model *m.Model) error {
	for _, f := range model.Fields {
		if err := e.checkForeignKey(f); err != nil {
			return err
		}
	}
	for _, c := range model.Options().Constraints {
		fk, ok := c.(*m.ForeignKeyConstraint)
		if !ok {
			continue
		}
		if err := e.checkForeignKeyConstraint(model, fk); err != nil {
			return err
		}
	}
	return nil
}

// CreateFKSQL renders the foreign key constraint of f without the ON UPDATE
// clause and with only the ON DELETE actions Oracle accepts.
//
// django: base/schema.py BaseDatabaseSchemaEditor._create_fk_sql
func (e *Editor) CreateFKSQL(model *m.Model, f *m.ModelField) *base.Statement {
	st := e.Editor.CreateFKSQL(model, f)
	od, _ := onDeleteSQL(f.Field.ForeignKey.OnDelete)
	if a := st.ForeignKey(); a != nil {
		a.OnDelete, a.OnUpdate = od, ""
	}
	return st
}

// TableSQL returns the CREATE TABLE statement.
func (e *Editor) TableSQL(model *m.Model) (string, error) {
	if err := e.checkForeignKeys(model); err != nil {
		return "", err
	}
	return e.Editor.TableSQL(model)
}

// AddField adds a column.
func (e *Editor) AddField(model *m.Model, f *m.ModelField) error {
	if err := e.checkForeignKey(f); err != nil {
		return err
	}
	return e.Editor.AddField(model, f)
}

// RemoveField drops a column, dropping its identity property first.
//
// django: oracle/schema.py DatabaseSchemaEditor.remove_field
func (e *Editor) RemoveField(model *m.Model, f *m.ModelField) error {
	identity, err := e.isIdentityColumn(model.Table, f.Column)
	if err != nil {
		return err
	}
	if identity {
		if err := e.dropIdentity(model.Table, f.Column); err != nil {
			return err
		}
	}
	return e.Editor.RemoveField(model, f)
}

// CreatePrimaryKeySQL renders ADD PRIMARY KEY.
//
// Django names the constraint; gormgate leaves it unnamed so that a primary
// key added by an alteration carries the same (system generated) name as
// one created by CREATE TABLE, which neither gorm nor Django's table_sql
// names either.
func (e *Editor) CreatePrimaryKeySQL(model *m.Model, columns []string) *base.Statement {
	table := &base.Table{Name: model.Table, Quote: e.QuoteName}
	cols := &base.Columns{Table: model.Table, Names: append([]string(nil), columns...), Quote: e.QuoteName}
	return e.Spell(base.NewStatement(table, cols), func(base.Grammar) string {
		return "ALTER TABLE " + table.String() + " ADD PRIMARY KEY (" + cols.String() + ")"
	})
}

// DBDefaultSQL renders a database default the way gorm-oracle's
// FullDataTypeOf does, so that a column created by gormgate is spelled
// exactly like the one AutoMigrate would create.
//
// gorm-oracle: migrator.go Migrator.buildOracleDefault /
// buildOracleDefaultFromInterface
func (e *Editor) DBDefaultSQL(f *m.ModelField) (string, error) {
	d := f.Field.DBDefault
	if d.Expr != "" {
		return gormDefaultExpr(d.Expr), nil
	}
	if t, ok := d.Value.(time.Time); ok {
		return fmt.Sprintf("TO_TIMESTAMP('%s', 'YYYY-MM-DD HH24:MI:SS')", t.Format("2006-01-02 15:04:05")), nil
	}
	return e.QuoteValue(d.Value)
}

// gormDefaultExpr reproduces gorm-oracle's translation of a `default:` tag
// that was not parsed into a Go value.
func gormDefaultExpr(value string) string {
	value = strings.TrimSpace(value)
	switch strings.ToUpper(value) {
	case "NULL":
		return "NULL"
	case "CURRENT_TIMESTAMP", "NOW()":
		return "CURRENT_TIMESTAMP"
	case "SYSDATE":
		return "SYSDATE"
	case "TRUE":
		return "1"
	case "FALSE":
		return "0"
	}
	if strings.Contains(strings.ToUpper(value), ".NEXTVAL") {
		return value
	}
	if _, err := strconv.ParseFloat(value, 64); err == nil {
		return value
	}
	if len(value) == 10 && strings.Count(value, "-") == 2 {
		if _, err := time.Parse("2006-01-02", value); err == nil {
			return "TO_DATE('" + value + "', 'YYYY-MM-DD')"
		}
	}
	if strings.HasPrefix(value, "'") && strings.HasSuffix(value, "'") {
		return value
	}
	return base.QuoteString(value)
}

// AlterColumnNullSQL is _alter_column_null_sql, but silent when the column
// already has the wanted nullability: Oracle rejects a MODIFY that does not
// change it (ORA-01451 "column to be modified to NULL cannot be modified to
// NULL", ORA-01442 "column to be modified to NOT NULL is already NOT NULL").
func (e *Editor) AlterColumnNullSQL(model *m.Model, old, new *m.ModelField) (string, error) {
	s, err := e.Editor.AlterColumnNullSQL(model, old, new)
	if err != nil || s == "" {
		return s, err
	}
	nullable, known, err := e.columnNullable(model.Table, new.Column)
	if err != nil {
		return "", err
	}
	if known && nullable == new.Field.Null {
		return "", nil
	}
	return s, nil
}

// AlterColumnTypeSQL is _alter_column_type_sql: it drops the identity of a
// column that stops being an auto-increment column and never emits the
// identity clause itself, which Oracle refuses in a MODIFY (ORA-30673).
//
// django: oracle/schema.py DatabaseSchemaEditor._alter_column_type_sql
func (e *Editor) AlterColumnTypeSQL(model *m.Model, old, new *m.ModelField, newType string) (base.Fragment, []string, error) {
	oldType, err := e.ColumnType(model, old)
	if err != nil {
		return base.Fragment{}, nil, err
	}
	var other []string
	if e.Conn().Features().SupportsComments && old.Field.Comment != new.Field.Comment {
		s, err := e.Self().AlterColumnCommentSQL(model, new, newType, new.Field.Comment)
		if err != nil {
			return base.Fragment{}, nil, err
		}
		if s != "" {
			other = append(other, s)
		}
	}
	oldBase, oldIdentity := splitType(oldType)
	newBase, newIdentity := splitType(newType)
	if oldIdentity != "" && newIdentity == "" {
		identity, err := e.isIdentityColumn(model.Table, new.Column)
		if err != nil {
			return base.Fragment{}, nil, err
		}
		if identity {
			if err := e.dropIdentity(model.Table, new.Column); err != nil {
				return base.Fragment{}, nil, err
			}
		}
	}
	if strings.EqualFold(oldBase, newBase) {
		return base.Fragment{}, other, nil
	}
	args := base.AlterColumnType{Column: e.QuoteName(new.Column), Type: newBase}
	return base.Fragment{SQL: e.Grammar().AlterColumnType(args)}, other, nil
}

// AlterField alters a column, falling back to the temporary-column
// workaround for the alterations Oracle refuses.
//
// django: oracle/schema.py DatabaseSchemaEditor.alter_field
func (e *Editor) AlterField(model *m.Model, old, new *m.ModelField, strict bool) error {
	if err := e.checkForeignKey(new); err != nil {
		return err
	}
	oldType, err := e.ColumnType(model, old)
	if err != nil {
		return err
	}
	newType, err := e.ColumnType(model, new)
	if err != nil {
		return err
	}
	if needsTypeWorkaround(oldType, newType) {
		return e.alterFieldTypeWorkaround(model, old, new, oldType, newType)
	}
	err = e.Editor.AlterField(model, old, new, strict)
	switch {
	case err == nil:
		return nil
	// Changing to or from a LOB type, and changing the type (ORA-01439) or
	// narrowing the precision (ORA-01440) of a column that holds rows, is
	// only possible through a new column.
	case oraCode(err, 22858, 22859, 1439, 1440):
		return e.alterFieldTypeWorkaround(model, old, new, oldType, newType)
	// An identity column that changes to a non-numeric type has to lose its
	// identity first.
	case oraCode(err, 30675):
		if derr := e.dropIdentity(model.Table, old.Column); derr != nil {
			return derr
		}
		return e.Editor.AlterField(model, old, new, strict)
	}
	return err
}

// needsTypeWorkaround reports the type changes Oracle always refuses in
// place: adding an identity to an existing column (ORA-30673) and changing
// a column from or to a LOB type (ORA-22858).
func needsTypeWorkaround(oldType, newType string) bool {
	oldBase, oldIdentity := splitType(oldType)
	newBase, newIdentity := splitType(newType)
	if newIdentity != "" && oldIdentity == "" {
		return true
	}
	return !strings.EqualFold(oldBase, newBase) && (lobRe.MatchString(oldBase) || lobRe.MatchString(newBase))
}

// alterFieldTypeWorkaround adds a temporary column of the new type, copies
// the values over with an explicit conversion, drops the old column and
// renames the temporary one.
//
// django: oracle/schema.py DatabaseSchemaEditor._alter_field_type_workaround
func (e *Editor) alterFieldTypeWorkaround(model *m.Model, old, new *m.ModelField, oldType, newType string) error {
	if old.Field.PrimaryKey {
		// The column is dropped below, which would take the primary key
		// with it; Django drops it explicitly as well.
		if err := e.Self().DeletePrimaryKey(model, false); err != nil {
			return err
		}
	}
	temp := &m.ModelField{
		Model:  new.Model,
		Name:   new.Name,
		Column: e.generateTempName(new.Column),
		Field:  new.Field.Clone(),
	}
	// The temporary column has to be nullable unless it generates its own
	// values; its unique constraint, primary key, foreign key and comment
	// are created under the final column name by the alteration below, so
	// that they do not carry the temporary name.
	temp.Field.Null = !new.Field.AutoIncrement
	temp.Field.PrimaryKey = false
	temp.Field.Unique = false
	temp.Field.ForeignKey = nil
	temp.Field.Comment = ""
	if err := e.AddField(model, temp); err != nil {
		return err
	}
	if err := e.Execute(fmt.Sprintf("UPDATE %s SET %s = %s",
		e.QuoteName(model.Table), e.QuoteName(temp.Column),
		e.conversionSQL(old, oldType, newType))); err != nil {
		return err
	}
	if err := e.RemoveField(model, old); err != nil {
		return err
	}
	// Renames the temporary column and adds everything left out above.
	return e.Editor.AlterField(model, temp, new, false)
}

// conversionSQL is the expression that reads the old column with an
// explicit data type conversion.
//
// django: oracle/schema.py DatabaseSchemaEditor._alter_field_type_workaround
func (e *Editor) conversionSQL(old *m.ModelField, oldType, newType string) string {
	value := e.QuoteName(old.Column)
	ot, _ := splitType(oldType)
	nt, _ := splitType(newType)
	if clobRe.MatchString(ot) {
		value = "TO_CHAR(" + value + ")"
		ot = "VARCHAR2"
	}
	if varcharRe.MatchString(ot) {
		up := strings.ToUpper(nt)
		switch {
		case strings.HasPrefix(up, "DATE"):
			value = fmt.Sprintf("TO_DATE(%s, 'YYYY-MM-DD')", value)
		case strings.HasPrefix(up, "TIMESTAMP"):
			value = fmt.Sprintf("TO_TIMESTAMP(%s, 'YYYY-MM-DD HH24:MI:SS.FF')", value)
		}
	}
	return value
}

// generateTempName builds the name of a temporary column.
//
// django: oracle/schema.py DatabaseSchemaEditor._generate_temp_name
func (e *Editor) generateTempName(forName string) string {
	return base.TruncateName(forName+"_"+strings.ToUpper(base.NamesDigest(8, forName)), MaxNameLength, 4)
}

// isIdentityColumn reports whether a column generates its own values.
//
// django: oracle/schema.py DatabaseSchemaEditor._is_identity_column
func (e *Editor) isIdentityColumn(table, column string) (bool, error) {
	if column == "" {
		return false, nil
	}
	var yes sql.NullString
	err := e.Conn().DB().Raw(
		`SELECT identity_column FROM user_tab_cols WHERE table_name = ? AND column_name = ?`,
		table, column).Row().Scan(&yes)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return yes.String == "YES", nil
}

// columnNullable reports the nullability a column currently has; known is
// false when the column does not exist (yet).
func (e *Editor) columnNullable(table, column string) (nullable, known bool, err error) {
	var value sql.NullString
	err = e.Conn().DB().Raw(
		`SELECT nullable FROM user_tab_cols WHERE table_name = ? AND column_name = ?`,
		table, column).Row().Scan(&value)
	if err == sql.ErrNoRows {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	return value.String == "Y", true, nil
}

// dropIdentity removes the identity property of a column.
//
// django: oracle/schema.py DatabaseSchemaEditor._drop_identity
func (e *Editor) dropIdentity(table, column string) error {
	return e.Execute(fmt.Sprintf("ALTER TABLE %s MODIFY %s DROP IDENTITY",
		e.QuoteName(table), e.QuoteName(column)))
}

// Grammar is Oracle's spelling of the statements gormgate emits.
type Grammar struct{ base.BaseGrammar }

func (Grammar) DropTable(c base.DropTable) string {
	return "DROP TABLE " + c.Table + " CASCADE CONSTRAINTS"
}

func (Grammar) AddColumn(c base.AddColumn) string {
	return "ALTER TABLE " + c.Table + " ADD " + c.Column + " " + c.Definition
}

func (Grammar) AlterColumnType(c base.AlterColumnType) string {
	return "MODIFY " + c.Column + " " + c.Type + c.Collation
}

func (Grammar) AlterColumnNull(c base.AlterColumnNullity) string {
	return "MODIFY " + c.Column + " NULL"
}

func (Grammar) AlterColumnNotNull(c base.AlterColumnNullity) string {
	return "MODIFY " + c.Column + " NOT NULL"
}

func (Grammar) AlterColumnDefault(c base.AlterColumnDefault) string {
	return "MODIFY " + c.Column + " DEFAULT " + c.Default
}

// AlterColumnDropDefault sets the default to NULL: Oracle has no DROP
// DEFAULT.
func (Grammar) AlterColumnDropDefault(c base.AlterColumnDefault) string {
	return "MODIFY " + c.Column + " DEFAULT NULL"
}

func (Grammar) AlterColumnDropDefaultNull(c base.AlterColumnDefault) string {
	return "MODIFY " + c.Column + " DEFAULT NULL"
}
