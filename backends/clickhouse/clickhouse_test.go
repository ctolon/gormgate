package clickhouse

import (
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ctolon/gormgate/backends/base"
	m "github.com/ctolon/gormgate/migrations"
)

// editor returns an editor bound to a connection that carries nothing but
// the backend: everything tested here renders SQL or refuses to, and never
// talks to a server.
func editor(t *testing.T) *Editor {
	t.Helper()
	return NewEditor(&base.Conn{Backend: Backend}, true, false)
}

// model renders a one-model state.
func model(t *testing.T, fields m.Fields, opts m.Options) *m.Model {
	t.Helper()
	st := m.NewProjectState()
	st.AddModel(&m.ModelState{App: "shop", Name: "Item", Table: "shop_items", Fields: fields, Options: opts})
	apps, err := st.Apps()
	if err != nil {
		t.Fatalf("rendering the state: %v", err)
	}
	return apps.MustModel("shop", "Item")
}

var itemFields = m.Fields{
	{Name: "id", Field: m.Field{Type: m.Int, Size: 64, PrimaryKey: true}},
	{Name: "name", Field: m.Field{Type: m.String}},
	{Name: "day", Field: m.Field{Type: m.Time}},
}

func TestQuoteName(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"plain", "t", "`t`"},
		{"already quoted", "`t`", "`t`"},
		{"embedded backtick", "a`b", "`a``b`"},
		// ClickHouse has no dotted identifier in the DDL gormgate writes,
		// so a dot is part of the name.
		{"dotted", "db.t", "`db.t`"},
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
		{"quote doubled", "a'b", "'a''b'"},
		{"true", true, "true"},
		{"false", false, "false"},
		{"nil", nil, "NULL"},
		{"int", -3, "-3"},
		// ClickHouse takes bytes as the characters of a String, not as a
		// hexadecimal literal.
		{"bytes", []byte("ab"), "'ab'"},
		{"bytes with a quote", []byte("a'b"), "'a''b'"},
		{"time", time.Date(2024, 3, 1, 12, 0, 0, 0, time.UTC), "'2024-03-01 12:00:00'"},
		{"time with millis", time.Date(2024, 3, 1, 12, 0, 0, 123000000, time.UTC), "'2024-03-01 12:00:00.123'"},
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
	_, err := (Ops{}).QuoteValue(&m.GoExpr{Source: "time.Now()"})
	if err == nil {
		t.Fatal("QuoteValue of a GoExpr returned no error")
	}
	if !strings.Contains(err.Error(), "time.Now()") {
		t.Errorf("QuoteValue of a GoExpr error = %q, want it to name the source", err)
	}
}

func TestCheckVersion(t *testing.T) {
	cases := []struct {
		name, version, want string
	}{
		{"current", "24.8.4.13", ""},
		{"minimum", "22.8.1", ""},
		{"older minor", "22.7.9", "gormgate: ClickHouse 22.7.9 is too old; 22.8 or newer is required (lightweight DELETE)"},
		{"older major", "21.12.1", "gormgate: ClickHouse 21.12.1 is too old; 22.8 or newer is required (lightweight DELETE)"},
		{"unreadable", "unknown", `gormgate: cannot read the ClickHouse server version from "unknown"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := checkVersion(nil, c.version)
			if c.want == "" {
				if err != nil {
					t.Fatalf("checkVersion(%q) = %v, want nil", c.version, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("checkVersion(%q) = nil, want %q", c.version, c.want)
			}
			if err.Error() != c.want {
				t.Errorf("checkVersion(%q) = %q, want %q", c.version, err, c.want)
			}
		})
	}
}

func TestGranularity(t *testing.T) {
	cases := []struct {
		name, option string
		want         int
		wantErr      string
	}{
		{"default", "", DefaultGranularity, ""},
		{"number", "4", 4, ""},
		{"keyword", "GRANULARITY 4", 4, ""},
		{"lower case keyword", "granularity 8", 8, ""},
		{"spaces", "  16  ", 16, ""},
		{"not a granularity", "WITH PARSER ngram", 0,
			`clickhouse: index "i": Option "WITH PARSER ngram" is not a GRANULARITY`},
		// A granularity of zero is not a granularity: ClickHouse would
		// reject the index.
		{"zero", "0", 0, `clickhouse: index "i": GRANULARITY "0" must be a positive number`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := granularity(m.Index{Name: "i", Option: c.option})
			if c.wantErr != "" {
				if err == nil {
					t.Fatalf("granularity(%q) = %d, nil, want error %q", c.option, got, c.wantErr)
				}
				if err.Error() != c.wantErr {
					t.Errorf("granularity(%q) error = %q, want %q", c.option, err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("granularity(%q) error: %v", c.option, err)
			}
			if got != c.want {
				t.Errorf("granularity(%q) = %d, want %d", c.option, got, c.want)
			}
		})
	}
}

func TestEngineSQL(t *testing.T) {
	cases := []struct {
		name string
		opts m.Options
		want string
	}{
		{
			// A MergeTree table must be sorted; without an explicit
			// ORDER BY the primary key columns are the sorting key.
			"default engine sorts by the primary key", m.Options{},
			" ENGINE = MergeTree() ORDER BY (`id`)",
		},
		{
			"explicit engine and order",
			m.Options{ClickHouse: &m.ClickHouseTable{Engine: "ReplacingMergeTree(day)", OrderBy: "(name)"}},
			" ENGINE = ReplacingMergeTree(day) ORDER BY (name)",
		},
		{
			// The clauses have a fixed order: PARTITION BY, PRIMARY KEY,
			// ORDER BY, SETTINGS.
			"every clause",
			m.Options{ClickHouse: &m.ClickHouseTable{
				Engine: "MergeTree()", OrderBy: "(id, name)", PartitionBy: "toYYYYMM(day)",
				PrimaryKey: "(id)", Settings: "index_granularity = 8192",
			}},
			" ENGINE = MergeTree() PARTITION BY toYYYYMM(day) PRIMARY KEY (id) ORDER BY (id, name) SETTINGS index_granularity = 8192",
		},
		{
			"non-MergeTree engine is not sorted",
			m.Options{ClickHouse: &m.ClickHouseTable{Engine: "Log"}},
			" ENGINE = Log",
		},
	}
	e := editor(t)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := e.EngineSQL(model(t, itemFields, c.opts)); got != c.want {
				t.Errorf("EngineSQL = %q, want %q", got, c.want)
			}
		})
	}
}

func TestEngineSQLWithoutPrimaryKey(t *testing.T) {
	// A model with no primary key is stored unsorted, which is the gorm
	// driver's ORDER BY tuple().
	fields := m.Fields{{Name: "name", Field: m.Field{Type: m.String}}}
	got := editor(t).EngineSQL(model(t, fields, m.Options{}))
	if want := " ENGINE = MergeTree() ORDER BY tuple()"; got != want {
		t.Errorf("EngineSQL = %q, want %q", got, want)
	}
}

func TestKeyColumns(t *testing.T) {
	cases := []struct {
		name string
		opts m.Options
		want []string
	}{
		// Without a ClickHouse ORDER BY the sorting key is the primary key.
		{"primary key", m.Options{}, []string{"id"}},
		{"explicit order by", m.Options{ClickHouse: &m.ClickHouseTable{OrderBy: "(name)"}}, []string{"name"}},
		{"expression order by", m.Options{ClickHouse: &m.ClickHouseTable{OrderBy: "(toDate(day), name)"}}, []string{"day", "name"}},
		{"partition by", m.Options{ClickHouse: &m.ClickHouseTable{OrderBy: "(id)", PartitionBy: "toYYYYMM(day)"}}, []string{"day", "id"}},
		{"clickhouse primary key", m.Options{ClickHouse: &m.ClickHouseTable{OrderBy: "(id)", PrimaryKey: "(name)"}}, []string{"id", "name"}},
		// An engine clause without an ORDER BY still falls back to the
		// model's primary key.
		{"engine only", m.Options{ClickHouse: &m.ClickHouseTable{Engine: "MergeTree()"}}, []string{"id"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := slices.Sorted(maps.Keys(keyColumns(model(t, itemFields, c.opts))))
			if !slices.Equal(got, c.want) {
				t.Errorf("keyColumns = %q, want %q", got, c.want)
			}
		})
	}
}

// wantNotSupported checks that err is a NotSupportedError with the exact
// message: the message is what the user is shown when a migration cannot
// run on ClickHouse.
func wantNotSupported(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("got no error, want %q", want)
	}
	var nse *m.NotSupportedError
	if !errors.As(err, &nse) {
		t.Fatalf("error is %T (%v), want *migrations.NotSupportedError", err, err)
	}
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err, want)
	}
}

func TestTableSQLRejectsForeignKeys(t *testing.T) {
	st := m.NewProjectState()
	st.AddModel(&m.ModelState{App: "shop", Name: "Order", Table: "shop_orders", Fields: m.Fields{
		{Name: "id", Field: m.Field{Type: m.Int, Size: 64, PrimaryKey: true}},
	}})
	st.AddModel(&m.ModelState{App: "shop", Name: "Line", Table: "shop_lines", Fields: m.Fields{
		{Name: "id", Field: m.Field{Type: m.Int, Size: 64, PrimaryKey: true}},
		{Name: "order_id", Field: m.Field{Type: m.Int, Size: 64, ForeignKey: &m.ForeignKey{To: "shop.Order", Name: "fk_lines_order"}}},
	}})
	apps, err := st.Apps()
	if err != nil {
		t.Fatalf("rendering the state: %v", err)
	}
	_, err = editor(t).TableSQL(apps.MustModel("shop", "Line"))
	wantNotSupported(t, err,
		"clickhouse: CreateModel shop.Line.order_id: ClickHouse has no foreign keys (the field references shop.Order)")
}

func TestTableSQLRejectsUniqueField(t *testing.T) {
	fields := m.Fields{
		{Name: "id", Field: m.Field{Type: m.Int, Size: 64, PrimaryKey: true}},
		{Name: "code", Field: m.Field{Type: m.String, Unique: true}},
	}
	_, err := editor(t).TableSQL(model(t, fields, m.Options{}))
	wantNotSupported(t, err,
		"clickhouse: CreateModel shop.Item.code: ClickHouse has no unique constraints or unique indexes (the field is unique)")
}

func TestTableSQLRejectsUniqueTogether(t *testing.T) {
	opts := m.Options{UniqueTogether: [][]string{{"name", "day"}}}
	_, err := editor(t).TableSQL(model(t, itemFields, opts))
	wantNotSupported(t, err,
		"clickhouse: CreateModel shop.Item: ClickHouse has no unique constraints or unique indexes (unique_together (name, day))")
}

func TestTableSQLRejectsUniqueConstraint(t *testing.T) {
	opts := m.Options{Constraints: []m.Constraint{&m.UniqueConstraint{Name: "uq_name", Fields: []string{"name"}}}}
	_, err := editor(t).TableSQL(model(t, itemFields, opts))
	wantNotSupported(t, err,
		`clickhouse: CreateModel shop.Item: ClickHouse has no unique constraints or unique indexes (constraint "uq_name")`)
}

func TestConstraintSQLRejectsUniqueConstraint(t *testing.T) {
	e := editor(t)
	mdl := model(t, itemFields, m.Options{})
	uc := &m.UniqueConstraint{Name: "uq_name", Fields: []string{"name"}}

	_, err := e.ConstraintSQL(mdl, uc)
	wantNotSupported(t, err,
		`clickhouse: CreateModel shop.Item: ClickHouse has no unique constraints or unique indexes (constraint "uq_name")`)

	_, err = e.CreateConstraintSQL(mdl, uc)
	wantNotSupported(t, err,
		`clickhouse: AddConstraint shop.Item: ClickHouse has no unique constraints or unique indexes (constraint "uq_name")`)

	_, err = e.RemoveConstraintSQL(mdl, uc)
	wantNotSupported(t, err,
		`clickhouse: RemoveConstraint shop.Item: ClickHouse has no unique constraints or unique indexes (constraint "uq_name")`)
}

// TestCompositeForeignKeyNotSupported pins the refusal on the one backend
// that cannot have a composite foreign key: ClickHouse has no foreign keys
// at all, so a table-level one is refused exactly as a field-level one is,
// rather than being silently dropped.
func TestCompositeForeignKeyNotSupported(t *testing.T) {
	fkc := &m.ForeignKeyConstraint{
		Name:     "fk_shop_items_order",
		Fields:   []string{"order_id", "order_line"},
		To:       "shop.Order",
		ToFields: []string{"id", "line"},
	}
	const reason = `ClickHouse has no foreign keys (constraint "fk_shop_items_order" references shop.Order)`
	e := editor(t)
	mdl := model(t, itemFields, m.Options{})

	_, err := e.ConstraintSQL(mdl, fkc)
	wantNotSupported(t, err, "clickhouse: CreateModel shop.Item: "+reason)

	_, err = e.CreateConstraintSQL(mdl, fkc)
	wantNotSupported(t, err, "clickhouse: AddConstraint shop.Item: "+reason)

	_, err = e.RemoveConstraintSQL(mdl, fkc)
	wantNotSupported(t, err, "clickhouse: RemoveConstraint shop.Item: "+reason)

	// The whole model is refused too, so a CreateModel never half-applies.
	st := m.NewProjectState()
	st.AddModel(&m.ModelState{App: "shop", Name: "Order", Table: "shop_orders", Fields: m.Fields{
		{Name: "id", Field: m.Field{Type: m.Int, Size: 64, PrimaryKey: true}},
		{Name: "line", Field: m.Field{Type: m.Int, Size: 64, PrimaryKey: true}},
	}})
	st.AddModel(&m.ModelState{App: "shop", Name: "Item", Table: "shop_items",
		Fields: m.Fields{
			{Name: "id", Field: m.Field{Type: m.Int, Size: 64, PrimaryKey: true}},
			{Name: "order_id", Field: m.Field{Type: m.Int, Size: 64}},
			{Name: "order_line", Field: m.Field{Type: m.Int, Size: 64}},
		},
		Options: m.Options{Constraints: []m.Constraint{fkc}}})
	apps, err := st.Apps()
	if err != nil {
		t.Fatalf("rendering the state: %v", err)
	}
	_, err = e.TableSQL(apps.MustModel("shop", "Item"))
	wantNotSupported(t, err, "clickhouse: CreateModel shop.Item: "+reason)
}

func TestConstraintSQLAllowsCheck(t *testing.T) {
	// A CHECK constraint is the one constraint ClickHouse does have.
	got, err := editor(t).ConstraintSQL(model(t, itemFields, m.Options{}),
		&m.CheckConstraint{Name: "ck_id", Check: "id > 0"})
	if err != nil {
		t.Fatalf("ConstraintSQL error: %v", err)
	}
	if want := "CONSTRAINT `ck_id` CHECK (id > 0)"; got != want {
		t.Errorf("ConstraintSQL = %q, want %q", got, want)
	}
}

func TestRenameIndexNotSupported(t *testing.T) {
	err := editor(t).RenameIndex(model(t, itemFields, m.Options{}),
		m.Index{Name: "old_ix"}, m.Index{Name: "new_ix"})
	wantNotSupported(t, err,
		`clickhouse: RenameIndex shop.Item: ClickHouse cannot rename index "old_ix"; remove it and add "new_ix" instead`)
}

func TestAlterUniqueTogether(t *testing.T) {
	e := editor(t)
	mdl := model(t, itemFields, m.Options{})
	// Nothing to do is not an error, so a migration that carries an empty
	// AlterUniqueTogether still runs.
	if err := e.AlterUniqueTogether(mdl, nil, nil); err != nil {
		t.Errorf("AlterUniqueTogether(nil, nil) = %v, want nil", err)
	}
	err := e.AlterUniqueTogether(mdl, nil, [][]string{{"name"}})
	wantNotSupported(t, err,
		"clickhouse: AlterUniqueTogether shop.Item: ClickHouse has no unique constraints or unique indexes")
}

func TestRemoveFieldRejectsKeyColumn(t *testing.T) {
	mdl := model(t, itemFields, m.Options{})
	err := editor(t).RemoveField(mdl, mdl.Field("id"))
	wantNotSupported(t, err, "clickhouse: RemoveField shop.Item.id: "+keyReason)
}

func TestCreateIndexSQL(t *testing.T) {
	mdl := model(t, itemFields, m.Options{})
	cases := []struct {
		name string
		ix   m.Index
		want string
	}{
		{
			"default granularity",
			m.Index{Name: "ix_name", Fields: m.Columns("name"), Type: "minmax"},
			"ALTER TABLE `shop_items` ADD INDEX `ix_name` (`name`) TYPE minmax GRANULARITY 3",
		},
		{
			"explicit granularity and several columns",
			m.Index{Name: "ix_nd", Fields: m.Columns("name", "day"), Type: "set(100)", Option: "GRANULARITY 8"},
			"ALTER TABLE `shop_items` ADD INDEX `ix_nd` (`name`, `day`) TYPE set(100) GRANULARITY 8",
		},
		{
			"expression",
			m.Index{Name: "ix_expr", Fields: []m.IndexField{{Expression: "lower(name)"}}, Type: "bloom_filter"},
			"ALTER TABLE `shop_items` ADD INDEX `ix_expr` (lower(name)) TYPE bloom_filter GRANULARITY 3",
		},
	}
	e := editor(t)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st, err := e.CreateIndexSQL(mdl, c.ix)
			if err != nil {
				t.Fatalf("CreateIndexSQL error: %v", err)
			}
			if got := st.String(); got != c.want {
				t.Errorf("CreateIndexSQL = %q, want %q", got, c.want)
			}
		})
	}
}

func TestCreateIndexSQLRejections(t *testing.T) {
	const subject = `shop.Item (index "ix")`
	cases := []struct {
		name string
		ix   m.Index
		want string
	}{
		{"unique", m.Index{Name: "ix", Fields: m.Columns("name"), Unique: true},
			"clickhouse: AddIndex " + subject + ": " + noUnique},
		{"no type", m.Index{Name: "ix", Fields: m.Columns("name")},
			"clickhouse: AddIndex " + subject + `: ClickHouse only has data-skipping indexes, so Index.Type is required (for example "minmax", "set(100)" or "bloom_filter")`},
		{"condition", m.Index{Name: "ix", Fields: m.Columns("name"), Type: "minmax", Where: "id > 0"},
			"clickhouse: AddIndex " + subject + ": a ClickHouse data-skipping index has no condition"},
		{"include", m.Index{Name: "ix", Fields: m.Columns("name"), Type: "minmax", Include: []string{"day"}},
			"clickhouse: AddIndex " + subject + ": a ClickHouse data-skipping index has no INCLUDE columns"},
		{"class", m.Index{Name: "ix", Fields: m.Columns("name"), Type: "minmax", Class: "FULLTEXT"},
			"clickhouse: AddIndex " + subject + ": ClickHouse has no index class; use Index.Type"},
		{"comment", m.Index{Name: "ix", Fields: m.Columns("name"), Type: "minmax", Comment: "c"},
			"clickhouse: AddIndex " + subject + ": a ClickHouse data-skipping index has no comment"},
		{"sort", m.Index{Name: "ix", Fields: []m.IndexField{{Column: "name", Sort: "DESC"}}, Type: "minmax"},
			"clickhouse: AddIndex " + subject + ": a ClickHouse data-skipping index has no column order, collation or prefix length"},
		{"prefix length", m.Index{Name: "ix", Fields: []m.IndexField{{Column: "name", Length: 10}}, Type: "minmax"},
			"clickhouse: AddIndex " + subject + ": a ClickHouse data-skipping index has no column order, collation or prefix length"},
	}
	e := editor(t)
	mdl := model(t, itemFields, m.Options{})
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := e.CreateIndexSQL(mdl, c.ix)
			wantNotSupported(t, err, c.want)
		})
	}
}

func TestCreateIndexSQLBadGranularity(t *testing.T) {
	// A malformed GRANULARITY is a mistake in the migration, not an
	// unsupported operation, so it is a plain error.
	_, err := editor(t).CreateIndexSQL(model(t, itemFields, m.Options{}),
		m.Index{Name: "ix", Fields: m.Columns("name"), Type: "minmax", Option: "nonsense"})
	if err == nil {
		t.Fatal("CreateIndexSQL with a malformed GRANULARITY returned no error")
	}
	var nse *m.NotSupportedError
	if errors.As(err, &nse) {
		t.Errorf("error is a NotSupportedError (%v), want a plain error", err)
	}
	if want := `clickhouse: index "ix": Option "nonsense" is not a GRANULARITY`; err.Error() != want {
		t.Errorf("error = %q, want %q", err, want)
	}
}

func TestTransactionSQLIsEmpty(t *testing.T) {
	// ClickHouse has no transaction covering DDL, so sqlmigrate must not
	// wrap its output in one.
	if got := (Ops{}).StartTransactionSQL(); got != "" {
		t.Errorf("StartTransactionSQL = %q, want %q", got, "")
	}
	if got := (Ops{}).EndTransactionSQL(); got != "" {
		t.Errorf("EndTransactionSQL = %q, want %q", got, "")
	}
}

func TestSequenceResetSQL(t *testing.T) {
	got, err := SequenceResetSQL(nil, base.PlainStyle{}, nil)
	if err != nil || got != nil {
		t.Errorf("SequenceResetSQL = %q, %v, want nil, nil", got, err)
	}
}
