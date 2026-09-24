package base

// The arguments of the statements a Grammar spells. Every identifier in
// them is already quoted by the caller; a field a backend does not use in
// its own spelling is simply ignored, and one the caller has nothing for is
// empty rather than missing.
type (
	// CreateTable is CREATE TABLE. Definition is the whole body: the
	// column definitions and the table-level constraints, joined.
	CreateTable struct{ Table, Definition string }
	// RenameTable renames a table.
	RenameTable struct{ OldTable, NewTable string }
	// DropTable is DROP TABLE.
	DropTable struct{ Table string }
	// TableComment sets a table's comment. Comment is already a quoted
	// SQL literal.
	TableComment struct{ Table, Comment string }

	// AddColumn adds a column to an existing table.
	AddColumn struct{ Table, Column, Definition string }
	// DropColumn removes one.
	DropColumn struct{ Table, Column string }
	// RenameColumn renames one.
	RenameColumn struct{ Table, OldColumn, NewColumn string }
	// ColumnComment sets a column's comment. Type is the column's new
	// type, which the backends that restate the column need.
	ColumnComment struct{ Table, Column, Type, Comment string }
	// FillColumnDefault gives the rows that have no value for a column
	// the default, on the way to making it NOT NULL.
	FillColumnDefault struct{ Table, Column, Default string }

	// AlterTable wraps one or more change clauses in an ALTER TABLE.
	// Changes is what the clause renderers below produced.
	AlterTable struct{ Table, Changes string }

	// The change clauses. They are fragments of an AlterTable, not
	// statements of their own.

	// AlterColumnType changes a column's type. Collation and Comment are
	// filled in by the backends that spell them as part of the column
	// definition; one that spells them separately leaves them empty.
	AlterColumnType struct{ Column, Type, Collation, Comment, Using string }
	// AlterColumnNullity makes a column nullable or not. Type is needed
	// by the backends that restate the whole column to change it.
	AlterColumnNullity struct{ Column, Type string }
	// AlterColumnDefault sets or drops a column's default. Default is
	// already a quoted SQL literal.
	AlterColumnDefault struct{ Column, Type, Default string }

	// The inline constraints, which go in a CREATE TABLE body.

	// NamedConstraint wraps a constraint body in CONSTRAINT <name>.
	NamedConstraint struct{ Name, Constraint string }
	// PrimaryKeyConstraint is PRIMARY KEY (...).
	PrimaryKeyConstraint struct{ Columns string }
	// UniqueConstraint is UNIQUE (...). Deferrable is the trailing
	// DEFERRABLE clause, empty where there is none.
	UniqueConstraint struct{ Columns, Deferrable string }
	// CheckConstraint is CHECK (...).
	CheckConstraint struct{ Check string }

	// The table-level statements. They are built before they run and
	// rendered at the moment they do, so that a rename in between is
	// reflected in the SQL that finally executes.

	// AddCheck adds a named CHECK constraint.
	AddCheck struct{ Table, Name, Check string }
	// AddUnique adds a named UNIQUE constraint. NullsDistinct and
	// Deferrable are the clauses the backends that have them spell.
	AddUnique struct{ Table, Name, Columns, NullsDistinct, Deferrable string }
	// AddPrimaryKey adds a named PRIMARY KEY constraint.
	AddPrimaryKey struct{ Table, Name, Columns string }
	// AddForeignKey adds a named FOREIGN KEY. Inline spells it as part of
	// a CREATE TABLE body rather than as a statement of its own.
	AddForeignKey struct {
		Table, Name, Column, ToTable, ToColumn, OnDelete, OnUpdate string
		Inline                                                     bool
	}

	// DropConstraint drops a constraint by name. The five kinds are
	// separate because the backends spell them differently: MySQL drops a
	// foreign key with DROP FOREIGN KEY and a unique one with DROP INDEX.
	DropConstraint struct{ Table, Name string }

	// CreateIndex is CREATE INDEX. Every optional part is a clause the
	// caller has already rendered, empty where there is none.
	CreateIndex struct {
		Name, Table, Columns                       string
		Include, Using, Comment, Option, Condition string
		NullsDistinct                              string
		Unique                                     bool
		Class                                      string
		Concurrently                               bool
		// Trailing goes after everything else, which is where SQL Server
		// puts its WITH clause.
		Trailing string
	}
	// DropIndex is DROP INDEX.
	DropIndex struct {
		Table, Name  string
		Concurrently bool
	}
	// RenameIndex renames an index.
	RenameIndex struct{ Table, OldName, NewName string }
)

// Grammar spells the statement shapes of one backend.
//
// It is a value, built once and never changed, so two editors on the same
// connection cannot drift. A backend implements it by embedding
// BaseGrammar and defining only the shapes it spells differently; what it
// leaves out is the shape below, which is the one Django's base schema
// editor uses.
//
// A method returning the empty string means "this backend has no such
// statement", and the caller skips it. That is how SQLite says it has no
// comments and MySQL says a column comment is part of the column
// definition rather than a statement of its own.
type Grammar interface {
	CreateTable(CreateTable) string
	RenameTable(RenameTable) string
	DropTable(DropTable) string
	TableComment(TableComment) string

	AddColumn(AddColumn) string
	DropColumn(DropColumn) string
	RenameColumn(RenameColumn) string
	ColumnComment(ColumnComment) string
	FillColumnDefault(FillColumnDefault) string

	AlterTable(AlterTable) string
	AlterColumnType(AlterColumnType) string
	AlterColumnNull(AlterColumnNullity) string
	AlterColumnNotNull(AlterColumnNullity) string
	AlterColumnDefault(AlterColumnDefault) string
	AlterColumnDropDefault(AlterColumnDefault) string
	AlterColumnDropDefaultNull(AlterColumnDefault) string

	NamedConstraint(NamedConstraint) string
	PrimaryKeyConstraint(PrimaryKeyConstraint) string
	UniqueConstraint(UniqueConstraint) string
	CheckConstraint(CheckConstraint) string

	AddCheck(AddCheck) string
	AddUnique(AddUnique) string
	AddPrimaryKey(AddPrimaryKey) string
	AddForeignKey(AddForeignKey) string

	DropCheck(DropConstraint) string
	DropUnique(DropConstraint) string
	DropPrimaryKey(DropConstraint) string
	DropForeignKey(DropConstraint) string
	DropConstraint(DropConstraint) string

	CreateIndex(CreateIndex) string
	CreateUniqueIndex(CreateIndex) string
	DropIndex(DropIndex) string
	RenameIndex(RenameIndex) string
}

// BaseGrammar is the shape Django's base schema editor uses. A backend
// embeds it and overrides what it spells differently.
type BaseGrammar struct{}

func (BaseGrammar) CreateTable(c CreateTable) string {
	return "CREATE TABLE " + c.Table + " (" + c.Definition + ")"
}

func (BaseGrammar) RenameTable(c RenameTable) string {
	return "ALTER TABLE " + c.OldTable + " RENAME TO " + c.NewTable
}

func (BaseGrammar) DropTable(c DropTable) string { return "DROP TABLE " + c.Table }

func (BaseGrammar) TableComment(c TableComment) string {
	return "COMMENT ON TABLE " + c.Table + " IS " + c.Comment
}

func (BaseGrammar) AddColumn(c AddColumn) string {
	return "ALTER TABLE " + c.Table + " ADD COLUMN " + c.Column + " " + c.Definition
}

func (BaseGrammar) DropColumn(c DropColumn) string {
	return "ALTER TABLE " + c.Table + " DROP COLUMN " + c.Column
}

func (BaseGrammar) RenameColumn(c RenameColumn) string {
	return "ALTER TABLE " + c.Table + " RENAME COLUMN " + c.OldColumn + " TO " + c.NewColumn
}

func (BaseGrammar) ColumnComment(c ColumnComment) string {
	return "COMMENT ON COLUMN " + c.Table + "." + c.Column + " IS " + c.Comment
}

func (BaseGrammar) FillColumnDefault(c FillColumnDefault) string {
	return "UPDATE " + c.Table + " SET " + c.Column + " = " + c.Default + " WHERE " + c.Column + " IS NULL"
}

func (BaseGrammar) AlterTable(c AlterTable) string {
	return "ALTER TABLE " + c.Table + " " + c.Changes
}

func (BaseGrammar) AlterColumnType(c AlterColumnType) string {
	return "ALTER COLUMN " + c.Column + " TYPE " + c.Type + c.Collation
}

func (BaseGrammar) AlterColumnNull(c AlterColumnNullity) string {
	return "ALTER COLUMN " + c.Column + " DROP NOT NULL"
}

func (BaseGrammar) AlterColumnNotNull(c AlterColumnNullity) string {
	return "ALTER COLUMN " + c.Column + " SET NOT NULL"
}

func (BaseGrammar) AlterColumnDefault(c AlterColumnDefault) string {
	return "ALTER COLUMN " + c.Column + " SET DEFAULT " + c.Default
}

func (BaseGrammar) AlterColumnDropDefault(c AlterColumnDefault) string {
	return "ALTER COLUMN " + c.Column + " DROP DEFAULT"
}

func (BaseGrammar) AlterColumnDropDefaultNull(c AlterColumnDefault) string {
	return "ALTER COLUMN " + c.Column + " DROP DEFAULT"
}

func (BaseGrammar) NamedConstraint(c NamedConstraint) string {
	return "CONSTRAINT " + c.Name + " " + c.Constraint
}

func (BaseGrammar) PrimaryKeyConstraint(c PrimaryKeyConstraint) string {
	return "PRIMARY KEY (" + c.Columns + ")"
}

func (BaseGrammar) UniqueConstraint(c UniqueConstraint) string {
	return "UNIQUE (" + c.Columns + ")" + c.Deferrable
}

func (BaseGrammar) CheckConstraint(c CheckConstraint) string {
	return "CHECK (" + c.Check + ")"
}

func (BaseGrammar) AddCheck(c AddCheck) string {
	return "ALTER TABLE " + c.Table + " ADD CONSTRAINT " + c.Name + " CHECK (" + c.Check + ")"
}

func (BaseGrammar) AddUnique(c AddUnique) string {
	return "ALTER TABLE " + c.Table + " ADD CONSTRAINT " + c.Name + " UNIQUE" +
		c.NullsDistinct + " (" + c.Columns + ")" + c.Deferrable
}

func (BaseGrammar) AddPrimaryKey(c AddPrimaryKey) string {
	return "ALTER TABLE " + c.Table + " ADD CONSTRAINT " + c.Name + " PRIMARY KEY (" + c.Columns + ")"
}

func (BaseGrammar) AddForeignKey(c AddForeignKey) string {
	body := "CONSTRAINT " + c.Name + " FOREIGN KEY (" + c.Column + ") REFERENCES " +
		c.ToTable + " (" + c.ToColumn + ")" + c.OnDelete + c.OnUpdate
	if c.Inline {
		return body
	}
	return "ALTER TABLE " + c.Table + " ADD " + body
}

// The four drops share one shape here; the backends that spell a kind
// differently override just that one.
func dropConstraint(c DropConstraint) string {
	return "ALTER TABLE " + c.Table + " DROP CONSTRAINT " + c.Name
}

func (BaseGrammar) DropCheck(c DropConstraint) string      { return dropConstraint(c) }
func (BaseGrammar) DropUnique(c DropConstraint) string     { return dropConstraint(c) }
func (BaseGrammar) DropPrimaryKey(c DropConstraint) string { return dropConstraint(c) }
func (BaseGrammar) DropForeignKey(c DropConstraint) string { return dropConstraint(c) }
func (BaseGrammar) DropConstraint(c DropConstraint) string { return dropConstraint(c) }

// IndexClass is the word before INDEX: UNIQUE, or whatever class gorm
// asked for. It is exported for the backends that spell CREATE INDEX
// differently but classify it the same way.
func IndexClass(c CreateIndex) string { return indexClass(c) }

func indexClass(c CreateIndex) string {
	switch {
	case c.Unique:
		return "UNIQUE "
	case c.Class != "":
		return c.Class + " "
	}
	return ""
}

func (BaseGrammar) CreateIndex(c CreateIndex) string {
	return "CREATE " + indexClass(c) + "INDEX " + c.Name + " ON " + c.Table +
		" (" + c.Columns + ")" + c.Include + c.Using + c.Comment + c.Option + c.Condition + c.Trailing
}

func (BaseGrammar) CreateUniqueIndex(c CreateIndex) string {
	return "CREATE UNIQUE INDEX " + c.Name + " ON " + c.Table + " (" + c.Columns + ")" +
		c.Include + c.NullsDistinct + c.Condition
}

func (BaseGrammar) DropIndex(c DropIndex) string { return "DROP INDEX " + c.Name }

func (BaseGrammar) RenameIndex(c RenameIndex) string {
	return "ALTER INDEX " + c.OldName + " RENAME TO " + c.NewName
}

var _ Grammar = BaseGrammar{}
