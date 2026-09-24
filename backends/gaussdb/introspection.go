package gaussdb

import (
	"github.com/ctolon/gormgate/backends/base"
	"github.com/ctolon/gormgate/backends/postgresql"
)

// Introspection reads openGauss's catalogs. openGauss forked PostgreSQL
// 9.2, so the queries that only use 9.2 catalog columns are inherited from
// the PostgreSQL backend; the ones that need newer columns
// (pg_class.relispartition, pg_attribute.attidentity) or newer SQL
// (multi-argument unnest ... WITH ORDINALITY) are replaced here.
//
// django: db/backends/postgresql/introspection.py
type Introspection struct {
	*postgresql.Introspection
}

// tableListSQL lists tables (and views). pg_class.relispartition does not
// exist before PostgreSQL 10, so partitions are reported as plain tables.
//
// django: postgresql/introspection.py DatabaseIntrospection.get_table_list
const tableListSQL = `
	SELECT
		c.relname,
		CASE
			WHEN c.relkind = 'm' THEN 'm'
			WHEN c.relkind = 'v' THEN 'v'
			WHEN c.relkind = 'f' THEN 'f'
			ELSE 't'
		END,
		coalesce(obj_description(c.oid, 'pg_class'), '')
	FROM pg_catalog.pg_class c
	LEFT JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
	WHERE c.relkind IN ('f', 'm', 'r', 'v')
		AND n.nspname NOT IN ('pg_catalog', 'pg_toast')
		AND pg_catalog.pg_table_is_visible(c.oid)
	ORDER BY c.relname`

// columnsSQL describes the columns of a table in attnum order.
// pg_attribute.attidentity is PostgreSQL 10; openGauss has no identity
// columns at all, so a nextval() default is the only auto-increment marker.
//
// django: postgresql/introspection.py DatabaseIntrospection.get_table_description
const columnsSQL = `
	SELECT
		a.attname,
		format_type(a.atttypid, a.atttypmod),
		NOT (a.attnotnull OR (t.typtype = 'd' AND t.typnotnull)),
		pg_get_expr(ad.adbin, ad.adrelid),
		CASE WHEN co.collname = 'default' THEN '' ELSE coalesce(co.collname, '') END,
		coalesce(pg_get_expr(ad.adbin, ad.adrelid), '') LIKE 'nextval(%',
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
	WHERE c.relkind IN ('f', 'm', 'r', 'v')
		AND c.relname = ?
		AND a.attnum > 0 AND NOT a.attisdropped
		AND n.nspname NOT IN ('pg_catalog', 'pg_toast')
		AND pg_catalog.pg_table_is_visible(c.oid)
	ORDER BY a.attnum`

// indexesSQL lists a table's indexes. It cannot use PostgreSQL's
// unnest(indkey, indoption) WITH ORDINALITY ("ERROR: function
// unnest(integer[], integer[]) does not exist": openGauss only has the
// single-argument form), so the two int vectors are walked with
// generate_series over pg_index.indnatts instead.
//
// django: postgresql/introspection.py DatabaseIntrospection.get_constraints
const indexesSQL = `
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
			c2.relname AS indexname,
			i.indisunique,
			i.indisprimary,
			s.arridx,
			attr.attname,
			am.amname,
			CASE
				WHEN i.indexprs IS NOT NULL THEN pg_get_indexdef(i.indexrelid)
			END AS exprdef,
			CASE am.amname
				WHEN 'btree' THEN
					CASE (i.indoption[s.arridx - 1] & 1)
						WHEN 1 THEN 'DESC' ELSE 'ASC'
					END
			END AS ordering
		FROM pg_index i
		JOIN pg_class c ON i.indrelid = c.oid
		JOIN pg_class c2 ON i.indexrelid = c2.oid
		LEFT JOIN pg_am am ON c2.relam = am.oid
		JOIN generate_series(1, i.indnatts) AS s(arridx) ON true
		LEFT JOIN pg_attribute attr
			ON attr.attrelid = c.oid AND attr.attnum = i.indkey[s.arridx - 1]
		WHERE c.relname = ? AND pg_catalog.pg_table_is_visible(c.oid)
	) s2
	GROUP BY indexname, indisunique, indisprimary, amname, exprdef`

// NewIntrospection binds an introspection to c.
func NewIntrospection(c *base.Conn) *Introspection {
	return &Introspection{Introspection: &postgresql.Introspection{
		Conn:             c,
		TableQuery:       tableListSQL,
		DescriptionQuery: columnsSQL,
		IndexQuery:       indexesSQL,
	}}
}
