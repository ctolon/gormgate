package sqlite3

import (
	"database/sql"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/ctolon/gormgate/backends/base"
)

// Introspection reads SQLite's schema.
//
// django: db/backends/sqlite3/introspection.py
type Introspection struct {
	Conn *base.Conn
}

// IdentifierConverter returns name unchanged.
func (i *Introspection) IdentifierConverter(name string) string { return name }

// TableNames lists tables (and views). The sqlite_sequence system table
// used for autoincrement key generation is skipped.
//
// django: sqlite3/introspection.py DatabaseIntrospection.get_table_list
func (i *Introspection) TableNames(includeViews bool) ([]base.TableInfo, error) {
	rows, err := i.Conn.Rows(`
		SELECT name, type FROM sqlite_master
		WHERE type IN ('table', 'view') AND NOT name = 'sqlite_sequence'
		ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("listing tables: %w", err)
	}
	defer rows.Close()
	var out []base.TableInfo
	for rows.Next() {
		var name, typ string
		if err := rows.Scan(&name, &typ); err != nil {
			return nil, err
		}
		t := base.TableInfo{Name: name, Type: typ[:1]}
		if !includeViews && t.Type == "v" {
			continue
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

var fieldSizeRe = regexp.MustCompile(`^\s*(?i:var)?(?i:char)\s*\(\s*(\d+)\s*\)\s*$`)

// fieldSize extracts the size of a "varchar(11)" type name.
//
// django: sqlite3/introspection.py get_field_size
func fieldSize(name string) int {
	m := fieldSizeRe.FindStringSubmatch(name)
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

// tableSQL returns the CREATE TABLE statement of a table ("" for views).
func (i *Introspection) tableSQL(table string) (string, error) {
	var s sql.NullString
	err := i.Conn.DB().Raw(
		`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Row().Scan(&s)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return s.String, err
}

// TableDescription describes the columns of a table.
//
// django: sqlite3/introspection.py DatabaseIntrospection.get_table_description
func (i *Introspection) TableDescription(table string) ([]base.ColumnInfo, error) {
	rows, err := i.Conn.Rows(`SELECT name, type, "notnull", dflt_value, hidden FROM pragma_table_xinfo(?)`, table)
	if err != nil {
		return nil, fmt.Errorf("describing the columns of %s: %w", table, err)
	}
	type raw struct {
		name, typ string
		notNull   bool
		def       sql.NullString
		hidden    int
	}
	var list []raw
	for rows.Next() {
		var r raw
		if err := rows.Scan(&r.name, &r.typ, &r.notNull, &r.def, &r.hidden); err != nil {
			rows.Close()
			return nil, err
		}
		// 0: normal column, 2: virtual generated, 3: stored generated.
		if r.hidden == 0 || r.hidden == 2 || r.hidden == 3 {
			list = append(list, r)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sqlText, err := i.tableSQL(table)
	if err != nil {
		return nil, err
	}
	parsed := parseTableDef(sqlText)
	byName := map[string]columnDef{}
	for _, c := range parsed.Columns {
		byName[c.Name] = c
	}
	out := make([]base.ColumnInfo, 0, len(list))
	for _, r := range list {
		c := base.ColumnInfo{
			Name: r.name,
			Type: r.typ,
			Size: fieldSize(r.typ),
			Null: !r.notNull,
		}
		if r.def.Valid {
			d := r.def.String
			c.Default = &d
		}
		if p, ok := byName[r.name]; ok {
			c.AutoIncrement = p.AutoIncrement
			c.Collation = p.Collation
		}
		out = append(out, c)
	}
	return out, nil
}

// Sequences returns the primary key column, which is the only "sequence"
// SQLite has (the rowid alias).
//
// django: sqlite3/introspection.py DatabaseIntrospection.get_sequences
func (i *Introspection) Sequences(table string) ([]base.SequenceInfo, error) {
	cols, err := i.PrimaryKeyColumns(table)
	if err != nil || len(cols) == 0 {
		return nil, err
	}
	return []base.SequenceInfo{{Table: table, Column: cols[0]}}, nil
}

// fkRow is one row of PRAGMA foreign_key_list.
type fkRow struct {
	ID       int
	Seq      int
	Table    string
	From     string
	To       string
	OnUpdate string
	OnDelete string
}

// foreignKeyList reads PRAGMA foreign_key_list, ordered by (id, seq).
func (i *Introspection) foreignKeyList(table string) ([]fkRow, error) {
	rows, err := i.Conn.Rows(`SELECT id, seq, "table", "from", "to" FROM pragma_foreign_key_list(?) ORDER BY id, seq`, table)
	if err != nil {
		return nil, fmt.Errorf("listing the foreign keys of %s: %w", table, err)
	}
	defer rows.Close()
	var out []fkRow
	for rows.Next() {
		var r fkRow
		var from, to sql.NullString
		if err := rows.Scan(&r.ID, &r.Seq, &r.Table, &from, &to); err != nil {
			return nil, err
		}
		r.From, r.To = from.String, to.String
		out = append(out, r)
	}
	return out, rows.Err()
}

// Relations maps foreign-key columns to their targets.
//
// django: sqlite3/introspection.py DatabaseIntrospection.get_relations
func (i *Introspection) Relations(table string) (map[string]base.RelationInfo, error) {
	list, err := i.foreignKeyList(table)
	if err != nil {
		return nil, err
	}
	out := map[string]base.RelationInfo{}
	for _, r := range list {
		out[r.From] = base.RelationInfo{Column: r.From, ToColumn: r.To, ToTable: r.Table}
	}
	return out, nil
}

// PrimaryKeyColumns returns the primary key columns in key order.
//
// django: sqlite3/introspection.py DatabaseIntrospection.get_primary_key_columns
func (i *Introspection) PrimaryKeyColumns(table string) ([]string, error) {
	rows, err := i.Conn.Rows(`SELECT name FROM pragma_table_info(?) WHERE pk > 0 ORDER BY pk`, table)
	if err != nil {
		return nil, fmt.Errorf("reading the primary key of %s: %w", table, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// TableComment returns "": SQLite has no comments.
func (i *Introspection) TableComment(table string) (string, error) { return "", nil }

// Constraints returns constraints and indexes of a table.
//
// django: sqlite3/introspection.py DatabaseIntrospection.get_constraints
func (i *Introspection) Constraints(table string) (map[string]base.ConstraintInfo, error) {
	out := map[string]base.ConstraintInfo{}
	sqlText, err := i.tableSQL(table)
	if err != nil {
		return nil, err
	}
	var parsed tableDef
	if sqlText != "" {
		// Find the inline constraints: PRAGMAs report neither their names
		// nor the CHECK constraints.
		parsed = parseTableDef(sqlText)
		cols, err := i.TableDescription(table)
		if err != nil {
			return nil, err
		}
		columns := map[string]bool{}
		for _, c := range cols {
			columns[c.Name] = true
		}
		for _, c := range parsed.Constraints {
			switch c.Kind {
			case "UNIQUE":
				out[c.Name] = base.ConstraintInfo{Columns: c.Columns, Unique: true}
			case "CHECK":
				cc := filterColumns(c.Columns, columns)
				if len(cc) == 0 {
					continue
				}
				out[c.Name] = base.ConstraintInfo{Columns: cc, Check: true}
			}
		}
	}

	// Get the index info.
	indexes, err := i.Conn.Rows(`SELECT name, "unique" FROM pragma_index_list(?)`, table)
	if err != nil {
		return nil, fmt.Errorf("listing the indexes of %s: %w", table, err)
	}
	type indexInfo struct {
		name   string
		unique bool
	}
	var list []indexInfo
	for indexes.Next() {
		var ix indexInfo
		if err := indexes.Scan(&ix.name, &ix.unique); err != nil {
			indexes.Close()
			return nil, err
		}
		list = append(list, ix)
	}
	indexes.Close()
	if err := indexes.Err(); err != nil {
		return nil, err
	}
	for _, ix := range list {
		var indexSQL sql.NullString
		err := i.Conn.DB().Raw(`SELECT sql FROM sqlite_master WHERE type = 'index' AND name = ?`, ix.name).Row().Scan(&indexSQL)
		if err != nil && err != sql.ErrNoRows {
			return nil, err
		}
		// Inline constraints are already detected above; the indexes
		// SQLite creates for them have no SQL of their own.
		if !indexSQL.Valid || indexSQL.String == "" {
			continue
		}
		cols, err := i.Conn.Rows(`SELECT name FROM pragma_index_info(?) ORDER BY seqno`, ix.name)
		if err != nil {
			return nil, fmt.Errorf("listing the columns of index %s of %s: %w", ix.name, table, err)
		}
		info := base.ConstraintInfo{Unique: ix.unique, Index: true, Type: "btree"}
		for cols.Next() {
			var n sql.NullString
			if err := cols.Scan(&n); err != nil {
				cols.Close()
				return nil, err
			}
			info.Columns = append(info.Columns, n.String)
		}
		cols.Close()
		if err := cols.Err(); err != nil {
			return nil, err
		}
		info.Orders = indexColumnOrders(indexSQL.String)
		info.Definition = indexSQL.String
		out[ix.name] = info
	}

	// Get the primary key. SQLite doesn't name it, so, like Django, we
	// invent a name: the backend never drops a primary key by name, it
	// remakes the table instead.
	pk, err := i.PrimaryKeyColumns(table)
	if err != nil {
		return nil, err
	}
	if len(pk) > 0 {
		out["__primary__"] = base.ConstraintInfo{
			Columns:    pk,
			PrimaryKey: true,
			// It's not actually a unique constraint.
			Unique: false,
		}
	}

	// Get the foreign keys; their names only exist in the CREATE TABLE
	// statement.
	fks, err := i.foreignKeyList(table)
	if err != nil {
		return nil, err
	}
	var parsedFKs []constraintDef
	for _, c := range parsed.Constraints {
		if c.Kind == "FOREIGN KEY" {
			parsedFKs = append(parsedFKs, c)
		}
	}
	used := make([]bool, len(parsedFKs))
	groups := map[int][]fkRow{}
	var ids []int
	for _, r := range fks {
		if _, ok := groups[r.ID]; !ok {
			ids = append(ids, r.ID)
		}
		groups[r.ID] = append(groups[r.ID], r)
	}
	for n, id := range ids {
		g := groups[id]
		var columns []string
		for _, r := range g {
			columns = append(columns, r.From)
		}
		var refColumns []string
		for _, r := range g {
			refColumns = append(refColumns, r.To)
		}
		name := ""
		for k, c := range parsedFKs {
			if used[k] || c.ToTable != g[0].Table || !slices.EqualFunc(c.Columns, columns, strings.EqualFold) {
				continue
			}
			if len(c.ToCols) > 0 && !slices.EqualFunc(c.ToCols, refColumns, strings.EqualFold) {
				continue
			}
			used[k] = true
			name = c.Name
			break
		}
		if name == "" {
			name = "fk_" + strconv.Itoa(n)
		}
		out[name] = base.ConstraintInfo{
			Columns:    columns,
			ForeignKey: &base.ForeignKeyTarget{Table: g[0].Table, Columns: refColumns},
		}
	}
	return out, nil
}
