package cockroachdb

import (
	"github.com/ctolon/gormgate/backends/base"
	"github.com/ctolon/gormgate/backends/postgresql"
)

// Introspection reads CockroachDB's emulated pg_catalog. Most of
// PostgreSQL's queries work unchanged; the ones that don't are replaced
// here.
//
// django: django_cockroachdb/introspection.py DatabaseIntrospection
type Introspection struct {
	*postgresql.Introspection
}

// tableListSQL lists tables (and views). Unlike PostgreSQL, CockroachDB
// puts its PostGIS compatibility relations (geometry_columns,
// geography_columns, spatial_ref_sys) in the pg_extension schema, which is
// visible by default and would otherwise show up as user tables.
//
// django: django_cockroachdb/introspection.py DatabaseIntrospection.get_table_list
const tableListSQL = `
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
		AND n.nspname NOT IN ('pg_catalog', 'pg_toast', 'pg_extension', 'crdb_internal', 'information_schema')
		AND pg_catalog.pg_table_is_visible(c.oid)
	ORDER BY c.relname`

// indexesSQL lists a table's indexes. CockroachDB's only index access
// method is "prefix" (there is no pg_am row for btree), so PostgreSQL's
// ordering expression, which is guarded by amname = 'btree', has to be
// applied to it instead.
//
// django: django_cockroachdb/introspection.py DatabaseIntrospection.index_default_access_method
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
			c2.relname as indexname, idx.*, attr.attname, am.amname,
			CASE
				WHEN idx.indexprs IS NOT NULL THEN
					pg_get_indexdef(idx.indexrelid)
			END AS exprdef,
			CASE am.amname
				WHEN 'prefix' THEN
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

// NewIntrospection binds an introspection to c.
func NewIntrospection(c *base.Conn) *Introspection {
	return &Introspection{Introspection: &postgresql.Introspection{
		Conn:       c,
		TableQuery: tableListSQL,
		IndexQuery: indexesSQL,
	}}
}

// TableDescription describes the columns of a table. A serial column is an
// INT8 with DEFAULT unique_rowid() (serial_normalization = rowid) instead
// of nextval() of an owned sequence, so that default is what marks it as
// auto-incrementing.
func (i *Introspection) TableDescription(table string) ([]base.ColumnInfo, error) {
	cols, err := i.Introspection.TableDescription(table)
	if err != nil {
		return nil, err
	}
	for k, c := range cols {
		if c.Default != nil && *c.Default == serialDefault {
			cols[k].AutoIncrement = true
		}
	}
	return cols, nil
}
