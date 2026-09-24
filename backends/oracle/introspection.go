package oracle

import (
	"database/sql"
	"fmt"
	"regexp"
	"strings"

	"github.com/ctolon/gormgate/backends/base"
)

// Introspection reads Oracle's data dictionary.
//
// django: db/backends/oracle/introspection.py
type Introspection struct {
	Conn *base.Conn
}

// IdentifierConverter returns the name unchanged.
//
// Django's Oracle backend quotes every identifier upper-cased and therefore
// lower-cases the names it reads back. gorm-oracle quotes identifiers
// verbatim (oracle.go Dialector.QuoteTo), so the names in the data
// dictionary are exactly the names gormgate and gorm use.
//
// django: oracle/introspection.py DatabaseIntrospection.identifier_converter
func (i *Introspection) IdentifierConverter(name string) string { return name }

// TableNames lists tables (and views).
//
// django: oracle/introspection.py DatabaseIntrospection.get_table_list
func (i *Introspection) TableNames(includeViews bool) ([]base.TableInfo, error) {
	rows, err := i.Conn.Rows(`
		SELECT t.table_name, 't', NVL(c.comments, '')
		FROM user_tables t
		LEFT OUTER JOIN user_tab_comments c ON c.table_name = t.table_name
		WHERE NOT EXISTS (
			SELECT 1 FROM user_mviews mv WHERE mv.mview_name = t.table_name
		)
		UNION ALL
		SELECT view_name, 'v', '' FROM user_views
		UNION ALL
		SELECT mview_name, 'm', '' FROM user_mviews
		ORDER BY 1`)
	if err != nil {
		return nil, fmt.Errorf("listing tables: %w", err)
	}
	defer rows.Close()
	var out []base.TableInfo
	for rows.Next() {
		var t base.TableInfo
		if err := rows.Scan(&t.Name, &t.Type, &t.Comment); err != nil {
			return nil, err
		}
		if !includeViews && (t.Type == "v" || t.Type == "m") {
			continue
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// TableDescription describes the columns of a table in column order.
// Hidden columns (the virtual columns behind function-based indexes) are
// left out, exactly like the cursor description Django reads.
//
// django: oracle/introspection.py DatabaseIntrospection.get_table_description
func (i *Introspection) TableDescription(table string) ([]base.ColumnInfo, error) {
	var defaultCollation sql.NullString
	err := i.Conn.DB().Raw(
		`SELECT default_collation FROM user_tables WHERE table_name = ?`, table).Row().Scan(&defaultCollation)
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	rows, err := i.Conn.Rows(`
		SELECT
			c.column_name,
			c.data_type,
			c.data_length,
			NVL(c.char_length, 0),
			NVL(c.char_used, ' '),
			NVL(c.data_precision, -1),
			NVL(c.data_scale, -1),
			c.nullable,
			c.identity_column,
			NVL(c.collation, ''),
			NVL(cc.comments, ''),
			c.data_default
		FROM user_tab_cols c
		LEFT OUTER JOIN user_col_comments cc
			ON cc.table_name = c.table_name AND cc.column_name = c.column_name
		WHERE c.table_name = ? AND c.hidden_column = 'NO'
		ORDER BY c.column_id`, table)
	if err != nil {
		return nil, fmt.Errorf("describing the columns of %s: %w", table, err)
	}
	defer rows.Close()
	var out []base.ColumnInfo
	for rows.Next() {
		var (
			c                            base.ColumnInfo
			dataType, nullable, identity string
			dataLength, charLength       int
			charUsed, collation          string
			precision, scale             int
			def                          sql.NullString
		)
		if err := rows.Scan(&c.Name, &dataType, &dataLength, &charLength, &charUsed,
			&precision, &scale, &nullable, &identity, &collation, &c.Comment, &def); err != nil {
			return nil, err
		}
		c.Size = dataLength
		if charUsed != " " && charUsed != "" {
			c.Size = charLength
		}
		if precision >= 0 {
			c.Precision = precision
		}
		if scale >= 0 {
			c.Scale = scale
		}
		c.Type = columnType(dataType, c.Size, precision, scale)
		c.Null = nullable == "Y"
		c.AutoIncrement = identity == "YES"
		if collation != defaultCollation.String {
			c.Collation = collation
		}
		// Django drops a default of "NULL", which is what a dropped default
		// leaves behind (ALTER TABLE ... MODIFY col DEFAULT NULL).
		if def.Valid {
			if d := strings.TrimRight(def.String, " \t\r\n"); d != "" && d != "NULL" {
				c.Default = &d
			}
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// charTypes are the types whose data dictionary entry needs the length to
// be complete.
var charTypes = map[string]bool{
	"VARCHAR2": true, "NVARCHAR2": true, "CHAR": true, "NCHAR": true, "VARCHAR": true, "RAW": true,
}

// columnType renders the column type the way it is written in DDL.
// user_tab_cols reports the type name and its parameters separately
// (NUMBER + precision/scale, VARCHAR2 + length), except for the timestamp
// and interval types, whose data_type already carries them.
func columnType(dataType string, size, precision, scale int) string {
	switch {
	case charTypes[dataType]:
		return fmt.Sprintf("%s(%d)", dataType, size)
	case dataType == "NUMBER":
		switch {
		case precision < 0:
			return "NUMBER"
		case scale <= 0:
			return fmt.Sprintf("NUMBER(%d)", precision)
		default:
			return fmt.Sprintf("NUMBER(%d,%d)", precision, scale)
		}
	case dataType == "FLOAT":
		if precision < 0 {
			return "FLOAT"
		}
		return fmt.Sprintf("FLOAT(%d)", precision)
	}
	return dataType
}

// Sequences lists the identity sequence feeding the primary key.
//
// django: oracle/introspection.py DatabaseIntrospection.get_sequences
func (i *Introspection) Sequences(table string) ([]base.SequenceInfo, error) {
	rows, err := i.Conn.Rows(`
		SELECT ic.sequence_name, ic.column_name
		FROM user_tab_identity_cols ic, user_constraints cons, user_cons_columns cols
		WHERE cons.constraint_name = cols.constraint_name
			AND cons.table_name = ic.table_name
			AND cols.column_name = ic.column_name
			AND cons.constraint_type = 'P'
			AND ic.table_name = ?`, table)
	if err != nil {
		return nil, fmt.Errorf("listing the sequences of %s: %w", table, err)
	}
	defer rows.Close()
	var out []base.SequenceInfo
	for rows.Next() {
		s := base.SequenceInfo{Table: table}
		if err := rows.Scan(&s.Name, &s.Column); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// Relations maps foreign-key columns to their targets.
//
// django: oracle/introspection.py DatabaseIntrospection.get_relations
func (i *Introspection) Relations(table string) (map[string]base.RelationInfo, error) {
	rows, err := i.Conn.Rows(`
		SELECT ca.column_name, cb.table_name, cb.column_name
		FROM user_constraints cons, user_cons_columns ca, user_cons_columns cb
		WHERE cons.table_name = ?
			AND cons.constraint_name = ca.constraint_name
			AND cons.r_constraint_name = cb.constraint_name
			AND ca.position = cb.position`, table)
	if err != nil {
		return nil, fmt.Errorf("listing the foreign keys of %s: %w", table, err)
	}
	defer rows.Close()
	out := map[string]base.RelationInfo{}
	for rows.Next() {
		var r base.RelationInfo
		if err := rows.Scan(&r.Column, &r.ToTable, &r.ToColumn); err != nil {
			return nil, err
		}
		out[r.Column] = r
	}
	return out, rows.Err()
}

// PrimaryKeyColumns returns the primary key columns in order.
//
// django: oracle/introspection.py DatabaseIntrospection.get_primary_key_columns
func (i *Introspection) PrimaryKeyColumns(table string) ([]string, error) {
	rows, err := i.Conn.Rows(`
		SELECT cols.column_name
		FROM user_constraints cons, user_cons_columns cols
		WHERE cons.constraint_name = cols.constraint_name
			AND cons.constraint_type = 'P'
			AND cons.table_name = ?
		ORDER BY cols.position`, table)
	if err != nil {
		return nil, fmt.Errorf("reading the primary key of %s: %w", table, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// TableComment returns the table comment.
func (i *Introspection) TableComment(table string) (string, error) {
	var c sql.NullString
	err := i.Conn.DB().Raw(
		`SELECT comments FROM user_tab_comments WHERE table_name = ?`, table).Row().Scan(&c)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return c.String, err
}

// hiddenColumn matches the virtual columns Oracle creates for the elements
// of a function-based index.
var hiddenColumn = regexp.MustCompile(`^SYS_NC\d+\$$`)

func splitList(s sql.NullString) []string {
	if !s.Valid || s.String == "" {
		return nil
	}
	return strings.Split(s.String, ",")
}

// Constraints returns the constraints and indexes of a table.
//
// django: oracle/introspection.py DatabaseIntrospection.get_constraints
func (i *Introspection) Constraints(table string) (map[string]base.ConstraintInfo, error) {
	out := map[string]base.ConstraintInfo{}
	// Primary keys, unique constraints and checks. Oracle's NOT NULL
	// column attribute is a check constraint with a generated name, and
	// Django reports it as one.
	rows, err := i.Conn.Rows(`
		SELECT
			cons.constraint_name,
			LISTAGG(cols.column_name, ',') WITHIN GROUP (ORDER BY cols.position),
			cons.constraint_type
		FROM user_constraints cons
		LEFT OUTER JOIN user_cons_columns cols ON cons.constraint_name = cols.constraint_name
		WHERE cons.constraint_type IN ('P', 'U', 'C') AND cons.table_name = ?
		GROUP BY cons.constraint_name, cons.constraint_type`, table)
	if err != nil {
		return nil, fmt.Errorf("reading the constraints of %s: %w", table, err)
	}
	for rows.Next() {
		var name, kind string
		var cols sql.NullString
		if err := rows.Scan(&name, &cols, &kind); err != nil {
			rows.Close()
			return nil, err
		}
		out[name] = base.ConstraintInfo{
			Columns:    splitList(cols),
			PrimaryKey: kind == "P",
			Unique:     kind == "P" || kind == "U",
			Check:      kind == "C",
			// Every unique constraint comes with an index.
			Index: kind == "P" || kind == "U",
		}
	}
	rows.Close()
	// Foreign keys.
	rows, err = i.Conn.Rows(`
		SELECT
			cons.constraint_name,
			(SELECT LISTAGG(cols.column_name, ',') WITHIN GROUP (ORDER BY cols.position)
				FROM user_cons_columns cols WHERE cols.constraint_name = cons.constraint_name),
			rcons.table_name,
			(SELECT LISTAGG(rcols.column_name, ',') WITHIN GROUP (ORDER BY rcols.position)
				FROM user_cons_columns rcols WHERE rcols.constraint_name = cons.r_constraint_name)
		FROM user_constraints cons
		INNER JOIN user_constraints rcons ON rcons.constraint_name = cons.r_constraint_name
		WHERE cons.constraint_type = 'R' AND cons.table_name = ?`, table)
	if err != nil {
		return nil, fmt.Errorf("reading the constraints of %s: %w", table, err)
	}
	for rows.Next() {
		var name, otherTable string
		var cols, otherColumns sql.NullString
		if err := rows.Scan(&name, &cols, &otherTable, &otherColumns); err != nil {
			rows.Close()
			return nil, err
		}
		out[name] = base.ConstraintInfo{
			Columns:    splitList(cols),
			ForeignKey: &base.ForeignKeyTarget{Table: otherTable, Columns: splitList(otherColumns)},
		}
	}
	rows.Close()
	// Indexes that do not back a constraint.
	exprs, err := i.indexExpressions(table)
	if err != nil {
		return nil, err
	}
	rows, err = i.Conn.Rows(`
		SELECT
			ind.index_name,
			LOWER(ind.index_type),
			LOWER(ind.uniqueness),
			LISTAGG(cols.column_name, ',') WITHIN GROUP (ORDER BY cols.column_position),
			LISTAGG(cols.descend, ',') WITHIN GROUP (ORDER BY cols.column_position)
		FROM user_ind_columns cols, user_indexes ind
		WHERE cols.table_name = ?
			AND NOT EXISTS (
				SELECT 1 FROM user_constraints cons WHERE ind.index_name = cons.index_name
			)
			AND cols.index_name = ind.index_name
		GROUP BY ind.index_name, ind.index_type, ind.uniqueness`, table)
	if err != nil {
		return nil, fmt.Errorf("reading the constraints of %s: %w", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var name, kind, uniqueness string
		var cols, orders sql.NullString
		if err := rows.Scan(&name, &kind, &uniqueness, &cols, &orders); err != nil {
			return nil, err
		}
		columns := splitList(cols)
		for n, c := range columns {
			// A descending or computed index element is stored as a hidden
			// virtual column; report the expression behind it instead.
			if hiddenColumn.MatchString(c) {
				if expr, ok := exprs[indexElement{name, n + 1}]; ok {
					columns[n] = expr
				}
			}
		}
		t := kind
		if kind == "normal" {
			t = "idx"
		}
		// user_ind_columns.descend is ASC or DESC; anything else would be
		// a spelling Oracle has never documented, and is reported rather
		// than folded into ascending.
		sorts, err := base.ParseSortOrders(splitList(orders))
		if err != nil {
			return nil, fmt.Errorf("reading the constraints of %s: %w", table, err)
		}
		out[name] = base.ConstraintInfo{
			Columns: columns,
			Unique:  uniqueness == "unique",
			Index:   true,
			Type:    t,
			Orders:  sorts,
		}
	}
	return out, rows.Err()
}

// indexElement identifies one element of an index.
type indexElement struct {
	Index    string
	Position int
}

// indexExpressions maps (index name, position) to the expression of a
// function-based index element.
func (i *Introspection) indexExpressions(table string) (map[indexElement]string, error) {
	rows, err := i.Conn.Rows(`
		SELECT index_name, column_position, column_expression
		FROM user_ind_expressions WHERE table_name = ?`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[indexElement]string{}
	for rows.Next() {
		var name string
		var pos int
		var expr sql.NullString
		if err := rows.Scan(&name, &pos, &expr); err != nil {
			return nil, err
		}
		e := expr.String
		// A plain descending column is stored as the quoted column name.
		if len(e) > 2 && strings.HasPrefix(e, `"`) && strings.HasSuffix(e, `"`) &&
			!strings.Contains(e[1:len(e)-1], `"`) {
			e = e[1 : len(e)-1]
		}
		out[indexElement{name, pos}] = e
	}
	return out, rows.Err()
}
