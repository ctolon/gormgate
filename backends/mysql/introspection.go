package mysql

import (
	"database/sql"
	"fmt"
	"regexp"
	"strings"

	"github.com/ctolon/gormgate/backends/base"
	m "github.com/ctolon/gormgate/migrations"
)

// Introspection reads MySQL's information_schema.
//
// django: db/backends/mysql/introspection.py DatabaseIntrospection
type Introspection struct {
	Conn   *base.Conn
	Server Server
	// CheckConstraintQuery returns (constraint_name, check_clause) for the
	// table given as its only parameter. It is empty when the server cannot
	// introspect check constraints.
	CheckConstraintQuery string
}

// MySQL's information_schema.check_constraints has no table_name column, so
// the table is reached through table_constraints; MariaDB has one.
const (
	mysqlCheckQuery = `
		SELECT cc.constraint_name, cc.check_clause
		FROM information_schema.check_constraints AS cc,
			information_schema.table_constraints AS tc
		WHERE cc.constraint_schema = DATABASE()
			AND tc.table_schema = cc.constraint_schema
			AND cc.constraint_name = tc.constraint_name
			AND tc.constraint_type = 'CHECK'
			AND tc.table_name = ?`
	mariaDBCheckQuery = `
		SELECT c.constraint_name, c.check_clause
		FROM information_schema.check_constraints AS c
		WHERE c.constraint_schema = DATABASE() AND c.table_name = ?`
)

// NewIntrospection builds the introspection of a MySQL or MariaDB server.
func NewIntrospection(c *base.Conn, s Server) *Introspection {
	i := &Introspection{Conn: c, Server: s}
	if CanIntrospectCheckConstraints(s) {
		if s.MariaDB {
			i.CheckConstraintQuery = mariaDBCheckQuery
		} else {
			i.CheckConstraintQuery = mysqlCheckQuery
		}
	}
	return i
}

// IdentifierConverter returns name unchanged: MySQL stores identifiers as
// they were written.
func (i *Introspection) IdentifierConverter(name string) string { return name }

// TableNames lists the tables (and views) of the current database.
//
// django: mysql/introspection.py DatabaseIntrospection.get_table_list
func (i *Introspection) TableNames(includeViews bool) ([]base.TableInfo, error) {
	rows, err := i.Conn.Rows(`
		SELECT table_name, table_type, table_comment
		FROM information_schema.tables
		WHERE table_schema = DATABASE()
		ORDER BY table_name`)
	if err != nil {
		return nil, fmt.Errorf("listing tables: %w", err)
	}
	defer rows.Close()
	var out []base.TableInfo
	for rows.Next() {
		var name, kind string
		var comment sql.NullString
		if err := rows.Scan(&name, &kind, &comment); err != nil {
			return nil, err
		}
		t := base.TableInfo{Name: name, Comment: comment.String}
		switch kind {
		case "BASE TABLE":
			t.Type = "t"
		case "VIEW":
			t.Type = "v"
			// A view has no comment of its own; MySQL reports "VIEW".
			t.Comment = ""
		default:
			continue
		}
		if !includeViews && t.Type == "v" {
			continue
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// TableDescription describes the columns of a table in ordinal order.
//
// django: mysql/introspection.py DatabaseIntrospection.get_table_description
func (i *Introspection) TableDescription(table string) ([]base.ColumnInfo, error) {
	rows, err := i.Conn.Rows(`
		SELECT
			column_name, column_type, is_nullable, column_default, extra,
			column_comment, collation_name, character_maximum_length,
			numeric_precision, numeric_scale
		FROM information_schema.columns
		WHERE table_schema = DATABASE() AND table_name = ?
		ORDER BY ordinal_position`, table)
	if err != nil {
		return nil, fmt.Errorf("describing the columns of %s: %w", table, err)
	}
	defer rows.Close()
	var out []base.ColumnInfo
	for rows.Next() {
		var c base.ColumnInfo
		var nullable, extra string
		var def, comment, collation sql.NullString
		var size, precision, scale sql.NullInt64
		if err := rows.Scan(&c.Name, &c.Type, &nullable, &def, &extra, &comment, &collation, &size, &precision, &scale); err != nil {
			return nil, err
		}
		c.Null = strings.EqualFold(nullable, "YES")
		c.AutoIncrement = strings.Contains(strings.ToLower(extra), "auto_increment")
		c.Comment = comment.String
		c.Collation = collation.String
		c.Size, c.Precision, c.Scale = int(size.Int64), int(precision.Int64), int(scale.Int64)
		// MariaDB writes the implicit NULL default of a nullable column as
		// the unquoted string "NULL" (a string default would be quoted), and
		// reports SQL NULL for the same column once DROP DEFAULT ran. Both
		// mean "no default", so they are reported the same way.
		if def.Valid && !(i.Server.MariaDB && c.Null && def.String == "NULL") {
			d := def.String
			c.Default = &d
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Sequences returns the AUTO_INCREMENT column of the table; MySQL allows
// only one per table.
//
// django: mysql/introspection.py DatabaseIntrospection.get_sequences
func (i *Introspection) Sequences(table string) ([]base.SequenceInfo, error) {
	cols, err := i.TableDescription(table)
	if err != nil {
		return nil, err
	}
	for _, c := range cols {
		if c.AutoIncrement {
			return []base.SequenceInfo{{Table: table, Column: c.Name}}, nil
		}
	}
	return nil, nil
}

// Relations maps every foreign-key column of a table to its target.
//
// django: mysql/introspection.py DatabaseIntrospection.get_relations
func (i *Introspection) Relations(table string) (map[string]base.RelationInfo, error) {
	rows, err := i.Conn.Rows(`
		SELECT column_name, referenced_column_name, referenced_table_name
		FROM information_schema.key_column_usage
		WHERE table_name = ?
			AND table_schema = DATABASE()
			AND referenced_table_schema = DATABASE()
			AND referenced_table_name IS NOT NULL
			AND referenced_column_name IS NOT NULL`, table)
	if err != nil {
		return nil, fmt.Errorf("listing the foreign keys of %s: %w", table, err)
	}
	defer rows.Close()
	out := map[string]base.RelationInfo{}
	for rows.Next() {
		var r base.RelationInfo
		if err := rows.Scan(&r.Column, &r.ToColumn, &r.ToTable); err != nil {
			return nil, err
		}
		out[r.Column] = r
	}
	return out, rows.Err()
}

// PrimaryKeyColumns returns the primary key columns in order.
func (i *Introspection) PrimaryKeyColumns(table string) ([]string, error) {
	cons, err := i.Constraints(table)
	if err != nil {
		return nil, err
	}
	return base.FirstPrimaryKey(cons), nil
}

// TableComment returns the table comment.
func (i *Introspection) TableComment(table string) (string, error) {
	var c sql.NullString
	err := i.Conn.DB().Raw(`
		SELECT table_comment FROM information_schema.tables
		WHERE table_schema = DATABASE() AND table_name = ?`, table).Row().Scan(&c)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return c.String, err
}

// StorageEngine returns the storage engine of a table, or the server
// default when the table does not exist.
//
// django: mysql/introspection.py DatabaseIntrospection.get_storage_engine
func (i *Introspection) StorageEngine(table string) (string, error) {
	var e sql.NullString
	err := i.Conn.DB().Raw(`
		SELECT engine FROM information_schema.tables
		WHERE table_schema = DATABASE() AND table_name = ?`, table).Row().Scan(&e)
	if err == sql.ErrNoRows || !e.Valid {
		return i.Server.StorageEngine, nil
	}
	return e.String, err
}

// quotedName matches the backtick-quoted identifiers of a check clause.
var quotedName = regexp.MustCompile("`((?:[^`]|``)*)`")

// parseConstraintColumns returns the table columns a check clause mentions,
// in order of appearance and without repetitions.
//
// django: mysql/introspection.py DatabaseIntrospection._parse_constraint_columns
func parseConstraintColumns(checkClause string, columns map[string]bool) []string {
	var out []string
	seen := map[string]bool{}
	for _, mt := range quotedName.FindAllStringSubmatch(checkClause, -1) {
		name := strings.ReplaceAll(mt[1], "``", "`")
		if columns[name] && !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	return out
}

// Constraints returns the constraints and indexes of a table.
//
// django: mysql/introspection.py DatabaseIntrospection.get_constraints
func (i *Introspection) Constraints(table string) (map[string]base.ConstraintInfo, error) {
	out := map[string]base.ConstraintInfo{}
	ordered := map[string][]string{}
	addColumn := func(name, column string) {
		for _, c := range ordered[name] {
			if c == column {
				return
			}
		}
		ordered[name] = append(ordered[name], column)
	}

	// Primary keys, unique constraints and foreign keys.
	rows, err := i.Conn.Rows(`
		SELECT kc.constraint_name, kc.column_name,
			kc.referenced_table_name, kc.referenced_column_name,
			c.constraint_type
		FROM information_schema.key_column_usage AS kc,
			information_schema.table_constraints AS c
		WHERE kc.table_schema = DATABASE()
			AND (kc.referenced_table_schema = DATABASE() OR kc.referenced_table_schema IS NULL)
			AND c.table_schema = kc.table_schema
			AND c.table_name = kc.table_name
			AND c.constraint_name = kc.constraint_name
			AND c.constraint_type != 'CHECK'
			AND kc.table_name = ?
		ORDER BY kc.ordinal_position`, table)
	if err != nil {
		return nil, fmt.Errorf("reading the constraints of %s: %w", table, err)
	}
	for rows.Next() {
		var name, column, kind string
		var refTable, refColumn sql.NullString
		if err := rows.Scan(&name, &column, &refTable, &refColumn, &kind); err != nil {
			rows.Close()
			return nil, err
		}
		if _, ok := out[name]; !ok {
			info := base.ConstraintInfo{
				PrimaryKey: kind == "PRIMARY KEY",
				Unique:     kind == "PRIMARY KEY" || kind == "UNIQUE",
			}
			if refColumn.Valid {
				info.ForeignKey = &base.ForeignKeyTarget{Table: refTable.String}
			}
			out[name] = info
		}
		// The rows arrive in ordinal_position order, so appending builds
		// the referenced columns of a multi-column key in key order.
		if fk := out[name].ForeignKey; fk != nil && refColumn.Valid {
			fk.Columns = append(fk.Columns, refColumn.String)
		}
		addColumn(name, column)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	// Check constraints.
	if i.CheckConstraintQuery != "" {
		cols, err := i.TableDescription(table)
		if err != nil {
			return nil, err
		}
		columns := map[string]bool{}
		for _, c := range cols {
			columns[c.Name] = true
		}
		rows, err := i.Conn.Rows(i.CheckConstraintQuery, table)
		if err != nil {
			return nil, fmt.Errorf("reading the constraints of %s: %w", table, err)
		}
		unnamed := 0
		for rows.Next() {
			var name, clause string
			if err := rows.Scan(&name, &clause); err != nil {
				rows.Close()
				return nil, err
			}
			checkColumns := parseConstraintColumns(clause, columns)
			// Unnamed unique and check column constraints have the same name
			// as a column; make them unique.
			if len(checkColumns) == 1 && checkColumns[0] == name {
				unnamed++
				name = fmt.Sprintf("__unnamed_constraint_%d__", unnamed)
			}
			out[name] = base.ConstraintInfo{Check: true, Definition: clause}
			ordered[name] = checkColumns
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}

	// Indexes.
	orders := map[string][]m.SortOrder{}
	rows, err = i.Conn.Rows(`
		SELECT index_name, non_unique, column_name, collation, index_type
		FROM information_schema.statistics
		WHERE table_schema = DATABASE() AND table_name = ?
		ORDER BY index_name, seq_in_index`, table)
	if err != nil {
		return nil, fmt.Errorf("reading the constraints of %s: %w", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var name, indexType string
		var nonUnique int64
		var column, collation sql.NullString
		if err := rows.Scan(&name, &nonUnique, &column, &collation, &indexType); err != nil {
			return nil, err
		}
		info, ok := out[name]
		if !ok {
			info = base.ConstraintInfo{Unique: nonUnique == 0}
		}
		info.Index = true
		info.Type = strings.ToLower(indexType)
		out[name] = info
		addColumn(name, column.String)
		if i.Conn.Features().SupportsIndexColumnOrdering {
			// information_schema.statistics.collation is 'A', 'D' or NULL.
			// Only 'D' is descending; 'A' and an unsorted (NULL) element
			// are both reported as ascending, which is what Django does
			// and what the index then behaves as.
			order := m.SortAsc
			if collation.String == "D" {
				order = m.SortDesc
			}
			orders[name] = append(orders[name], order)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for name, info := range out {
		info.Columns = ordered[name]
		info.Orders = orders[name]
		out[name] = info
	}
	return out, nil
}
