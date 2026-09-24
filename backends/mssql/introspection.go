package mssql

import (
	"database/sql"
	"fmt"
	"slices"
	"strings"

	"github.com/ctolon/gormgate/backends/base"
	m "github.com/ctolon/gormgate/migrations"
)

// Introspection reads SQL Server's system catalogs.
//
// django: mssql-django mssql/introspection.py DatabaseIntrospection
type Introspection struct {
	Conn *base.Conn
}

// IdentifierConverter returns name unchanged: SQL Server stores identifiers
// with the case they were created with.
func (i *Introspection) IdentifierConverter(name string) string { return name }

// TableNames lists the tables (and views) of the connection's schema.
//
// django: mssql/introspection.py DatabaseIntrospection.get_table_list
func (i *Introspection) TableNames(includeViews bool) ([]base.TableInfo, error) {
	rows, err := i.Conn.Rows(`
		SELECT
			t.TABLE_NAME,
			t.TABLE_TYPE,
			COALESCE(CAST(ep.value AS nvarchar(max)), '')
		FROM INFORMATION_SCHEMA.TABLES t
		LEFT JOIN sys.objects o
			ON o.name = t.TABLE_NAME AND o.schema_id = SCHEMA_ID(t.TABLE_SCHEMA)
		LEFT JOIN sys.extended_properties ep
			ON ep.class = 1 AND ep.major_id = o.object_id AND ep.minor_id = 0
			AND ep.name = 'MS_Description'
		WHERE t.TABLE_SCHEMA = SCHEMA_NAME()
		ORDER BY t.TABLE_NAME`)
	if err != nil {
		return nil, fmt.Errorf("listing tables: %w", err)
	}
	defer rows.Close()
	var out []base.TableInfo
	for rows.Next() {
		var name, kind, comment string
		if err := rows.Scan(&name, &kind, &comment); err != nil {
			return nil, err
		}
		t := base.TableInfo{Name: name, Type: "t", Comment: comment}
		if kind == "VIEW" {
			t.Type = "v"
		}
		if !includeViews && t.Type == "v" {
			continue
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// TableDescription describes the columns of a table in column_id order.
//
// django: mssql/introspection.py DatabaseIntrospection.get_table_description
func (i *Introspection) TableDescription(table string) ([]base.ColumnInfo, error) {
	rows, err := i.Conn.Rows(`
		SELECT
			c.name,
			tp.name,
			c.max_length,
			c.precision,
			c.scale,
			c.is_nullable,
			dc.definition,
			COALESCE(c.collation_name, ''),
			COALESCE(CAST(ep.value AS nvarchar(max)), ''),
			c.is_identity
		FROM sys.columns c
		JOIN sys.tables t ON t.object_id = c.object_id
		JOIN sys.types tp ON tp.user_type_id = c.user_type_id
		LEFT JOIN sys.default_constraints dc
			ON dc.parent_object_id = c.object_id AND dc.parent_column_id = c.column_id
		LEFT JOIN sys.extended_properties ep
			ON ep.class = 1 AND ep.major_id = c.object_id AND ep.minor_id = c.column_id
			AND ep.name = 'MS_Description'
		WHERE t.object_id = OBJECT_ID(?)
		ORDER BY c.column_id`, table)
	if err != nil {
		return nil, fmt.Errorf("describing the columns of %s: %w", table, err)
	}
	defer rows.Close()
	var out []base.ColumnInfo
	for rows.Next() {
		var (
			name, typeName string
			maxLength      int
			precision      int
			scale          int
			def            sql.NullString
		)
		c := base.ColumnInfo{}
		if err := rows.Scan(&name, &typeName, &maxLength, &precision, &scale,
			&c.Null, &def, &c.Collation, &c.Comment, &c.AutoIncrement); err != nil {
			return nil, err
		}
		c.Name = name
		c.Size = charLength(typeName, maxLength)
		c.Precision, c.Scale = precision, scale
		c.Type = columnType(typeName, maxLength, precision, scale)
		if def.Valid {
			d := def.String
			c.Default = &d
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// charLength converts sys.columns.max_length (bytes) into characters for the
// string types; 0 means "not a string type", -1 means MAX.
func charLength(typeName string, maxLength int) int {
	switch typeName {
	case "nvarchar", "nchar":
		if maxLength < 0 {
			return -1
		}
		return maxLength / 2
	case "varchar", "char", "varbinary", "binary":
		return maxLength
	}
	return 0
}

// columnType renders the column type the way it is written in DDL, so that
// two schemas can be compared on it.
func columnType(typeName string, maxLength, precision, scale int) string {
	switch typeName {
	case "nvarchar", "nchar", "varchar", "char", "varbinary", "binary":
		n := charLength(typeName, maxLength)
		if n < 0 {
			return typeName + "(MAX)"
		}
		return fmt.Sprintf("%s(%d)", typeName, n)
	case "decimal", "numeric":
		return fmt.Sprintf("%s(%d, %d)", typeName, precision, scale)
	case "datetime2", "datetimeoffset", "time":
		return fmt.Sprintf("%s(%d)", typeName, scale)
	}
	return typeName
}

// Sequences returns the identity column of the table; SQL Server allows at
// most one per table and it has no separate sequence object.
//
// django: mssql/introspection.py DatabaseIntrospection.get_sequences
func (i *Introspection) Sequences(table string) ([]base.SequenceInfo, error) {
	rows, err := i.Conn.Rows(`
		SELECT c.name FROM sys.columns c
		WHERE c.object_id = OBJECT_ID(?) AND c.is_identity = 1`, table)
	if err != nil {
		return nil, fmt.Errorf("listing the sequences of %s: %w", table, err)
	}
	defer rows.Close()
	var out []base.SequenceInfo
	for rows.Next() {
		s := base.SequenceInfo{Table: table}
		if err := rows.Scan(&s.Column); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// Relations maps foreign-key columns to their targets.
//
// django: mssql/introspection.py DatabaseIntrospection.get_relations
func (i *Introspection) Relations(table string) (map[string]base.RelationInfo, error) {
	fks, err := i.ForeignKeys(table)
	if err != nil {
		return nil, err
	}
	out := map[string]base.RelationInfo{}
	for _, fk := range fks {
		if len(fk.Columns) == 0 {
			continue
		}
		out[fk.Columns[0]] = base.RelationInfo{
			Column:   fk.Columns[0],
			ToColumn: fk.ToColumns[0],
			ToTable:  fk.ToTable,
		}
	}
	return out, nil
}

// PrimaryKeyColumns returns the primary key columns in key order.
func (i *Introspection) PrimaryKeyColumns(table string) ([]string, error) {
	ixs, err := i.Indexes(table)
	if err != nil {
		return nil, err
	}
	for _, ix := range ixs {
		if ix.PrimaryKey {
			return ix.Columns, nil
		}
	}
	return nil, nil
}

// TableComment returns the MS_Description extended property of the table.
func (i *Introspection) TableComment(table string) (string, error) {
	var c sql.NullString
	err := i.Conn.DB().Raw(`
		SELECT CAST(ep.value AS nvarchar(max))
		FROM sys.extended_properties ep
		WHERE ep.class = 1 AND ep.major_id = OBJECT_ID(?) AND ep.minor_id = 0
			AND ep.name = 'MS_Description'`, table).Row().Scan(&c)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return c.String, err
}

// IndexDefinition describes an index (including the ones backing a primary
// key or a unique constraint) precisely enough to recreate it.
type IndexDefinition struct {
	Name string
	// Columns are the key columns, in key order.
	Columns []string
	// Orders holds the sort direction of every key column, in key order.
	Orders []m.SortOrder
	// Included are the columns of an INCLUDE clause.
	Included []string
	Unique   bool
	// PrimaryKey and UniqueConstraint mark indexes owned by a constraint;
	// they are dropped and created with ALTER TABLE.
	PrimaryKey       bool
	UniqueConstraint bool
	// Clustered is the index storage type.
	Clustered bool
	// Type is Django's constraint type: "idx" for b-tree (clustered and
	// nonclustered) indexes, the lower-cased type_desc otherwise.
	Type string
	// Filter is the WHERE clause of a filtered index ("" when unfiltered).
	Filter string
}

// Indexes lists every index of a table with everything needed to recreate it.
func (i *Introspection) Indexes(table string) ([]IndexDefinition, error) {
	rows, err := i.Conn.Rows(`
		SELECT
			i.name,
			i.is_unique,
			i.is_primary_key,
			i.is_unique_constraint,
			i.type,
			i.type_desc,
			COALESCE(i.filter_definition, ''),
			c.name,
			ic.is_descending_key,
			ic.is_included_column
		FROM sys.indexes i
		JOIN sys.index_columns ic
			ON ic.object_id = i.object_id AND ic.index_id = i.index_id
		JOIN sys.columns c
			ON c.object_id = ic.object_id AND c.column_id = ic.column_id
		WHERE i.object_id = OBJECT_ID(?) AND i.name IS NOT NULL
		ORDER BY i.index_id, ic.is_included_column, ic.key_ordinal, ic.index_column_id`, table)
	if err != nil {
		return nil, fmt.Errorf("listing the indexes of %s: %w", table, err)
	}
	defer rows.Close()
	var order []string
	byName := map[string]*IndexDefinition{}
	for rows.Next() {
		var (
			name, typeDesc, filter, column string
			unique, primary, uniqueCon     bool
			indexType                      int
			descending, included           bool
		)
		if err := rows.Scan(&name, &unique, &primary, &uniqueCon, &indexType, &typeDesc,
			&filter, &column, &descending, &included); err != nil {
			return nil, err
		}
		d, ok := byName[name]
		if !ok {
			kind := strings.ToLower(typeDesc)
			if indexType == 1 || indexType == 2 {
				kind = indexSuffix
			}
			d = &IndexDefinition{
				Name: name, Unique: unique, PrimaryKey: primary, UniqueConstraint: uniqueCon,
				Clustered: indexType == 1, Type: kind, Filter: filter,
			}
			byName[name] = d
			order = append(order, name)
		}
		if included {
			d.Included = append(d.Included, column)
			continue
		}
		d.Columns = append(d.Columns, column)
		if descending {
			d.Orders = append(d.Orders, m.SortDesc)
		} else {
			d.Orders = append(d.Orders, m.SortAsc)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]IndexDefinition, 0, len(order))
	for _, n := range order {
		out = append(out, *byName[n])
	}
	return out, nil
}

// indexSuffix is Django's Index.suffix, the type reported for b-tree indexes.
const indexSuffix = "idx"

// CheckDefinition is a check constraint with the columns it covers.
type CheckDefinition struct {
	Name string
	// Definition is the constraint expression as SQL Server stores it,
	// including its outer parentheses.
	Definition string
	Columns    []string
}

// Checks lists the check constraints of a table.
func (i *Introspection) Checks(table string) ([]CheckDefinition, error) {
	rows, err := i.Conn.Rows(`
		SELECT cc.name, cc.definition, COALESCE(ccu.COLUMN_NAME, '')
		FROM sys.check_constraints cc
		LEFT JOIN INFORMATION_SCHEMA.CONSTRAINT_COLUMN_USAGE ccu
			ON ccu.CONSTRAINT_NAME = cc.name
			AND ccu.CONSTRAINT_SCHEMA = SCHEMA_NAME(cc.schema_id)
		WHERE cc.parent_object_id = OBJECT_ID(?)
		ORDER BY cc.name`, table)
	if err != nil {
		return nil, fmt.Errorf("reading the check constraints of %s: %w", table, err)
	}
	defer rows.Close()
	var order []string
	byName := map[string]*CheckDefinition{}
	for rows.Next() {
		var name, definition, column string
		if err := rows.Scan(&name, &definition, &column); err != nil {
			return nil, err
		}
		d, ok := byName[name]
		if !ok {
			d = &CheckDefinition{Name: name, Definition: definition}
			byName[name] = d
			order = append(order, name)
		}
		if column != "" {
			d.Columns = append(d.Columns, column)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]CheckDefinition, 0, len(order))
	for _, n := range order {
		out = append(out, *byName[n])
	}
	return out, nil
}

// DefaultDefinition is a named DEFAULT constraint of a column.
type DefaultDefinition struct {
	Name       string
	Column     string
	Definition string
}

// Defaults lists the default constraints of a table.
func (i *Introspection) Defaults(table string) ([]DefaultDefinition, error) {
	rows, err := i.Conn.Rows(`
		SELECT dc.name, c.name, dc.definition
		FROM sys.default_constraints dc
		JOIN sys.columns c
			ON c.object_id = dc.parent_object_id AND c.column_id = dc.parent_column_id
		WHERE dc.parent_object_id = OBJECT_ID(?)
		ORDER BY dc.name`, table)
	if err != nil {
		return nil, fmt.Errorf("reading the default constraints of %s: %w", table, err)
	}
	defer rows.Close()
	var out []DefaultDefinition
	for rows.Next() {
		var d DefaultDefinition
		if err := rows.Scan(&d.Name, &d.Column, &d.Definition); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// DefaultConstraintName returns the name of the DEFAULT constraint of a
// column, or "" when the column has no default.
//
// django: mssql/schema.py DatabaseSchemaEditor._sql_select_default_constraint_name
func (i *Introspection) DefaultConstraintName(table, column string) (string, error) {
	defs, err := i.Defaults(table)
	if err != nil {
		return "", err
	}
	for _, d := range defs {
		if d.Column == column {
			return d.Name, nil
		}
	}
	return "", nil
}

// ForeignKeyDefinition is a foreign key with everything needed to recreate it.
type ForeignKeyDefinition struct {
	Name string
	// Table is the referencing table, ToTable the referenced one.
	Table     string
	Columns   []string
	ToTable   string
	ToColumns []string
	OnDelete  m.ReferentialAction
	OnUpdate  m.ReferentialAction
}

// ForeignKeys lists the foreign keys declared on a table.
func (i *Introspection) ForeignKeys(table string) ([]ForeignKeyDefinition, error) {
	return i.foreignKeys(`fk.parent_object_id = OBJECT_ID(?)`, table)
}

// ReferencingForeignKeys lists the foreign keys of other tables that point at
// this table.
func (i *Introspection) ReferencingForeignKeys(table string) ([]ForeignKeyDefinition, error) {
	return i.foreignKeys(`fk.referenced_object_id = OBJECT_ID(?)`, table)
}

func (i *Introspection) foreignKeys(where, table string) ([]ForeignKeyDefinition, error) {
	rows, err := i.Conn.Rows(`
		SELECT
			fk.name,
			OBJECT_NAME(fk.parent_object_id),
			pc.name,
			rt.name,
			rc.name,
			fk.delete_referential_action_desc,
			fk.update_referential_action_desc
		FROM sys.foreign_keys fk
		JOIN sys.foreign_key_columns fkc ON fkc.constraint_object_id = fk.object_id
		JOIN sys.columns pc
			ON pc.object_id = fkc.parent_object_id AND pc.column_id = fkc.parent_column_id
		JOIN sys.tables rt ON rt.object_id = fkc.referenced_object_id
		JOIN sys.columns rc
			ON rc.object_id = fkc.referenced_object_id AND rc.column_id = fkc.referenced_column_id
		WHERE `+where+`
		ORDER BY fk.name, fkc.constraint_column_id`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var order []string
	byName := map[string]*ForeignKeyDefinition{}
	for rows.Next() {
		var name, parent, column, refTable, refColumn, onDelete, onUpdate string
		if err := rows.Scan(&name, &parent, &column, &refTable, &refColumn, &onDelete, &onUpdate); err != nil {
			return nil, err
		}
		d, ok := byName[name]
		if !ok {
			onDel, err := referentialAction(onDelete)
			if err != nil {
				return nil, fmt.Errorf("reading the foreign key %s: %w", name, err)
			}
			onUpd, err := referentialAction(onUpdate)
			if err != nil {
				return nil, fmt.Errorf("reading the foreign key %s: %w", name, err)
			}
			d = &ForeignKeyDefinition{
				Name: name, Table: parent, ToTable: refTable,
				OnDelete: onDel, OnUpdate: onUpd,
			}
			byName[name] = d
			order = append(order, name)
		}
		d.Columns = append(d.Columns, column)
		d.ToColumns = append(d.ToColumns, refColumn)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]ForeignKeyDefinition, 0, len(order))
	for _, n := range order {
		out = append(out, *byName[n])
	}
	return out, nil
}

// referentialAction turns sys.foreign_keys' *_referential_action_desc into
// the action the declaration side spells. NO_ACTION is the database default
// and maps to the zero value, which leaves the clause out; SQL Server has no
// RESTRICT. A description outside the four SQL Server documents is an error
// rather than the zero value, because silently dropping a CASCADE would make
// a recreated foreign key differ from the one that was read.
func referentialAction(desc string) (m.ReferentialAction, error) {
	switch desc {
	case "NO_ACTION":
		return "", nil
	case "CASCADE":
		return m.Cascade, nil
	case "SET_NULL":
		return m.SetNull, nil
	case "SET_DEFAULT":
		return m.SetDefault, nil
	}
	return "", fmt.Errorf("unknown referential action %q", desc)
}

// Constraints returns the constraints and indexes of a table.
//
// Primary keys, unique constraints, foreign keys, check constraints and
// default constraints are reported from the catalog views that own them;
// indexes are added afterwards unless a constraint of the same name was
// already reported (a primary key and a unique constraint are backed by an
// index of the same name).
//
// django: mssql/introspection.py DatabaseIntrospection.get_constraints
func (i *Introspection) Constraints(table string) (map[string]base.ConstraintInfo, error) {
	out := map[string]base.ConstraintInfo{}
	ixs, err := i.Indexes(table)
	if err != nil {
		return nil, err
	}
	for _, ix := range ixs {
		if !ix.PrimaryKey && !ix.UniqueConstraint {
			continue
		}
		out[ix.Name] = base.ConstraintInfo{
			Columns:    ix.Columns,
			PrimaryKey: ix.PrimaryKey,
			Unique:     true,
		}
	}
	fks, err := i.ForeignKeys(table)
	if err != nil {
		return nil, err
	}
	for _, fk := range fks {
		out[fk.Name] = base.ConstraintInfo{
			Columns:    fk.Columns,
			ForeignKey: &base.ForeignKeyTarget{Table: fk.ToTable, Columns: slices.Clone(fk.ToColumns)},
		}
	}
	checks, err := i.Checks(table)
	if err != nil {
		return nil, err
	}
	for _, c := range checks {
		cols := append([]string(nil), c.Columns...)
		slices.Sort(cols)
		out[c.Name] = base.ConstraintInfo{Columns: cols, Check: true, Definition: c.Definition}
	}
	defs, err := i.Defaults(table)
	if err != nil {
		return nil, err
	}
	for _, d := range defs {
		out[d.Name] = base.ConstraintInfo{Columns: []string{d.Column}, Definition: d.Definition}
	}
	for _, ix := range ixs {
		if _, ok := out[ix.Name]; ok {
			continue
		}
		out[ix.Name] = base.ConstraintInfo{
			Columns:    ix.Columns,
			Orders:     ix.Orders,
			PrimaryKey: ix.PrimaryKey,
			Unique:     ix.Unique,
			Index:      true,
			Type:       ix.Type,
			Definition: ix.Filter,
		}
	}
	return out, nil
}
