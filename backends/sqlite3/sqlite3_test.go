package sqlite3

import (
	"slices"
	"strings"
	"testing"
	"time"

	m "github.com/ctolon/gormgate/migrations"
)

func TestParseVersion(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want [3]int
	}{
		{"release", "3.45.1", [3]int{3, 45, 1}},
		{"two parts", "3.31", [3]int{3, 31, 0}},
		// A pre-release part counts as the number it starts with, so that
		// 3.35.5-rc1 is not read as 3.35.0.
		{"pre-release", "3.35.5-rc1", [3]int{3, 35, 5}},
		{"spaces", "  3.40.0  ", [3]int{3, 40, 0}},
		{"not a version", "unknown", [3]int{0, 0, 0}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := parseVersion(c.in); got != c.want {
				t.Errorf("parseVersion(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

func TestCompareVersion(t *testing.T) {
	// The comparison decides whether the server is new enough to be used
	// at all (3.31) and whether ALTER TABLE ... DROP COLUMN may be used
	// instead of remaking the whole table (3.35.5).
	cases := []struct {
		name string
		v    string
		want [3]int
		sign int
	}{
		{"equal to minimum", "3.31.0", minVersion, 0},
		{"below minimum", "3.30.1", minVersion, -1},
		{"above minimum", "3.45.1", minVersion, 1},
		{"minor decides", "3.36.0", dropColumnVersion, 1},
		{"patch decides", "3.35.4", dropColumnVersion, -1},
		{"patch equal", "3.35.5", dropColumnVersion, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := compareVersion(c.v, c.want); got != c.sign {
				t.Errorf("compareVersion(%q, %v) = %d, want %d", c.v, c.want, got, c.sign)
			}
		})
	}
}

func TestQuoteName(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"plain", "t", `"t"`},
		{"already quoted", `"t"`, `"t"`},
		{"embedded quote", `a"b`, `"a""b"`},
		// A dot is part of the name: SQLite has no qualified identifiers
		// in the DDL gormgate writes.
		{"dotted", "a.b", `"a.b"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := (Ops{}).QuoteName(c.in); got != c.want {
				t.Errorf("QuoteName(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestQuoteValue(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want string
	}{
		// SQLite has no backslash escape: a quote is doubled.
		{"quote", "a'b", "'a''b'"},
		{"backslash", `a\b`, `'a\b'`},
		{"true", true, "1"},
		{"false", false, "0"},
		{"nil", nil, "NULL"},
		{"bytes", []byte{0x00, 0xff}, "X'00ff'"},
		{"time", time.Date(2024, 3, 1, 12, 0, 0, 0, time.UTC), "'2024-03-01 12:00:00'"},
		{"float", 1.5, "1.5"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := (Ops{}).QuoteValue(c.in)
			if err != nil {
				t.Fatalf("QuoteValue(%v) error: %v", c.in, err)
			}
			if got != c.want {
				t.Errorf("QuoteValue(%v) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestQuoteValueGoExpr(t *testing.T) {
	_, err := (Ops{}).QuoteValue(&m.GoExpr{Source: "uuid.New()"})
	if err == nil {
		t.Fatal("QuoteValue of a GoExpr returned no error")
	}
	if !strings.Contains(err.Error(), "uuid.New()") {
		t.Errorf("QuoteValue of a GoExpr error = %q, want it to name the source", err)
	}
}

func TestDBDefaultSQL(t *testing.T) {
	// gorm's sqlite dialector explains a bound variable with a double
	// quote as the escaper, so a string default reads DEFAULT "x" and the
	// text PRAGMA table_info reports matches what AutoMigrate creates.
	e := &Editor{}
	cases := []struct {
		name string
		d    *m.DBDefault
		want string
	}{
		{"expression", m.DBExpr("CURRENT_TIMESTAMP"), "CURRENT_TIMESTAMP"},
		{"string", m.DBValue("x"), `"x"`},
		{"string with a quote", m.DBValue(`a"b`), `"a""b"`},
		{"bool", m.DBValue(true), "true"},
		{"int", m.DBValue(7), "7"},
		{"float", m.DBValue(1.5), "1.5"},
		{"zero time", m.DBValue(time.Time{}), `"0000-00-00 00:00:00"`},
		{"time", m.DBValue(time.Date(2024, 3, 1, 12, 0, 0, 0, time.UTC)), `"2024-03-01 12:00:00"`},
		// ExplainSQL keeps printable bytes as a string and refuses to
		// print the rest.
		{"printable bytes", m.DBValue([]byte("ab")), `"ab"`},
		{"binary bytes", m.DBValue([]byte{0x00}), `"<binary>"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := e.DBDefaultSQL(&m.ModelField{Field: m.Field{DBDefault: c.d}})
			if err != nil {
				t.Fatalf("DBDefaultSQL error: %v", err)
			}
			if got != c.want {
				t.Errorf("DBDefaultSQL = %q, want %q", got, c.want)
			}
		})
	}
}

func TestMentionsColumn(t *testing.T) {
	// Whether a column is used by a CHECK or a partial index decides
	// whether SQLite may drop it with ALTER TABLE or has to remake the
	// table, so a substring match would remake far too often and a missed
	// match would emit DDL SQLite rejects.
	cases := []struct {
		name, sql, column string
		want              bool
	}{
		{"bare", "price > 0", "price", true},
		{"quoted", `"price" > 0`, "price", true},
		{"prefix of a longer name", "price_cents > 0", "price", false},
		{"suffix of a longer name", "unit_price > 0", "price", false},
		{"inside a word", "xpricey > 0", "price", false},
		{"at the end", "0 < price", "price", true},
		{"whole string", "price", "price", true},
		{"absent", "qty > 0", "price", false},
		{"empty column", "price > 0", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := mentionsColumn(c.sql, c.column); got != c.want {
				t.Errorf("mentionsColumn(%q, %q) = %v, want %v", c.sql, c.column, got, c.want)
			}
		})
	}
}

// model builds a rendered one-model state with the given fields and
// options.
func model(t *testing.T, fields m.Fields, opts m.Options) *m.Model {
	t.Helper()
	st := m.NewProjectState()
	st.AddModel(&m.ModelState{App: "shop", Name: "Item", Table: "shop_item", Fields: fields, Options: opts})
	apps, err := st.Apps()
	if err != nil {
		t.Fatalf("rendering the state: %v", err)
	}
	return apps.MustModel("shop", "Item")
}

var itemFields = m.Fields{
	{Name: "id", Field: m.Field{Type: m.Int, Size: 64, PrimaryKey: true, AutoIncrement: true}},
	{Name: "price", Field: m.Field{Type: m.Int, Size: 64}},
	{Name: "qty", Field: m.Field{Type: m.Int, Size: 64}},
}

func TestColumnIsIndexed(t *testing.T) {
	cases := []struct {
		name   string
		opts   m.Options
		column string
		want   bool
	}{
		{"plain index", m.Options{Indexes: []m.Index{{Name: "i", Fields: m.Columns("price")}}}, "price", true},
		{"other column", m.Options{Indexes: []m.Index{{Name: "i", Fields: m.Columns("qty")}}}, "price", false},
		{"expression index", m.Options{Indexes: []m.Index{{Name: "i", Fields: []m.IndexField{{Expression: "price * 2"}}}}}, "price", true},
		{"partial index condition", m.Options{Indexes: []m.Index{{Name: "i", Fields: m.Columns("qty"), Where: "price > 0"}}}, "price", true},
		{"unique together", m.Options{UniqueTogether: [][]string{{"price", "qty"}}}, "price", true},
		{"nothing", m.Options{}, "price", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := columnIsIndexed(model(t, itemFields, c.opts), c.column); got != c.want {
				t.Errorf("columnIsIndexed(%q) = %v, want %v", c.column, got, c.want)
			}
		})
	}
}

func TestColumnInConstraint(t *testing.T) {
	cases := []struct {
		name   string
		opts   m.Options
		column string
		want   bool
	}{
		{"check", m.Options{Constraints: []m.Constraint{&m.CheckConstraint{Name: "c", Check: "price > 0"}}}, "price", true},
		{"check on another column", m.Options{Constraints: []m.Constraint{&m.CheckConstraint{Name: "c", Check: "qty > 0"}}}, "price", false},
		{"unique fields", m.Options{Constraints: []m.Constraint{&m.UniqueConstraint{Name: "u", Fields: []string{"price"}}}}, "price", true},
		{"unique include", m.Options{Constraints: []m.Constraint{&m.UniqueConstraint{Name: "u", Fields: []string{"qty"}, Include: []string{"price"}}}}, "price", true},
		{"unique condition", m.Options{Constraints: []m.Constraint{&m.UniqueConstraint{Name: "u", Fields: []string{"qty"}, Condition: "price > 0"}}}, "price", true},
		{"nothing", m.Options{}, "price", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := columnInConstraint(model(t, itemFields, c.opts), c.column); got != c.want {
				t.Errorf("columnInConstraint(%q) = %v, want %v", c.column, got, c.want)
			}
		})
	}
}

func TestIndexCovers(t *testing.T) {
	cases := []struct {
		name   string
		ix     m.Index
		column string
		want   bool
	}{
		{"column", m.Index{Fields: m.Columns("price")}, "price", true},
		{"other column", m.Index{Fields: m.Columns("qty")}, "price", false},
		{"expression", m.Index{Fields: []m.IndexField{{Expression: "lower(price)"}}}, "price", true},
		{"condition", m.Index{Fields: m.Columns("qty"), Where: "price IS NOT NULL"}, "price", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := indexCovers(c.ix, c.column); got != c.want {
				t.Errorf("indexCovers(%q) = %v, want %v", c.column, got, c.want)
			}
		})
	}
}

func TestRemakeOnly(t *testing.T) {
	// SQLite has no ALTER TABLE ... ADD CONSTRAINT: a unique constraint
	// that a unique index can express is added as one, everything else
	// needs the table remade.
	cases := []struct {
		name string
		c    m.Constraint
		want bool
	}{
		{"check", &m.CheckConstraint{Name: "c", Check: "price > 0"}, true},
		{"plain unique", &m.UniqueConstraint{Name: "u", Fields: []string{"price"}}, true},
		{"partial unique", &m.UniqueConstraint{Name: "u", Fields: []string{"price"}, Condition: "qty > 0"}, false},
		{"covering unique", &m.UniqueConstraint{Name: "u", Fields: []string{"price"}, Include: []string{"qty"}}, false},
		{"deferrable unique", &m.UniqueConstraint{Name: "u", Fields: []string{"price"}, Deferrable: "DEFERRED"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := remakeOnly(c.c); got != c.want {
				t.Errorf("remakeOnly(%T) = %v, want %v", c.c, got, c.want)
			}
		})
	}
}

func TestMapping(t *testing.T) {
	// The mapping is the column list and the SELECT list of the INSERT
	// that copies the data into the remade table: the two have to stay
	// aligned and keep the column order of the table.
	mp := &mapping{}
	mp.set("a", `"a"`)
	mp.set("b", `"b"`)
	mp.set("c", `"c"`)

	mp.set("b", "coalesce(\"b\", 0)")
	if got, want := mp.cols, []string{"a", "b", "c"}; !slices.Equal(got, want) {
		t.Fatalf("after set of an existing column, cols = %q, want %q", got, want)
	}
	if got, want := mp.exprs[1], `coalesce("b", 0)`; got != want {
		t.Errorf("exprs[1] = %q, want %q", got, want)
	}

	// A rename keeps the column in place rather than moving it to the end.
	mp.replace("b", "bb", `"b"`)
	if got, want := mp.cols, []string{"a", "bb", "c"}; !slices.Equal(got, want) {
		t.Errorf("after replace, cols = %q, want %q", got, want)
	}
	if got, want := mp.exprs, []string{`"a"`, `"b"`, `"c"`}; !slices.Equal(got, want) {
		t.Errorf("after replace, exprs = %q, want %q", got, want)
	}

	// Replacing a column that is not there appends it.
	mp.replace("gone", "d", `"d"`)
	if got, want := mp.cols, []string{"a", "bb", "c", "d"}; !slices.Equal(got, want) {
		t.Errorf("after replace of a missing column, cols = %q, want %q", got, want)
	}

	mp.remove("bb")
	if got, want := mp.cols, []string{"a", "c", "d"}; !slices.Equal(got, want) {
		t.Errorf("after remove, cols = %q, want %q", got, want)
	}
	if got, want := mp.exprs, []string{`"a"`, `"c"`, `"d"`}; !slices.Equal(got, want) {
		t.Errorf("after remove, exprs = %q, want %q", got, want)
	}

	// Removing a column that is not there leaves the mapping alone.
	mp.remove("gone")
	if len(mp.cols) != 3 || len(mp.exprs) != 3 {
		t.Errorf("after remove of a missing column, mapping = %q / %q, want 3 entries", mp.cols, mp.exprs)
	}
}

func TestFieldSize(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want int
	}{
		{"varchar", "varchar(11)", 11},
		{"char", "char(3)", 3},
		{"upper case", "VARCHAR(255)", 255},
		{"spaces", " varchar ( 8 ) ", 8},
		{"no size", "text", 0},
		// A type that merely starts with varchar is not one.
		{"trailing text", "varchar(11) NOT NULL", 0},
		{"decimal", "decimal(10,2)", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := fieldSize(c.in); got != c.want {
				t.Errorf("fieldSize(%q) = %d, want %d", c.in, got, c.want)
			}
		})
	}
}
