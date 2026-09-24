package postgresql

import (
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/ctolon/gormgate/backends/base"
)

// Introspection reads PostgreSQL's catalogs.
//
// The forks that speak PostgreSQL's dialect embed it and replace only the
// queries their own catalogs spell differently, through the query fields
// below; everything else -- the scanning, the decoding and the shape of the
// results -- lives here once.
//
// django: db/backends/postgresql/introspection.py
type Introspection struct {
	Conn *base.Conn
	// TableQuery lists the tables; empty means TableListSQL.
	TableQuery string
	// DescriptionQuery describes a table's columns; empty means ColumnsSQL.
	DescriptionQuery string
	// IndexQuery lists a table's indexes; empty means IndexesSQL.
	IndexQuery string
}

// TableListSQL lists tables (and views) with their kind and comment.
//
// django: postgresql/introspection.py DatabaseIntrospection.get_table_list
const TableListSQL = `
	SELECT
		c.relname,
		CASE
			WHEN c.relispartition THEN 'p'
			WHEN c.relkind = 'm' THEN 'm'
			WHEN c.relkind = 'v' THEN 'v'
			WHEN c.relkind = 'f' THEN 'f'
			ELSE 't'
		END,
		coalesce(obj_description(c.oid, 'pg_class'), '')
	FROM pg_catalog.pg_class c
	LEFT JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
	WHERE c.relkind IN ('f', 'm', 'p', 'r', 'v')
		AND n.nspname NOT IN ('pg_catalog', 'pg_toast')
		AND pg_catalog.pg_table_is_visible(c.oid)
	ORDER BY c.relname`

// ColumnsSQL describes the columns of a table in attnum order.
//
// django: postgresql/introspection.py DatabaseIntrospection.get_table_description
const ColumnsSQL = `
	SELECT
		a.attname,
		format_type(a.atttypid, a.atttypmod),
		NOT (a.attnotnull OR (t.typtype = 'd' AND t.typnotnull)),
		pg_get_expr(ad.adbin, ad.adrelid),
		CASE WHEN co.collname = 'default' THEN '' ELSE coalesce(co.collname, '') END,
		a.attidentity != '' OR coalesce(pg_get_expr(ad.adbin, ad.adrelid), '') LIKE 'nextval(%',
		coalesce(col_description(a.attrelid, a.attnum), ''),
		CASE WHEN a.atttypmod > 4 AND t.typname IN ('varchar', 'bpchar') THEN a.atttypmod - 4 ELSE 0 END,
		CASE WHEN t.typname = 'numeric' AND a.atttypmod > 0 THEN ((a.atttypmod - 4) >> 16) & 65535 ELSE 0 END,
		CASE WHEN t.typname = 'numeric' AND a.atttypmod > 0 THEN (a.atttypmod - 4) & 65535 ELSE 0 END
	FROM pg_attribute a
	LEFT JOIN pg_attrdef ad ON a.attrelid = ad.adrelid AND a.attnum = ad.adnum
	LEFT JOIN pg_collation co ON a.attcollation = co.oid
	JOIN pg_type t ON a.atttypid = t.oid
	JOIN pg_class c ON a.attrelid = c.oid
	JOIN pg_namespace n ON c.relnamespace = n.oid
	WHERE c.relkind IN ('f', 'm', 'p', 'r', 'v')
		AND c.relname = ?
		AND a.attnum > 0 AND NOT a.attisdropped
		AND n.nspname NOT IN ('pg_catalog', 'pg_toast')
		AND pg_catalog.pg_table_is_visible(c.oid)
	ORDER BY a.attnum`

// ConstraintsSQL lists the entries of pg_constraint for a table. It only
// uses catalog columns every PostgreSQL fork has, so no backend replaces
// it.
//
// django: postgresql/introspection.py DatabaseIntrospection.get_constraints
const ConstraintsSQL = `
	SELECT
		c.conname,
		array_to_json(array(
			SELECT attname
			FROM unnest(c.conkey) WITH ORDINALITY cols(colid, arridx)
			JOIN pg_attribute AS ca ON cols.colid = ca.attnum
			WHERE ca.attrelid = c.conrelid
			ORDER BY cols.arridx
		))::text,
		c.contype,
		(SELECT fkc.relname FROM pg_class AS fkc WHERE fkc.oid = c.confrelid),
		array_to_json(array(
			SELECT attname
			FROM unnest(c.confkey) WITH ORDINALITY cols(colid, arridx)
			JOIN pg_attribute AS fka ON cols.colid = fka.attnum
			WHERE fka.attrelid = c.confrelid
			ORDER BY cols.arridx
		))::text,
		pg_get_constraintdef(c.oid)
	FROM pg_constraint AS c
	JOIN pg_class AS cl ON c.conrelid = cl.oid
	WHERE cl.relname = ? AND pg_catalog.pg_table_is_visible(cl.oid)`

// IndexesSQL lists a table's indexes with their columns and orderings.
//
// django: postgresql/introspection.py DatabaseIntrospection.get_constraints
const IndexesSQL = `
	SELECT
		indexname,
		array_to_json(array_agg(attname ORDER BY arridx))::text,
		indisunique,
		indisprimary,
		array_to_json(array_agg(ordering ORDER BY arridx))::text,
		amname,
		exprdef
	FROM (
		SELECT
			c2.relname as indexname, idx.*, attr.attname, am.amname,
			CASE
				WHEN idx.indexprs IS NOT NULL THEN
					pg_get_indexdef(idx.indexrelid)
			END AS exprdef,
			CASE am.amname
				WHEN 'btree' THEN
					CASE (option & 1)
						WHEN 1 THEN 'DESC' ELSE 'ASC'
					END
			END as ordering
		FROM (
			SELECT *
			FROM
				pg_index i,
				unnest(i.indkey, i.indoption)
					WITH ORDINALITY koi(key, option, arridx)
		) idx
		LEFT JOIN pg_class c ON idx.indrelid = c.oid
		LEFT JOIN pg_class c2 ON idx.indexrelid = c2.oid
		LEFT JOIN pg_am am ON c2.relam = am.oid
		LEFT JOIN
			pg_attribute attr ON attr.attrelid = c.oid AND attr.attnum = idx.key
		WHERE c.relname = ? AND pg_catalog.pg_table_is_visible(c.oid)
	) s2
	GROUP BY indexname, indisunique, indisprimary, amname, exprdef`

// or picks the fork's own query when it has one.
func or(vendor, standard string) string {
	if vendor != "" {
		return vendor
	}
	return standard
}

// IdentifierConverter returns name unchanged.
func (i *Introspection) IdentifierConverter(name string) string { return name }

// TableNames lists tables (and views).
//
// django: postgresql/introspection.py DatabaseIntrospection.get_table_list
func (i *Introspection) TableNames(includeViews bool) ([]base.TableInfo, error) {
	rows, err := i.Conn.Rows(or(i.TableQuery, TableListSQL))
	if err != nil {
		return nil, fmt.Errorf("listing tables: %w", err)
	}
	defer rows.Close()
	var out []base.TableInfo
	for rows.Next() {
		var t base.TableInfo
		if err := rows.Scan(&t.Name, &t.Type, &t.Comment); err != nil {
			return nil, fmt.Errorf("listing tables: %w", err)
		}
		if !includeViews && (t.Type == "v" || t.Type == "m") {
			continue
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing tables: %w", err)
	}
	return out, nil
}

// TableDescription describes the columns of a table in attnum order.
//
// django: postgresql/introspection.py DatabaseIntrospection.get_table_description
func (i *Introspection) TableDescription(table string) ([]base.ColumnInfo, error) {
	rows, err := i.Conn.Rows(or(i.DescriptionQuery, ColumnsSQL), table)
	if err != nil {
		return nil, fmt.Errorf("describing the columns of %s: %w", table, err)
	}
	defer rows.Close()
	var out []base.ColumnInfo
	for rows.Next() {
		var c base.ColumnInfo
		var def sql.NullString
		if err := rows.Scan(&c.Name, &c.Type, &c.Null, &def, &c.Collation, &c.AutoIncrement, &c.Comment, &c.Size, &c.Precision, &c.Scale); err != nil {
			return nil, fmt.Errorf("describing the columns of %s: %w", table, err)
		}
		if def.Valid {
			d := def.String
			c.Default = &d
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("describing the columns of %s: %w", table, err)
	}
	return out, nil
}

// Sequences lists sequences owned by the table's columns.
//
// django: postgresql/introspection.py DatabaseIntrospection.get_sequences
func (i *Introspection) Sequences(table string) ([]base.SequenceInfo, error) {
	rows, err := i.Conn.Rows(`
		SELECT s.relname, a.attname
		FROM pg_class s
			JOIN pg_depend d ON d.objid = s.oid
				AND d.classid = 'pg_class'::regclass
				AND d.refclassid = 'pg_class'::regclass
			JOIN pg_attribute a ON d.refobjid = a.attrelid
				AND d.refobjsubid = a.attnum
			JOIN pg_class tbl ON tbl.oid = d.refobjid
				AND tbl.relname = ?
				AND pg_catalog.pg_table_is_visible(tbl.oid)
		WHERE s.relkind = 'S'
		ORDER BY s.relname`, table)
	if err != nil {
		return nil, fmt.Errorf("listing the sequences of %s: %w", table, err)
	}
	defer rows.Close()
	var out []base.SequenceInfo
	for rows.Next() {
		s := base.SequenceInfo{Table: table}
		if err := rows.Scan(&s.Name, &s.Column); err != nil {
			return nil, fmt.Errorf("listing the sequences of %s: %w", table, err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing the sequences of %s: %w", table, err)
	}
	return out, nil
}

// Relations maps foreign-key columns to their targets.
//
// django: postgresql/introspection.py DatabaseIntrospection.get_relations
func (i *Introspection) Relations(table string) (map[string]base.RelationInfo, error) {
	rows, err := i.Conn.Rows(`
		SELECT a1.attname, c2.relname, a2.attname
		FROM pg_constraint con
		LEFT JOIN pg_class c1 ON con.conrelid = c1.oid
		LEFT JOIN pg_class c2 ON con.confrelid = c2.oid
		LEFT JOIN pg_attribute a1 ON c1.oid = a1.attrelid AND a1.attnum = con.conkey[1]
		LEFT JOIN pg_attribute a2 ON c2.oid = a2.attrelid AND a2.attnum = con.confkey[1]
		WHERE c1.relname = ?
			AND con.contype = 'f'
			AND c1.relnamespace = c2.relnamespace
			AND pg_catalog.pg_table_is_visible(c1.oid)`, table)
	if err != nil {
		return nil, fmt.Errorf("listing the foreign keys of %s: %w", table, err)
	}
	defer rows.Close()
	out := map[string]base.RelationInfo{}
	for rows.Next() {
		var r base.RelationInfo
		if err := rows.Scan(&r.Column, &r.ToTable, &r.ToColumn); err != nil {
			return nil, fmt.Errorf("listing the foreign keys of %s: %w", table, err)
		}
		out[r.Column] = r
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing the foreign keys of %s: %w", table, err)
	}
	return out, nil
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
		SELECT obj_description(c.oid, 'pg_class')
		FROM pg_catalog.pg_class c
		WHERE c.relname = ? AND pg_catalog.pg_table_is_visible(c.oid)`, table).Row().Scan(&c)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("reading the comment of %s: %w", table, err)
	}
	return c.String, nil
}

// decodeStrings decodes a json array of (possibly null) strings. A value
// that is not one is an error: reporting a constraint with no columns at
// all would make the caller drop the wrong object, or none.
func decodeStrings(s sql.NullString) ([]string, error) {
	if !s.Valid || s.String == "" {
		return nil, nil
	}
	var raw []*string
	if err := json.Unmarshal([]byte(s.String), &raw); err != nil {
		return nil, fmt.Errorf("decoding the column list %q: %w", s.String, err)
	}
	out := make([]string, 0, len(raw))
	for _, r := range raw {
		if r == nil {
			continue
		}
		out = append(out, *r)
	}
	return out, nil
}

// Constraints returns constraints and indexes of a table.
//
// django: postgresql/introspection.py DatabaseIntrospection.get_constraints
func (i *Introspection) Constraints(table string) (map[string]base.ConstraintInfo, error) {
	out := map[string]base.ConstraintInfo{}
	if err := i.scanConstraints(table, out); err != nil {
		return nil, fmt.Errorf("reading the constraints of %s: %w", table, err)
	}
	if err := i.scanIndexes(table, out); err != nil {
		return nil, fmt.Errorf("reading the indexes of %s: %w", table, err)
	}
	return out, nil
}

// scanConstraints adds the pg_constraint entries of a table to out.
func (i *Introspection) scanConstraints(table string, out map[string]base.ConstraintInfo) error {
	rows, err := i.Conn.Rows(ConstraintsSQL, table)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var name, kind string
		var cols, refTable, refCols, def sql.NullString
		if err := rows.Scan(&name, &cols, &kind, &refTable, &refCols, &def); err != nil {
			return err
		}
		columns, err := decodeStrings(cols)
		if err != nil {
			return err
		}
		info := base.ConstraintInfo{
			Columns:    columns,
			PrimaryKey: kind == "p",
			Unique:     kind == "p" || kind == "u",
			Check:      kind == "c",
			Definition: def.String,
		}
		if kind == "f" && refTable.Valid {
			refColumns, err := decodeStrings(refCols)
			if err != nil {
				return err
			}
			info.ForeignKey = &base.ForeignKeyTarget{Table: refTable.String, Columns: refColumns}
		}
		out[name] = info
	}
	return rows.Err()
}

// scanIndexes adds the indexes of a table to out, leaving the entries that
// scanConstraints already described alone.
func (i *Introspection) scanIndexes(table string, out map[string]base.ConstraintInfo) error {
	rows, err := i.Conn.Rows(or(i.IndexQuery, IndexesSQL), table)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var cols, orders, am, def sql.NullString
		var unique, primary bool
		if err := rows.Scan(&name, &cols, &unique, &primary, &orders, &am, &def); err != nil {
			return err
		}
		if _, ok := out[name]; ok {
			continue
		}
		columns, err := decodeStrings(cols)
		if err != nil {
			return err
		}
		ordering, err := decodeStrings(orders)
		if err != nil {
			return err
		}
		// The query reports ASC or DESC for a b-tree index and nothing at
		// all for any other access method; a third spelling would mean the
		// query no longer matches the catalog, so it is an error rather
		// than an index silently reported as ascending.
		sorts, err := base.ParseSortOrders(ordering)
		if err != nil {
			return err
		}
		out[name] = base.ConstraintInfo{
			Columns:    columns,
			Orders:     sorts,
			PrimaryKey: primary,
			Unique:     unique,
			Index:      true,
			Type:       am.String,
			Definition: def.String,
		}
	}
	return rows.Err()
}
