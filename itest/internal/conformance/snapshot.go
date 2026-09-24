// Package conformance is the vendor-agnostic operation conformance suite:
// every case applies a migration forwards and backwards on a real database
// and compares the resulting schema with a schema built from scratch.
package conformance

import (
	"fmt"
	"sort"
	"strings"

	"github.com/ctolon/gormgate/backends/base"
	m "github.com/ctolon/gormgate/migrations"
)

// joinOrders renders the sort directions of an index as one comparable
// string, the way the columns are rendered.
func joinOrders(os []m.SortOrder) string {
	ss := make([]string, len(os))
	for i, o := range os {
		ss[i] = string(o)
	}
	return strings.Join(ss, ",")
}

// Column is the comparable part of an introspected column.
type Column struct {
	Type    string
	Null    bool
	Default string
	Comment string
	AutoInc bool
}

// Constraint is the comparable part of an introspected constraint.
type Constraint struct {
	Columns    string
	PrimaryKey bool
	Unique     bool
	ForeignKey string
	Check      bool
	Index      bool
	Orders     string
}

// Table is one table's schema.
type Table struct {
	Columns     map[string]Column
	Constraints map[string]Constraint
	Comment     string
}

// Snapshot is the schema of a set of tables.
type Snapshot map[string]Table

// Normalizer adjusts introspected values that legitimately differ between
// a migrated and a freshly created schema: names the database generates
// itself (primary key constraints, serial sequences) keep the original
// table or column name after a rename, exactly as in Django.
type Normalizer struct {
	// Constraint may rename (or drop, keep=false) a constraint entry.
	Constraint func(table, name string, c Constraint) (newName string, keep bool)
	// Column may rewrite a column.
	Column func(table, name string, c Column) Column
	// Table post-processes a whole table, for normalizations that need
	// to see several constraints at once.
	Table func(table string, t Table) Table
}

// Take introspects the given tables (missing tables are skipped).
func Take(intro base.Introspection, tables []string, norm *Normalizer) (Snapshot, error) {
	existing, err := intro.TableNames(false)
	if err != nil {
		return nil, err
	}
	have := map[string]bool{}
	for _, t := range existing {
		have[t.Name] = true
	}
	snap := Snapshot{}
	for _, t := range tables {
		name := intro.IdentifierConverter(t)
		if !have[name] {
			continue
		}
		cols, err := intro.TableDescription(name)
		if err != nil {
			return nil, fmt.Errorf("describe %s: %w", t, err)
		}
		tb := Table{Columns: map[string]Column{}, Constraints: map[string]Constraint{}}
		for _, c := range cols {
			col := Column{Type: c.Type, Null: c.Null, Comment: c.Comment, AutoInc: c.AutoIncrement}
			if c.Default != nil {
				col.Default = *c.Default
			}
			if norm != nil && norm.Column != nil {
				col = norm.Column(t, c.Name, col)
			}
			tb.Columns[c.Name] = col
		}
		cons, err := intro.Constraints(name)
		if err != nil {
			return nil, fmt.Errorf("constraints %s: %w", t, err)
		}
		for n, c := range cons {
			cc := Constraint{
				Columns:    strings.Join(c.Columns, ","),
				PrimaryKey: c.PrimaryKey,
				Unique:     c.Unique,
				Check:      c.Check,
				Index:      c.Index,
				Orders:     joinOrders(c.Orders),
			}
			if c.ForeignKey != nil {
				cc.ForeignKey = c.ForeignKey.Table + "." + strings.Join(c.ForeignKey.Columns, ",")
			}
			if norm != nil && norm.Constraint != nil {
				nn, keep := norm.Constraint(t, n, cc)
				if !keep {
					continue
				}
				n = nn
			}
			tb.Constraints[n] = cc
		}
		if tb.Comment, err = intro.TableComment(name); err != nil {
			return nil, err
		}
		if norm != nil && norm.Table != nil {
			tb = norm.Table(t, tb)
		}
		snap[t] = tb
	}
	return snap, nil
}

// Diff describes differences between two snapshots ("" when equal).
func Diff(want, got Snapshot) string {
	var out []string
	tables := map[string]bool{}
	for t := range want {
		tables[t] = true
	}
	for t := range got {
		tables[t] = true
	}
	var names []string
	for t := range tables {
		names = append(names, t)
	}
	sort.Strings(names)
	for _, t := range names {
		w, wok := want[t]
		g, gok := got[t]
		switch {
		case !wok:
			out = append(out, fmt.Sprintf("table %s: unexpected", t))
			continue
		case !gok:
			out = append(out, fmt.Sprintf("table %s: missing", t))
			continue
		}
		if w.Comment != g.Comment {
			out = append(out, fmt.Sprintf("table %s: comment %q != %q", t, g.Comment, w.Comment))
		}
		out = append(out, diffMap(t, "column", w.Columns, g.Columns)...)
		out = append(out, diffMap(t, "constraint", w.Constraints, g.Constraints)...)
	}
	return strings.Join(out, "\n")
}

func diffMap[V comparable](table, kind string, want, got map[string]V) []string {
	var out []string
	keys := map[string]bool{}
	for k := range want {
		keys[k] = true
	}
	for k := range got {
		keys[k] = true
	}
	var names []string
	for k := range keys {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		w, wok := want[k]
		g, gok := got[k]
		switch {
		case !wok:
			out = append(out, fmt.Sprintf("table %s: unexpected %s %s %+v", table, kind, k, g))
		case !gok:
			out = append(out, fmt.Sprintf("table %s: missing %s %s %+v", table, kind, k, w))
		case w != g:
			out = append(out, fmt.Sprintf("table %s: %s %s\n    got  %+v\n    want %+v", table, kind, k, g, w))
		}
	}
	return out
}
