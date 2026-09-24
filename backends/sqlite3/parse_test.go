package sqlite3

import (
	"slices"
	"testing"

	m "github.com/ctolon/gormgate/migrations"
)

// The CREATE TABLE text stored in sqlite_master is the only place SQLite
// records constraint names, CHECK constraints and foreign key names, so a
// parser that loses one of them makes the schema editor drop the wrong
// object, or none.

func TestTokenize(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		want []token
	}{
		{"words and punctuation", "a (b, c)", []token{
			{"a", tokWord}, {"(", tokPunct}, {"b", tokWord}, {",", tokPunct}, {"c", tokWord}, {")", tokPunct},
		}},
		{"double quoted", `"a b"`, []token{{"a b", tokQuoted}}},
		{"backtick quoted", "`a b`", []token{{"a b", tokQuoted}}},
		{"bracket quoted", "[a b]", []token{{"a b", tokQuoted}}},
		// A doubled quote inside a quoted identifier is one quote; the
		// bracket form has no such escape.
		{"doubled quote", `"a""b"`, []token{{`a"b`, tokQuoted}}},
		{"string literal", "'a''b'", []token{{"a'b", tokString}}},
		{"line comment", "a -- b\nc", []token{{"a", tokWord}, {"c", tokWord}}},
		{"block comment", "a /* b, c */ d", []token{{"a", tokWord}, {"d", tokWord}}},
		{"unterminated block comment", "a /* b", []token{{"a", tokWord}}},
		{"operator", "a>=b", []token{{"a", tokWord}, {">=", tokOther}, {"b", tokWord}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := tokenize(c.sql)
			if !slices.Equal(got, c.want) {
				t.Errorf("tokenize(%q) = %v, want %v", c.sql, got, c.want)
			}
		})
	}
}

func TestTokenizeNumbersAreNotIdentifiers(t *testing.T) {
	// identsIn keeps the identifiers of a CHECK expression, so a numeric
	// literal must not look like one.
	for _, tok := range tokenize("a > 12 AND b < 1.5") {
		if tok.isIdent() && (tok.s == "12" || tok.s == "1" || tok.s == "5") {
			t.Errorf("tokenize reported %q as an identifier", tok.s)
		}
	}
}

func TestFilterColumns(t *testing.T) {
	// The CHECK parser reports every identifier of the expression; only
	// the ones that are columns of the table belong in the constraint.
	columns := map[string]bool{"a": true, "b": true}
	got := filterColumns([]string{"a", "IN", "b", "a"}, columns)
	if want := []string{"a", "b", "a"}; !slices.Equal(got, want) {
		t.Errorf("filterColumns = %q, want %q", got, want)
	}
	if got := filterColumns(nil, columns); got != nil {
		t.Errorf("filterColumns(nil) = %q, want nil", got)
	}
}

func TestParseTableDefColumns(t *testing.T) {
	def := parseTableDef(`CREATE TABLE "t" (
		"id" integer NOT NULL PRIMARY KEY AUTOINCREMENT,
		"name" varchar(50) COLLATE NOCASE,
		"qty" integer DEFAULT 0
	)`)
	var names []string
	for _, c := range def.Columns {
		names = append(names, c.Name)
	}
	if want := []string{"id", "name", "qty"}; !slices.Equal(names, want) {
		t.Fatalf("columns = %q, want %q", names, want)
	}
	if !def.Columns[0].AutoIncrement {
		t.Error("id.AutoIncrement = false, want true")
	}
	if def.Columns[1].AutoIncrement {
		t.Error("name.AutoIncrement = true, want false")
	}
	if got, want := def.Columns[1].Collation, "NOCASE"; got != want {
		t.Errorf("name.Collation = %q, want %q", got, want)
	}
	if got := def.Columns[2].Collation; got != "" {
		t.Errorf("qty.Collation = %q, want %q", got, "")
	}
}

// constraintNames returns the constraints of a parsed table as
// "name:kind(columns)", which is what the introspection reads off it.
func constraintNames(def tableDef) []string {
	var out []string
	for _, c := range def.Constraints {
		s := c.Name + ":" + c.Kind + "("
		for i, col := range c.Columns {
			if i > 0 {
				s += ","
			}
			s += col
		}
		s += ")"
		out = append(out, s)
	}
	return out
}

func TestParseTableDefConstraints(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		want []string
	}{
		{"named table constraints", `CREATE TABLE t (
			a integer, b integer,
			CONSTRAINT u UNIQUE (a, b),
			CONSTRAINT c CHECK (a > 0),
			CONSTRAINT pk PRIMARY KEY (a))`,
			[]string{"u:UNIQUE(a,b)", "c:CHECK(a)", "pk:PRIMARY KEY(a)"}},
		// An unnamed constraint still has to be reported, under a
		// placeholder name, or it could never be dropped.
		{"unnamed table constraints", `CREATE TABLE t (a integer, UNIQUE (a), CHECK (a > 0))`,
			[]string{"__unnamed_constraint_1__:UNIQUE(a)", "__unnamed_constraint_2__:CHECK(a)"}},
		{"column constraints", `CREATE TABLE t (a integer CONSTRAINT u UNIQUE CONSTRAINT c CHECK (a > 0))`,
			[]string{"u:UNIQUE(a)", "c:CHECK(a)"}},
		{"unnamed column unique", `CREATE TABLE t (a integer UNIQUE)`,
			[]string{"__unnamed_constraint_1__:UNIQUE(a)"}},
		// The comma inside the CHECK expression is not a definition
		// separator. Every identifier of the expression is reported,
		// keywords included; the caller filters them against the columns
		// the table really has.
		{"comma inside a check", `CREATE TABLE t (a integer, CONSTRAINT c CHECK (a IN (1, 2)))`,
			[]string{"c:CHECK(a,IN)"}},
		{"quoted names", `CREATE TABLE t ("a b" integer, CONSTRAINT "u v" UNIQUE ("a b"))`,
			[]string{"u v:UNIQUE(a b)"}},
		// Sort order keywords are not columns.
		{"sorted unique columns", `CREATE TABLE t (a integer, b integer, CONSTRAINT u UNIQUE (a DESC, b ASC))`,
			[]string{"u:UNIQUE(a,b)"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := constraintNames(parseTableDef(c.sql))
			if !slices.Equal(got, c.want) {
				t.Errorf("parseTableDef(%q) constraints = %q, want %q", c.sql, got, c.want)
			}
		})
	}
}

func TestParseTableDefForeignKeys(t *testing.T) {
	cases := []struct {
		name                 string
		sql                  string
		wantName, wantTo     string
		wantCols, wantToCols []string
	}{
		{
			name: "table constraint",
			sql: `CREATE TABLE t (a integer, CONSTRAINT fk FOREIGN KEY (a) REFERENCES other ("id")
				ON DELETE CASCADE)`,
			wantName: "fk", wantTo: "other",
			wantCols: []string{"a"}, wantToCols: []string{"id"},
		},
		{
			// gorm's composite foreign key, as AutoMigrate writes it into
			// sqlite_master: the name and both column lists have to come
			// back out, or introspection reports a key over one column
			// under a made-up name.
			name: "composite table constraint",
			sql: "CREATE TABLE `cfk_children` (`id` integer PRIMARY KEY AUTOINCREMENT," +
				"`parent_tenant_id` integer,`parent_code` text," +
				"CONSTRAINT `fk_cfk_children_parent` FOREIGN KEY (`parent_tenant_id`,`parent_code`) " +
				"REFERENCES `cfk_parents`(`tenant_id`,`code`))",
			wantName: "fk_cfk_children_parent", wantTo: "cfk_parents",
			wantCols:   []string{"parent_tenant_id", "parent_code"},
			wantToCols: []string{"tenant_id", "code"},
		},
		{
			name:     "column constraint",
			sql:      `CREATE TABLE t (a integer CONSTRAINT fk REFERENCES other (id))`,
			wantName: "fk", wantTo: "other",
			wantCols: []string{"a"}, wantToCols: []string{"id"},
		},
		{
			// Without an explicit column list the reference is to the
			// other table's primary key, which the parser leaves to the
			// caller to resolve.
			name:     "no target column",
			sql:      `CREATE TABLE t (a integer CONSTRAINT fk REFERENCES other)`,
			wantName: "fk", wantTo: "other",
			wantCols: []string{"a"}, wantToCols: nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			def := parseTableDef(c.sql)
			if len(def.Constraints) != 1 {
				t.Fatalf("parseTableDef gave %d constraints, want 1: %q", len(def.Constraints), constraintNames(def))
			}
			got := def.Constraints[0]
			if got.Name != c.wantName || got.Kind != "FOREIGN KEY" {
				t.Errorf("constraint = %q %q, want %q FOREIGN KEY", got.Name, got.Kind, c.wantName)
			}
			if got.ToTable != c.wantTo {
				t.Errorf("ToTable = %q, want %q", got.ToTable, c.wantTo)
			}
			if !slices.Equal(got.Columns, c.wantCols) {
				t.Errorf("Columns = %q, want %q", got.Columns, c.wantCols)
			}
			if !slices.Equal(got.ToCols, c.wantToCols) {
				t.Errorf("ToCols = %q, want %q", got.ToCols, c.wantToCols)
			}
		})
	}
}

func TestParseTableDefColumnAfterForeignKey(t *testing.T) {
	// The REFERENCES clause of a column constraint carries its own
	// parenthesised column list; the parser has to step over it, or the
	// target column would be mistaken for a further constraint keyword.
	def := parseTableDef(`CREATE TABLE t (
		a integer CONSTRAINT fk REFERENCES other (id) ON DELETE SET NULL,
		b integer UNIQUE)`)
	if got, want := constraintNames(def), []string{"fk:FOREIGN KEY(a)", "__unnamed_constraint_1__:UNIQUE(b)"}; !slices.Equal(got, want) {
		t.Errorf("constraints = %q, want %q", got, want)
	}
	var names []string
	for _, c := range def.Columns {
		names = append(names, c.Name)
	}
	if want := []string{"a", "b"}; !slices.Equal(names, want) {
		t.Errorf("columns = %q, want %q", names, want)
	}
}

func TestIndexColumnOrders(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		want []m.SortOrder
	}{
		{"default ascending", `CREATE INDEX i ON t ("a", "b")`, []m.SortOrder{m.SortAsc, m.SortAsc}},
		{"explicit desc", `CREATE INDEX i ON t ("a" DESC, "b")`, []m.SortOrder{m.SortDesc, m.SortAsc}},
		{"explicit asc", `CREATE INDEX i ON t ("a" ASC)`, []m.SortOrder{m.SortAsc}},
		{"lower case desc", `CREATE INDEX i ON t (a desc)`, []m.SortOrder{m.SortDesc}},
		// The order is the last keyword of the element, so a collation
		// before it must not hide it.
		{"collation then desc", `CREATE INDEX i ON t (a COLLATE NOCASE DESC)`, []m.SortOrder{m.SortDesc}},
		{"expression", `CREATE INDEX i ON t (lower(a) DESC, b)`, []m.SortOrder{m.SortDesc, m.SortAsc}},
		{"no column list", `CREATE INDEX i ON t`, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := indexColumnOrders(c.sql)
			if !slices.Equal(got, c.want) {
				t.Errorf("indexColumnOrders(%q) = %q, want %q", c.sql, got, c.want)
			}
		})
	}
}
