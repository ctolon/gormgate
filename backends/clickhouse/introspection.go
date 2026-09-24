package clickhouse

import (
	"fmt"
	"strings"

	"github.com/ctolon/gormgate/backends/base"
)

// Introspection reads ClickHouse's system tables.
//
// The schema of the current database is described by system.tables,
// system.columns and system.data_skipping_indices. CHECK constraints have
// no system table of their own, so they are read from the DDL of the table
// (system.tables.create_table_query), which the server normalizes.
type Introspection struct {
	Conn *base.Conn
}

// IdentifierConverter returns name unchanged: ClickHouse stores identifiers
// as they are written and compares them case-sensitively.
func (i *Introspection) IdentifierConverter(name string) string { return name }

// TableNames lists the tables (and views) of the current database.
func (i *Introspection) TableNames(includeViews bool) ([]base.TableInfo, error) {
	rows, err := i.Conn.Rows(`
		SELECT name, engine, comment
		FROM system.tables
		WHERE database = currentDatabase() AND NOT is_temporary
		ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("listing tables: %w", err)
	}
	defer rows.Close()
	var out []base.TableInfo
	for rows.Next() {
		var t base.TableInfo
		var engine string
		if err := rows.Scan(&t.Name, &engine, &t.Comment); err != nil {
			return nil, err
		}
		switch {
		case engine == "MaterializedView":
			t.Type = "m"
		case strings.HasSuffix(engine, "View"):
			t.Type = "v"
		default:
			t.Type = "t"
		}
		if !includeViews && (t.Type == "v" || t.Type == "m") {
			continue
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// TableDescription describes the columns of a table in storage order.
func (i *Introspection) TableDescription(table string) ([]base.ColumnInfo, error) {
	rows, err := i.Conn.Rows(`
		SELECT
			name,
			type,
			default_kind,
			default_expression,
			comment,
			toInt32(ifNull(character_octet_length, 0)),
			toInt32(ifNull(numeric_precision, ifNull(datetime_precision, 0))),
			toInt32(ifNull(numeric_scale, 0))
		FROM system.columns
		WHERE database = currentDatabase() AND table = ?
		ORDER BY position`, table)
	if err != nil {
		return nil, fmt.Errorf("describing the columns of %s: %w", table, err)
	}
	defer rows.Close()
	var out []base.ColumnInfo
	for rows.Next() {
		var c base.ColumnInfo
		var kind, expr string
		if err := rows.Scan(&c.Name, &c.Type, &kind, &expr, &c.Comment, &c.Size, &c.Precision, &c.Scale); err != nil {
			return nil, err
		}
		// Nullability is part of the type, and there are no sequences.
		c.Null = strings.HasPrefix(c.Type, "Nullable(")
		if kind == "DEFAULT" {
			d := expr
			c.Default = &d
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Sequences returns nothing: ClickHouse has no sequences.
func (i *Introspection) Sequences(string) ([]base.SequenceInfo, error) { return nil, nil }

// Relations returns nothing: ClickHouse has no foreign keys.
func (i *Introspection) Relations(string) (map[string]base.RelationInfo, error) {
	return map[string]base.RelationInfo{}, nil
}

// PrimaryKeyColumns returns the columns of the table's primary key (the
// prefix of its sorting key), in key order.
func (i *Introspection) PrimaryKeyColumns(table string) ([]string, error) {
	rows, err := i.Conn.Rows(`
		SELECT primary_key FROM system.tables
		WHERE database = currentDatabase() AND name = ?`, table)
	if err != nil {
		return nil, fmt.Errorf("reading the primary key of %s: %w", table, err)
	}
	defer rows.Close()
	var key string
	if rows.Next() {
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []string
	for _, part := range splitTopLevel(key) {
		if name := unquoteIdentifier(part); name != "" {
			out = append(out, name)
		}
	}
	return out, nil
}

// TableComment returns the comment of the table.
func (i *Introspection) TableComment(table string) (string, error) {
	rows, err := i.Conn.Rows(`
		SELECT comment FROM system.tables
		WHERE database = currentDatabase() AND name = ?`, table)
	if err != nil {
		return "", fmt.Errorf("reading the comment of %s: %w", table, err)
	}
	defer rows.Close()
	var comment string
	if rows.Next() {
		if err := rows.Scan(&comment); err != nil {
			return "", err
		}
	}
	return comment, rows.Err()
}

// Constraints returns the primary key, the data-skipping indexes and the
// CHECK constraints of a table.
func (i *Introspection) Constraints(table string) (map[string]base.ConstraintInfo, error) {
	out := map[string]base.ConstraintInfo{}
	pk, err := i.PrimaryKeyColumns(table)
	if err != nil {
		return nil, err
	}
	if len(pk) > 0 {
		// The primary key of a MergeTree table is a sparse index over the
		// sorting key; it is not unique.
		out["PRIMARY"] = base.ConstraintInfo{Columns: pk, PrimaryKey: true, Index: true}
	}
	rows, err := i.Conn.Rows(`
		SELECT name, type_full, expr, granularity
		FROM system.data_skipping_indices
		WHERE database = currentDatabase() AND table = ?
		ORDER BY name`, table)
	if err != nil {
		return nil, fmt.Errorf("reading the constraints of %s: %w", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var name, typ, expr string
		var gran uint64
		if err := rows.Scan(&name, &typ, &expr, &gran); err != nil {
			return nil, err
		}
		var cols []string
		for _, part := range splitTopLevel(expr) {
			cols = append(cols, unquoteIdentifier(part))
		}
		out[name] = base.ConstraintInfo{Columns: cols, Index: true, Type: typ, Definition: expr}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	columns, err := i.TableDescription(table)
	if err != nil {
		return nil, err
	}
	checks, err := i.checkConstraints(table, columns)
	if err != nil {
		return nil, err
	}
	for name, c := range checks {
		out[name] = c
	}
	return out, nil
}

// checkConstraints reads the CHECK constraints from the table's DDL.
func (i *Introspection) checkConstraints(table string, columns []base.ColumnInfo) (map[string]base.ConstraintInfo, error) {
	rows, err := i.Conn.Rows(`
		SELECT create_table_query FROM system.tables
		WHERE database = currentDatabase() AND name = ?`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ddl string
	if rows.Next() {
		if err := rows.Scan(&ddl); err != nil {
			return nil, err
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := map[string]base.ConstraintInfo{}
	for _, element := range splitTopLevel(tableElements(ddl)) {
		rest, ok := cutKeyword(element, "CONSTRAINT")
		if !ok {
			continue
		}
		name, expr := cutIdentifier(rest)
		if expr, ok = cutKeyword(expr, "CHECK"); !ok {
			continue
		}
		var cols []string
		words := map[string]bool{}
		for _, w := range wordSep.Split(expr, -1) {
			words[w] = true
		}
		for _, c := range columns {
			if words[c.Name] {
				cols = append(cols, c.Name)
			}
		}
		out[name] = base.ConstraintInfo{Columns: cols, Check: true, Definition: strings.TrimSpace(expr)}
	}
	return out, nil
}

// tableElements returns the content of the outermost parentheses of a
// CREATE TABLE statement (its columns, indexes and constraints).
func tableElements(ddl string) string {
	start := strings.Index(ddl, "(")
	if start < 0 {
		return ""
	}
	depth := 0
	for i := start; i < len(ddl); i++ {
		switch ddl[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return ddl[start+1 : i]
			}
		case '\'', '`':
			if n := skipQuoted(ddl[i:], ddl[i]); n > 0 {
				i += n - 1
			}
		}
	}
	return ""
}

// skipQuoted returns the length of the quoted literal at the start of s
// (which begins with quote), or 0 when it is not closed.
func skipQuoted(s string, quote byte) int {
	for i := 1; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case quote:
			if i+1 < len(s) && s[i+1] == quote {
				i++
				continue
			}
			return i + 1
		}
	}
	return 0
}

// splitTopLevel splits a comma-separated list, ignoring commas inside
// parentheses, quoted strings and quoted identifiers.
func splitTopLevel(s string) []string {
	var out []string
	depth, start := 0, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(', '[':
			depth++
		case ')', ']':
			depth--
		case '\'', '`':
			if n := skipQuoted(s[i:], s[i]); n > 0 {
				i += n - 1
			}
		case ',':
			if depth == 0 {
				out = append(out, strings.TrimSpace(s[start:i]))
				start = i + 1
			}
		}
	}
	if last := strings.TrimSpace(s[start:]); last != "" {
		out = append(out, last)
	}
	return out
}

// unquoteIdentifier returns the name of a plain (optionally backquoted)
// identifier, or "" when s is an expression.
func unquoteIdentifier(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "`") && strings.HasSuffix(s, "`") && len(s) >= 2 {
		return strings.ReplaceAll(s[1:len(s)-1], "``", "`")
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || (i > 0 && c >= '0' && c <= '9')) {
			return ""
		}
	}
	return s
}

// cutKeyword removes a leading keyword and the space after it.
func cutKeyword(s, keyword string) (string, bool) {
	s = strings.TrimSpace(s)
	if len(s) <= len(keyword) || !strings.EqualFold(s[:len(keyword)], keyword) {
		return s, false
	}
	if c := s[len(keyword)]; c != ' ' && c != '\t' && c != '\n' && c != '(' {
		return s, false
	}
	return strings.TrimSpace(s[len(keyword):]), true
}

// cutIdentifier splits a leading identifier from the rest.
func cutIdentifier(s string) (string, string) {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "`") {
		if n := skipQuoted(s, '`'); n > 0 {
			return unquoteIdentifier(s[:n]), s[n:]
		}
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			return s[:i], s[i:]
		}
	}
	return s, ""
}

var _ base.Introspection = (*Introspection)(nil)
