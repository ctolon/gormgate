package sqlite3

import (
	"strconv"
	"strings"

	m "github.com/ctolon/gormgate/migrations"
)

// The CREATE TABLE statement stored in sqlite_master is the only place
// where SQLite records constraint names, CHECK constraints and the names
// of foreign keys: no PRAGMA reports them. Django parses the same
// statement with sqlparse.
//
// django: sqlite3/introspection.py DatabaseIntrospection._parse_table_constraints

// token kinds.
const (
	tokWord   = 'w' // bare word (identifier or keyword)
	tokQuoted = 'q' // quoted identifier: "x", `x`, [x]
	tokString = 's' // string literal: 'x'
	tokPunct  = 'p' // single punctuation character
	tokOther  = 'o' // numbers, operators, anything else
)

type token struct {
	s    string
	kind byte
}

// isIdent reports whether the token can name a column.
func (t token) isIdent() bool { return t.kind == tokWord || t.kind == tokQuoted }

// upper returns the token in upper case (bare words only).
func (t token) upper() string {
	if t.kind != tokWord {
		return ""
	}
	return strings.ToUpper(t.s)
}

func isWordByte(b byte) bool {
	return b == '_' || b == '$' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= 0x80
}

// tokenize splits an SQL fragment into tokens, skipping whitespace and
// comments.
func tokenize(sql string) []token {
	var out []token
	for i := 0; i < len(sql); {
		c := sql[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f':
			i++
		case c == '-' && i+1 < len(sql) && sql[i+1] == '-':
			for i < len(sql) && sql[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(sql) && sql[i+1] == '*':
			j := strings.Index(sql[i+2:], "*/")
			if j < 0 {
				i = len(sql)
			} else {
				i += 2 + j + 2
			}
		case c == '"' || c == '`' || c == '\'':
			s, n := readQuoted(sql[i:], c, c)
			kind := byte(tokQuoted)
			if c == '\'' {
				kind = tokString
			}
			out = append(out, token{s: s, kind: kind})
			i += n
		case c == '[':
			s, n := readQuoted(sql[i:], '[', ']')
			out = append(out, token{s: s, kind: tokQuoted})
			i += n
		case isWordByte(c) && !(c >= '0' && c <= '9'):
			j := i
			for j < len(sql) && isWordByte(sql[j]) {
				j++
			}
			out = append(out, token{s: sql[i:j], kind: tokWord})
			i = j
		case c == '(' || c == ')' || c == ',':
			out = append(out, token{s: string(c), kind: tokPunct})
			i++
		default:
			j := i
			for j < len(sql) && !isWordByte(sql[j]) && sql[j] != '(' && sql[j] != ')' && sql[j] != ',' &&
				sql[j] != ' ' && sql[j] != '\t' && sql[j] != '\n' && sql[j] != '\r' &&
				sql[j] != '"' && sql[j] != '`' && sql[j] != '\'' && sql[j] != '[' {
				j++
			}
			if j == i {
				j++
			}
			out = append(out, token{s: sql[i:j], kind: tokOther})
			i = j
		}
	}
	return out
}

// readQuoted reads a quoted run starting at s[0] == open and returns the
// unquoted text and the number of bytes consumed. A doubled closing quote
// is an escaped quote.
func readQuoted(s string, open, close byte) (string, int) {
	var b strings.Builder
	i := 1
	for i < len(s) {
		if s[i] == close {
			if i+1 < len(s) && s[i+1] == close && open != '[' {
				b.WriteByte(close)
				i += 2
				continue
			}
			return b.String(), i + 1
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String(), len(s)
}

// columnDef is one column of a CREATE TABLE statement.
type columnDef struct {
	Name          string
	AutoIncrement bool
	Collation     string
}

// constraintDef is one constraint of a CREATE TABLE statement: a table
// constraint or a named column constraint.
type constraintDef struct {
	Name string
	// Kind is "UNIQUE", "CHECK", "PRIMARY KEY" or "FOREIGN KEY".
	Kind    string
	Columns []string
	ToTable string
	ToCols  []string
}

// tableDef is a parsed CREATE TABLE statement.
type tableDef struct {
	Columns     []columnDef
	Constraints []constraintDef
}

// splitDefinitions returns the top-level comma-separated definitions of
// the CREATE TABLE body.
func splitDefinitions(toks []token) [][]token {
	// Skip everything up to the opening parenthesis of the body.
	start := -1
	for i, t := range toks {
		if t.kind == tokPunct && t.s == "(" {
			start = i + 1
			break
		}
	}
	if start < 0 {
		return nil
	}
	var out [][]token
	depth := 0
	cur := []token{}
	for _, t := range toks[start:] {
		if t.kind == tokPunct {
			switch t.s {
			case "(":
				depth++
			case ")":
				if depth == 0 {
					if len(cur) > 0 {
						out = append(out, cur)
					}
					return out
				}
				depth--
			case ",":
				if depth == 0 {
					out = append(out, cur)
					cur = []token{}
					continue
				}
			}
		}
		cur = append(cur, t)
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}

// parenGroup returns the tokens of the parenthesised group starting at
// toks[i] (which must be "(") and the index just after its ")".
func parenGroup(toks []token, i int) ([]token, int) {
	if i >= len(toks) || toks[i].kind != tokPunct || toks[i].s != "(" {
		return nil, i
	}
	depth := 0
	for j := i; j < len(toks); j++ {
		if toks[j].kind == tokPunct {
			if toks[j].s == "(" {
				depth++
				if depth == 1 {
					continue
				}
			} else if toks[j].s == ")" {
				depth--
				if depth == 0 {
					return toks[i+1 : j], j + 1
				}
			}
		}
	}
	return toks[i+1:], len(toks)
}

// identList returns the identifiers of a column list, ignoring sort order
// keywords and collations.
func identList(toks []token) []string {
	var out []string
	depth := 0
	expect := true
	for _, t := range toks {
		if t.kind == tokPunct {
			switch t.s {
			case "(":
				depth++
			case ")":
				depth--
			case ",":
				if depth == 0 {
					expect = true
				}
			}
			continue
		}
		if depth == 0 && expect && t.isIdent() {
			out = append(out, t.s)
			expect = false
		}
	}
	return out
}

// parseTableDef parses a CREATE TABLE statement.
func parseTableDef(sql string) tableDef {
	var def tableDef
	unnamed := 0
	nextUnnamed := func() string {
		unnamed++
		return "__unnamed_constraint_" + strconv.Itoa(unnamed) + "__"
	}
	for _, d := range splitDefinitions(tokenize(sql)) {
		if len(d) == 0 {
			continue
		}
		name := ""
		i := 0
		if d[0].upper() == "CONSTRAINT" && len(d) > 1 {
			name = d[1].s
			i = 2
		}
		if i >= len(d) {
			continue
		}
		switch kind, next := tableConstraintKind(d, i); kind {
		case "PRIMARY KEY", "UNIQUE":
			cols, _ := parenGroup(d, next)
			c := constraintDef{Name: name, Kind: kind, Columns: identList(cols)}
			if c.Name == "" {
				c.Name = nextUnnamed()
			}
			def.Constraints = append(def.Constraints, c)
		case "CHECK":
			body, _ := parenGroup(d, next)
			c := constraintDef{Name: name, Kind: "CHECK", Columns: identsIn(body)}
			if c.Name == "" {
				c.Name = nextUnnamed()
			}
			def.Constraints = append(def.Constraints, c)
		case "FOREIGN KEY":
			cols, after := parenGroup(d, next)
			c := constraintDef{Name: name, Kind: "FOREIGN KEY", Columns: identList(cols)}
			c.ToTable, c.ToCols = parseReferences(d, after)
			if c.Name == "" {
				c.Name = nextUnnamed()
			}
			def.Constraints = append(def.Constraints, c)
		default:
			if name != "" || !d[i].isIdent() {
				// Not a column definition and not a constraint we know.
				continue
			}
			col, cons := parseColumnDef(d, i)
			def.Columns = append(def.Columns, col)
			for _, c := range cons {
				if c.Name == "" {
					c.Name = nextUnnamed()
				}
				def.Constraints = append(def.Constraints, c)
			}
		}
	}
	return def
}

// tableConstraintKind recognises the keyword introducing a table
// constraint and returns the index of the token after it.
func tableConstraintKind(d []token, i int) (string, int) {
	switch d[i].upper() {
	case "PRIMARY":
		if i+1 < len(d) && d[i+1].upper() == "KEY" && i+2 < len(d) && d[i+2].s == "(" {
			return "PRIMARY KEY", i + 2
		}
	case "UNIQUE":
		if i+1 < len(d) && d[i+1].s == "(" {
			return "UNIQUE", i + 1
		}
	case "CHECK":
		if i+1 < len(d) && d[i+1].s == "(" {
			return "CHECK", i + 1
		}
	case "FOREIGN":
		if i+1 < len(d) && d[i+1].upper() == "KEY" && i+2 < len(d) && d[i+2].s == "(" {
			return "FOREIGN KEY", i + 2
		}
	}
	return "", i
}

// parseReferences reads a "REFERENCES table (column, ...)" clause.
func parseReferences(d []token, i int) (string, []string) {
	for ; i < len(d); i++ {
		if d[i].upper() != "REFERENCES" {
			continue
		}
		if i+1 >= len(d) || !d[i+1].isIdent() {
			return "", nil
		}
		table := d[i+1].s
		if i+2 < len(d) && d[i+2].s == "(" {
			cols, _ := parenGroup(d, i+2)
			return table, identList(cols)
		}
		return table, nil
	}
	return "", nil
}

// parseColumnDef parses a column definition and the constraints attached
// to it.
func parseColumnDef(d []token, i int) (columnDef, []constraintDef) {
	col := columnDef{Name: d[i].s}
	var cons []constraintDef
	name := ""
	for j := i + 1; j < len(d); j++ {
		switch d[j].upper() {
		case "CONSTRAINT":
			if j+1 < len(d) {
				name = d[j+1].s
				j++
			}
			continue
		case "AUTOINCREMENT":
			col.AutoIncrement = true
		case "COLLATE":
			if j+1 < len(d) {
				col.Collation = d[j+1].s
				j++
			}
		case "UNIQUE":
			cons = append(cons, constraintDef{Name: name, Kind: "UNIQUE", Columns: []string{col.Name}})
		case "CHECK":
			if j+1 < len(d) && d[j+1].s == "(" {
				body, after := parenGroup(d, j+1)
				cons = append(cons, constraintDef{Name: name, Kind: "CHECK", Columns: identsIn(body)})
				j = after - 1
			}
		case "REFERENCES":
			table, cols := parseReferences(d, j)
			cons = append(cons, constraintDef{Name: name, Kind: "FOREIGN KEY", Columns: []string{col.Name}, ToTable: table, ToCols: cols})
			if j+2 < len(d) && d[j+2].s == "(" {
				_, after := parenGroup(d, j+2)
				j = after - 1
			} else {
				j++
			}
		default:
			continue
		}
		name = ""
	}
	return col, cons
}

// identsIn returns every identifier of an expression, in order (Django
// collects the tokens of a CHECK expression the same way and keeps only
// the ones naming a column; the caller filters).
func identsIn(toks []token) []string {
	var out []string
	for _, t := range toks {
		if t.isIdent() {
			out = append(out, t.s)
		}
	}
	return out
}

// filterColumns keeps the names that are columns of the table, in order.
func filterColumns(names []string, columns map[string]bool) []string {
	var out []string
	for _, n := range names {
		if columns[n] {
			out = append(out, n)
		}
	}
	return out
}

// indexColumnOrders returns the sort direction of every column of a CREATE
// INDEX statement. SQLite spells only DESC; anything else is ascending,
// which is what the grammar says rather than a guess about an unknown
// keyword.
//
// django: sqlite3/introspection.py DatabaseIntrospection._get_index_columns_orders
func indexColumnOrders(sql string) []m.SortOrder {
	toks := tokenize(sql)
	start := -1
	for i, t := range toks {
		if t.kind == tokPunct && t.s == "(" {
			start = i
			break
		}
	}
	if start < 0 {
		return nil
	}
	body, _ := parenGroup(toks, start)
	var out []m.SortOrder
	depth := 0
	last := ""
	flush := func() {
		if strings.EqualFold(last, string(m.SortDesc)) {
			out = append(out, m.SortDesc)
		} else {
			out = append(out, m.SortAsc)
		}
		last = ""
	}
	seen := false
	for _, t := range body {
		if t.kind == tokPunct {
			switch t.s {
			case "(":
				depth++
			case ")":
				depth--
			case ",":
				if depth == 0 {
					flush()
					seen = false
					continue
				}
			}
			continue
		}
		seen = true
		last = t.s
	}
	if seen || len(out) > 0 {
		flush()
	}
	return out
}
