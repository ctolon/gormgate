package base

import (
	"strings"
)

// django: db/backends/ddl_references.py

// Reference is a part of a deferred DDL statement that may refer to tables
// or columns and must follow renames.
type Reference interface {
	String() string
	ReferencesTable(table string) bool
	ReferencesColumn(table, column string) bool
	ReferencesIndex(table, index string) bool
	RenameTableReferences(oldTable, newTable string)
	RenameColumnReferences(table, oldColumn, newColumn string)
}

// Literal is a statement part that refers to nothing.
type Literal string

// String returns the literal text.
func (l Literal) String() string { return string(l) }

// ReferencesTable reports false: a literal refers to no table.
func (Literal) ReferencesTable(string) bool { return false }

// ReferencesColumn reports false: a literal refers to no column.
func (Literal) ReferencesColumn(string, string) bool { return false }

// ReferencesIndex reports false: a literal refers to no index.
func (Literal) ReferencesIndex(string, string) bool { return false }

// RenameTableReferences does nothing: a literal refers to no table.
func (Literal) RenameTableReferences(string, string) {}

// RenameColumnReferences does nothing: a literal refers to no column.
func (Literal) RenameColumnReferences(string, string, string) {}

// Table holds a reference to a table.
type Table struct {
	Name  string
	Quote func(string) string
}

// String returns the quoted table name.
func (t *Table) String() string { return t.Quote(t.Name) }

// ReferencesTable reports whether this is a reference to table.
func (t *Table) ReferencesTable(table string) bool { return t.Name == table }

// ReferencesColumn reports false: a table refers to no column.
func (t *Table) ReferencesColumn(string, string) bool { return false }

// ReferencesIndex reports whether the quoted table name is index, which is
// how a backend that scopes an index name to the table it is on spells one.
func (t *Table) ReferencesIndex(table, index string) bool {
	return t.ReferencesTable(table) && t.String() == index
}

// RenameTableReferences follows a rename of oldTable.
func (t *Table) RenameTableReferences(oldTable, newTable string) {
	if t.Name == oldTable {
		t.Name = newTable
	}
}

// RenameColumnReferences does nothing: a table refers to no column.
func (t *Table) RenameColumnReferences(string, string, string) {}

// Columns holds a reference to one or many columns of a table, each with an
// optional suffix (sort order, opclass, ...).
type Columns struct {
	Table    string
	Names    []string
	Quote    func(string) string
	Suffixes []string
}

// String renders the quoted columns with their suffixes, comma separated.
func (c *Columns) String() string {
	parts := make([]string, len(c.Names))
	for i, n := range c.Names {
		col := c.Quote(n)
		if i < len(c.Suffixes) && c.Suffixes[i] != "" {
			col += " " + c.Suffixes[i]
		}
		parts[i] = col
	}
	return strings.Join(parts, ", ")
}

// ReferencesTable reports whether the columns belong to table.
func (c *Columns) ReferencesTable(table string) bool { return c.Table == table }

// ReferencesColumn reports whether column is one of the columns of table.
func (c *Columns) ReferencesColumn(table, column string) bool {
	if c.Table != table {
		return false
	}
	for _, n := range c.Names {
		if n == column {
			return true
		}
	}
	return false
}

// ReferencesIndex reports false: columns refer to no index.
func (c *Columns) ReferencesIndex(string, string) bool { return false }

// RenameTableReferences follows a rename of oldTable.
func (c *Columns) RenameTableReferences(oldTable, newTable string) {
	if c.Table == oldTable {
		c.Table = newTable
	}
}

// RenameColumnReferences follows a rename of oldColumn of table.
func (c *Columns) RenameColumnReferences(table, oldColumn, newColumn string) {
	if c.Table != table {
		return
	}
	for i, n := range c.Names {
		if n == oldColumn {
			c.Names[i] = newColumn
		}
	}
}

// Name is a quoted identifier (index or constraint name) bound to the table
// and columns it covers, like Django's IndexName/ForeignKeyName.
type Name struct {
	Value     string
	Quote     func(string) string
	Table     string
	Columns   []string
	ToTable   string
	ToColumns []string
	// Suffix and MaxLength make the name lazy, like Django's IndexName: it
	// is derived from Table and Columns when the statement is rendered, so
	// that a table rename between the creation of the statement and its
	// execution changes the generated name too.
	Suffix    string
	MaxLength int
}

// String renders the quoted name, deriving it from the table and columns
// when the name is lazy (Suffix set).
func (n *Name) String() string {
	if n.Suffix != "" {
		return n.Quote(CreateIndexName(n.MaxLength, n.Table, n.Columns, n.Suffix))
	}
	return n.Quote(n.Value)
}

// ReferencesTable reports whether the name covers table, on either side of
// a foreign key.
func (n *Name) ReferencesTable(table string) bool {
	return n.Table == table || (n.ToTable != "" && n.ToTable == table)
}

// ReferencesColumn reports whether the name covers column of table, on
// either side of a foreign key.
func (n *Name) ReferencesColumn(table, column string) bool {
	has := func(t string, cols []string) bool {
		if t != table {
			return false
		}
		for _, c := range cols {
			if c == column {
				return true
			}
		}
		return false
	}
	return has(n.Table, n.Columns) || has(n.ToTable, n.ToColumns)
}

// ReferencesIndex reports false: a name is not matched against itself.
func (n *Name) ReferencesIndex(string, string) bool { return false }

// RenameTableReferences follows a rename of oldTable on either side of a
// foreign key.
func (n *Name) RenameTableReferences(oldTable, newTable string) {
	if n.Table == oldTable {
		n.Table = newTable
	}
	if n.ToTable == oldTable {
		n.ToTable = newTable
	}
}

// RenameColumnReferences follows a rename of oldColumn of table on either
// side of a foreign key.
func (n *Name) RenameColumnReferences(table, oldColumn, newColumn string) {
	if n.Table == table {
		n.Columns = renameIn(n.Columns, oldColumn, newColumn)
	}
	if n.ToTable == table {
		n.ToColumns = renameIn(n.ToColumns, oldColumn, newColumn)
	}
}

func renameIn(xs []string, old, new string) []string {
	out := make([]string, len(xs))
	for i, x := range xs {
		if x == old {
			x = new
		}
		out[i] = x
	}
	return out
}

// Expressions holds SQL expressions of an index over a table; column
// references inside can't be tracked, so renames are not applied.
type Expressions struct {
	Table string
	SQL   string
}

// String returns the expression SQL.
func (e *Expressions) String() string { return e.SQL }

// ReferencesTable reports whether the expressions index table.
func (e *Expressions) ReferencesTable(table string) bool { return e.Table == table }

// ReferencesColumn reports false: column references inside an expression
// can't be tracked.
func (e *Expressions) ReferencesColumn(string, string) bool { return false }

// ReferencesIndex reports false: expressions refer to no index.
func (e *Expressions) ReferencesIndex(string, string) bool { return false }

// RenameTableReferences follows a rename of oldTable.
func (e *Expressions) RenameTableReferences(oldTable, newTable string) {
	if e.Table == oldTable {
		e.Table = newTable
	}
}

// RenameColumnReferences does nothing: column references inside an
// expression can't be tracked.
func (e *Expressions) RenameColumnReferences(string, string, string) {}

// Statement is a DDL statement that is built before it runs and spelled
// when it does, so that a table or column rename that happens in between
// is reflected in the SQL that finally executes.
type Statement struct {
	// render spells the statement. It is called when the statement
	// executes, not when it is built, so a rename that happens in between
	// is in the SQL that finally runs.
	render func(Grammar) string
	// g is the grammar render is called with.
	g Grammar
	// refs are the parts that follow a rename. Rewriting walks this list.
	refs []Reference
	// kind tells two statements of different shape apart on the deferred
	// list, where comparing their SQL would mean rendering them early.
	kind StatementKind
	// name is the constraint or index the statement is about, when it has
	// one. The deferred list is searched by it.
	name *Name
	// index and dropIndex are the arguments of the two statements a
	// backend adjusts after the base editor has built them: PostgreSQL
	// adds CONCURRENTLY, SQL Server moves its option clause. They are nil
	// for every other kind.
	index      *CreateIndex
	dropIndex  *DropIndex
	foreignKey *AddForeignKey
}

// Columns returns the column list the statement was built with, for a
// backend that has to decorate it: PostgreSQL appends operator classes.
func (s *Statement) Columns() *Columns {
	for _, r := range s.refs {
		if c, ok := r.(*Columns); ok {
			return c
		}
	}
	return nil
}

// Tables returns the table references the statement was built with, for a
// backend that has to redirect one.
func (s *Statement) Tables() []*Table {
	var out []*Table
	for _, r := range s.refs {
		if t, ok := r.(*Table); ok {
			out = append(out, t)
		}
	}
	return out
}

// ForeignKey returns the arguments of an ADD FOREIGN KEY statement, for a
// backend that has to adjust them: Oracle has no ON UPDATE. It is nil for
// any other statement.
func (s *Statement) ForeignKey() *AddForeignKey { return s.foreignKey }

// NewStatement builds a statement from the parts that follow renames. The
// caller attaches the rendering with Editor.Spell.
func NewStatement(refs ...Reference) *Statement {
	out := make([]Reference, 0, len(refs))
	for _, r := range refs {
		if r != nil {
			out = append(out, r)
		}
	}
	return &Statement{refs: out}
}

// SetRender attaches a rendering to a statement built outside an Editor,
// which is what a backend's test double does.
func SetRender(st *Statement, g Grammar, render func(Grammar) string) *Statement {
	st.render, st.g = render, g
	return st
}

// refOf is the Reference in v, or nil when v is plain text. It is how a
// clause that is sometimes a reference and sometimes a literal joins the
// list of parts a rename has to follow.
func refOf(v any) Reference {
	r, _ := v.(Reference)
	return r
}

// Index returns the arguments of a CREATE INDEX statement, for a backend
// that has to adjust them. It is nil for any other statement.
func (s *Statement) Index() *CreateIndex { return s.index }

// DropIndexArgs returns the arguments of a DROP INDEX statement. It is nil
// for any other statement.
func (s *Statement) DropIndexArgs() *DropIndex { return s.dropIndex }

// Kind reports what the statement does.
func (s *Statement) Kind() StatementKind { return s.kind }

// Name reports the constraint or index the statement is about, or nil.
func (s *Statement) Name() *Name { return s.name }

// String renders the statement with the grammar it was built for.
func (s *Statement) String() string { return s.render(s.g) }

func (s *Statement) any(fn func(Reference) bool) bool {
	for _, p := range s.refs {
		if fn(p) {
			return true
		}
	}
	return false
}

// StatementKind identifies what a deferred statement does, for the
// predicates that search the deferred list.
type StatementKind int

// The kinds. StatementOther is the zero value, for a statement nothing
// searches for.
const (
	StatementOther StatementKind = iota
	StatementAddForeignKey
	StatementAddInlineForeignKey
	StatementCreateIndex
	StatementAddUnique
	StatementDropForeignKey
)

// partString renders a part that NewStatement would have boxed: a plain
// string, or a Reference read at the moment it is asked for.
func partString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case Reference:
		return x.String()
	}
	return ""
}

// ReferencesTable reports whether any part of the statement refers to
// table.
func (s *Statement) ReferencesTable(table string) bool {
	return s.any(func(r Reference) bool { return r.ReferencesTable(table) })
}

// ReferencesColumn reports whether any part of the statement refers to
// column of table.
func (s *Statement) ReferencesColumn(table, column string) bool {
	return s.any(func(r Reference) bool { return r.ReferencesColumn(table, column) })
}

// ReferencesIndex reports whether any part of the statement refers to
// index of table.
func (s *Statement) ReferencesIndex(table, index string) bool {
	return s.any(func(r Reference) bool { return r.ReferencesIndex(table, index) })
}

// RenameTableReferences makes every part follow a rename of oldTable.
func (s *Statement) RenameTableReferences(oldTable, newTable string) {
	for _, p := range s.refs {
		p.RenameTableReferences(oldTable, newTable)
	}
}

// RenameColumnReferences makes every part follow a rename of oldColumn of
// table.
func (s *Statement) RenameColumnReferences(table, oldColumn, newColumn string) {
	for _, p := range s.refs {
		p.RenameColumnReferences(table, oldColumn, newColumn)
	}
}
