package base

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	m "github.com/ctolon/gormgate/migrations"
)

// TableInfo is one table or view.
//
// django: db/backends/base/introspection.py TableInfo
type TableInfo struct {
	Name string
	// Type is "t" (table), "v" (view), "m" (materialized view), "p"
	// (partition) or "f" (foreign table).
	Type    string
	Comment string
}

// ColumnInfo describes one column (Django's FieldInfo).
type ColumnInfo struct {
	Name      string
	Type      string // database type as the database reports it
	Size      int    // character length, 0 when not applicable
	Precision int
	Scale     int
	Null      bool
	Default   *string
	Collation string
	Comment   string
	// AutoIncrement is set for identity/serial/auto_increment columns.
	AutoIncrement bool
}

// ConstraintInfo describes one constraint or index.
//
// django: base/introspection.py BaseDatabaseIntrospection.get_constraints
type ConstraintInfo struct {
	Columns    []string
	PrimaryKey bool
	Unique     bool
	// ForeignKey is the target of the constraint when it is a foreign key,
	// and nil otherwise.
	ForeignKey *ForeignKeyTarget
	Check      bool
	Index      bool
	// Type is the index access method ("btree", "hash", ...).
	Type string
	// Orders holds the sort direction of every column of an index, in
	// column order. The zero SortOrder means the database reports none.
	Orders     []m.SortOrder
	Definition string
}

// ParseSortOrder turns the sort direction a database reports for an index
// column into the same SortOrder the declaration side uses. The empty
// spelling is the unset order, which is what a database reports for an
// index whose access method has no direction at all.
//
// An unexpected spelling is an error rather than the zero value: folding it
// into "unset" would report a descending index as an ascending one, and the
// introspection is compared against what the models declare.
func ParseSortOrder(s string) (m.SortOrder, error) {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "":
		return "", nil
	case string(m.SortAsc):
		return m.SortAsc, nil
	case string(m.SortDesc):
		return m.SortDesc, nil
	}
	return "", fmt.Errorf("unknown index sort order %q", s)
}

// ParseSortOrders applies ParseSortOrder to every element of ss.
func ParseSortOrders(ss []string) ([]m.SortOrder, error) {
	if ss == nil {
		return nil, nil
	}
	out := make([]m.SortOrder, len(ss))
	for i, s := range ss {
		o, err := ParseSortOrder(s)
		if err != nil {
			return nil, err
		}
		out[i] = o
	}
	return out, nil
}

// ForeignKeyTarget is what a foreign key constraint references: a table and
// its referenced columns, lined up one by one with ConstraintInfo.Columns.
type ForeignKeyTarget struct {
	Table   string
	Columns []string
}

// FK builds a single-column foreign key target.
func FK(table, column string) *ForeignKeyTarget {
	return &ForeignKeyTarget{Table: table, Columns: []string{column}}
}

// SequenceInfo names a sequence feeding a column.
type SequenceInfo struct {
	Name   string
	Table  string
	Column string
}

// RelationInfo is one foreign key of a table: column -> (target column,
// target table).
type RelationInfo struct {
	Column   string
	ToColumn string
	ToTable  string
}

// Introspection reads the database schema.
type Introspection interface {
	TableNames(includeViews bool) ([]TableInfo, error)
	TableDescription(table string) ([]ColumnInfo, error)
	Constraints(table string) (map[string]ConstraintInfo, error)
	Sequences(table string) ([]SequenceInfo, error)
	Relations(table string) (map[string]RelationInfo, error)
	PrimaryKeyColumns(table string) ([]string, error)
	TableComment(table string) (string, error)
	// IdentifierConverter normalizes a name as the database stores it
	// (Oracle folds unquoted names to upper case; others return name).
	IdentifierConverter(name string) string
}

// FirstPrimaryKey returns the columns of the primary key among the
// constraints of a table.
//
// The constraints are a map and Go randomizes the order a map is ranged
// in, so the entries are visited in sorted name order: the answer is then
// the same on every call even where a table carries more than one entry
// marked as a primary key.
func FirstPrimaryKey(cons map[string]ConstraintInfo) []string {
	for _, name := range slices.Sorted(maps.Keys(cons)) {
		if c := cons[name]; c.PrimaryKey {
			return c.Columns
		}
	}
	return nil
}
