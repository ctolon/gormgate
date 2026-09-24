package oracle

import (
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ctolon/gormgate/backends/base"
	m "github.com/ctolon/gormgate/migrations"
)

func editor(t *testing.T) *Editor {
	t.Helper()
	return NewEditor(&base.Conn{Backend: Backend}, true, false)
}

func TestParseVersion(t *testing.T) {
	cases := []struct {
		name, banner string
		want         int
		ok           bool
	}{
		{"23ai", "Oracle Database 23ai Free Release 23.26.3.0.0 - Develop, Learn, and Run for Free", 23, true},
		{"19c", "Oracle Database 19c Enterprise Edition Release 19.0.0.0.0 - Production", 19, true},
		{"18c", "Oracle Database 18c Express Edition Release 18.0.0.0.0", 18, true},
		{"no version", "Oracle Database", 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := ParseVersion(c.banner)
			if got != c.want || ok != c.ok {
				t.Errorf("ParseVersion(%q) = %d, %v, want %d, %v", c.banner, got, ok, c.want, c.ok)
			}
		})
	}
}

func TestQuoteName(t *testing.T) {
	cases := []struct{ name, in, want string }{
		// Django upper-cases and truncates; gormgate has to address the
		// objects gorm-oracle creates, which are quoted verbatim.
		{"lower case kept", "t", `"t"`},
		{"already quoted", `"t"`, `"t"`},
		{"embedded quote", `a"b`, `"a""b"`},
		{"long name kept", strings.Repeat("a", 40), `"` + strings.Repeat("a", 40) + `"`},
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
		{"true", true, "1"},
		{"false", false, "0"},
		{"quote doubled", "a'b", "'a''b'"},
		{"nil", nil, "NULL"},
		// Oracle has no X'..' literal; a byte slice is its hexadecimal
		// text in a string literal, which is what RAW accepts.
		{"bytes", []byte{0xde, 0xad}, "'dead'"},
		{"empty bytes", []byte{}, "''"},
		{"time", time.Date(2024, 3, 1, 12, 0, 0, 0, time.UTC), "'2024-03-01 12:00:00'"},
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

func TestSQLLiteral(t *testing.T) {
	// The table and column names of the sequence-reset block go into
	// single-quoted literals rather than bound parameters.
	cases := []struct{ name, in, want string }{
		{"plain", "SHOP_ITEMS", "SHOP_ITEMS"},
		{"quote doubled", "a'b", "a''b"},
		{"two quotes", "a'b'c", "a''b''c"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := sqlLiteral(c.in); got != c.want {
				t.Errorf("sqlLiteral(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestNoAutofieldSequenceName(t *testing.T) {
	cases := []struct{ name, table, want string }{
		{"short", "shop_items", "SHOP_ITEMS_SQ"},
		// A table name too long for the sequence name is truncated to a
		// repeatable mangled form, so the same table always maps to the
		// same sequence.
		{"long", strings.Repeat("a", 200), strings.ToUpper(base.TruncateName(strings.Repeat("a", 200), MaxNameLength-3, 4)) + "_SQ"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := noAutofieldSequenceName(c.table)
			if got != c.want {
				t.Errorf("noAutofieldSequenceName(%q) = %q, want %q", c.table, got, c.want)
			}
			if len(got) > MaxNameLength {
				t.Errorf("noAutofieldSequenceName(%q) is %d characters, want at most %d", c.table, len(got), MaxNameLength)
			}
		})
	}
}

func TestOnDeleteSQL(t *testing.T) {
	cases := []struct {
		name   string
		action m.ReferentialAction
		want   string
		ok     bool
	}{
		{"none", "", "", true},
		{"cascade", m.Cascade, " ON DELETE CASCADE", true},
		{"set null", m.SetNull, " ON DELETE SET NULL", true},
		{"lower case", "cascade", " ON DELETE CASCADE", true},
		// NO ACTION and RESTRICT are Oracle's own behaviour, so they
		// produce no clause but are accepted.
		{"no action", m.NoAction, "", true},
		{"restrict", m.Restrict, "", true},
		// SET DEFAULT has no Oracle spelling at all.
		{"set default", m.SetDefault, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := onDeleteSQL(c.action)
			if got != c.want || ok != c.ok {
				t.Errorf("onDeleteSQL(%q) = %q, %v, want %q, %v", c.action, got, ok, c.want, c.ok)
			}
		})
	}
}

// fkField renders a model whose "order_id" column carries the foreign key.
func fkField(t *testing.T, fk *m.ForeignKey) *m.ModelField {
	t.Helper()
	st := m.NewProjectState()
	st.AddModel(&m.ModelState{App: "shop", Name: "Order", Table: "shop_orders", Fields: m.Fields{
		{Name: "id", Field: m.Field{Type: m.Int, Size: 64, PrimaryKey: true}},
	}})
	st.AddModel(&m.ModelState{App: "shop", Name: "Line", Table: "shop_lines", Fields: m.Fields{
		{Name: "id", Field: m.Field{Type: m.Int, Size: 64, PrimaryKey: true}},
		{Name: "order_id", Field: m.Field{Type: m.Int, Size: 64, ForeignKey: fk}},
	}})
	apps, err := st.Apps()
	if err != nil {
		t.Fatalf("rendering the state: %v", err)
	}
	return apps.MustModel("shop", "Line").Field("order_id")
}

func TestCheckForeignKeyRejectsOnUpdate(t *testing.T) {
	// Oracle's REFERENCES clause has no ON UPDATE action; gorm-oracle
	// emulates one with a trigger, which is not part of the migration
	// state, so the field is refused rather than silently changed.
	f := fkField(t, &m.ForeignKey{To: "shop.Order", Name: "fk_shop_lines_order", OnUpdate: m.Cascade})
	err := (&Editor{}).checkForeignKey(f)
	if err == nil {
		t.Fatal("checkForeignKey with OnUpdate returned no error")
	}
	var nse *m.NotSupportedError
	if !errors.As(err, &nse) {
		t.Fatalf("error is %T, want *migrations.NotSupportedError", err)
	}
	want := `Oracle foreign keys have no ON UPDATE action, but shop_lines.order_id (constraint fk_shop_lines_order) is ` +
		`declared with OnUpdate "CASCADE". gorm-oracle emulates ON UPDATE with an AFTER UPDATE trigger on the referenced ` +
		`table; gormgate does not, because such a trigger is not part of the migration state. Drop OnUpdate from the ` +
		`field, or create the trigger explicitly with a RunSQL operation.`
	if err.Error() != want {
		t.Errorf("checkForeignKey error = %q, want %q", err, want)
	}
}

func TestCheckForeignKeyRejectsOnDelete(t *testing.T) {
	f := fkField(t, &m.ForeignKey{To: "shop.Order", Name: "fk_shop_lines_order", OnDelete: m.SetDefault})
	err := (&Editor{}).checkForeignKey(f)
	if err == nil {
		t.Fatal("checkForeignKey with an unsupported OnDelete returned no error")
	}
	want := `Oracle foreign keys only support ON DELETE CASCADE and ON DELETE SET NULL, but shop_lines.order_id ` +
		`(constraint fk_shop_lines_order) is declared with OnDelete "SET DEFAULT".`
	if err.Error() != want {
		t.Errorf("checkForeignKey error = %q, want %q", err, want)
	}
}

func TestCheckForeignKeyAccepts(t *testing.T) {
	cases := []struct {
		name string
		fk   *m.ForeignKey
	}{
		{"no foreign key", nil},
		{"cascade", &m.ForeignKey{To: "shop.Order", Name: "fk", OnDelete: m.Cascade}},
		{"set null", &m.ForeignKey{To: "shop.Order", Name: "fk", OnDelete: m.SetNull}},
		{"restrict", &m.ForeignKey{To: "shop.Order", Name: "fk", OnDelete: m.Restrict}},
		{"nothing declared", &m.ForeignKey{To: "shop.Order", Name: "fk"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := fkField(t, c.fk)
			if err := (&Editor{}).checkForeignKey(f); err != nil {
				t.Errorf("checkForeignKey = %v, want nil", err)
			}
		})
	}
}

func TestSplitType(t *testing.T) {
	cases := []struct{ name, in, wantBase, wantIdentity string }{
		{"plain", "NUMBER(19)", "NUMBER(19)", ""},
		{"identity", "NUMBER(19) GENERATED BY DEFAULT AS IDENTITY", "NUMBER(19)", "GENERATED BY DEFAULT AS IDENTITY"},
		{"lower case", "number(19) generated always as identity", "number(19)", "generated always as identity"},
		{"spaces trimmed", "  VARCHAR2(50)  ", "VARCHAR2(50)", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b, id := splitType(c.in)
			if b != c.wantBase || id != c.wantIdentity {
				t.Errorf("splitType(%q) = %q, %q, want %q, %q", c.in, b, id, c.wantBase, c.wantIdentity)
			}
		})
	}
}

func TestNeedsTypeWorkaround(t *testing.T) {
	// Oracle always refuses these in place, so the editor has to take the
	// temporary-column route rather than let the statement fail halfway
	// through a migration it cannot roll back.
	cases := []struct {
		name, old, new string
		want           bool
	}{
		{"same type", "NUMBER(19)", "NUMBER(19)", false},
		{"widen", "NUMBER(10)", "NUMBER(19)", false},
		{"gains identity", "NUMBER(19)", "NUMBER(19) GENERATED BY DEFAULT AS IDENTITY", true},
		{"loses identity", "NUMBER(19) GENERATED BY DEFAULT AS IDENTITY", "NUMBER(19)", false},
		{"keeps identity", "NUMBER(19) GENERATED BY DEFAULT AS IDENTITY", "NUMBER(19) GENERATED BY DEFAULT AS IDENTITY", false},
		{"to clob", "VARCHAR2(50)", "CLOB", true},
		{"from clob", "CLOB", "VARCHAR2(50)", true},
		{"to blob", "RAW(16)", "BLOB", true},
		{"clob to nclob", "CLOB", "NCLOB", true},
		// A change of case alone is not a change of type.
		{"case only", "varchar2(50)", "VARCHAR2(50)", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := needsTypeWorkaround(c.old, c.new); got != c.want {
				t.Errorf("needsTypeWorkaround(%q, %q) = %v, want %v", c.old, c.new, got, c.want)
			}
		})
	}
}

func TestGormDefaultExpr(t *testing.T) {
	// gorm-oracle translates an unparsed `default:` tag itself; gormgate
	// has to write the same text, or AutoMigrate would re-alter the column.
	cases := []struct{ name, in, want string }{
		{"null", "NULL", "NULL"},
		{"lower null", "null", "NULL"},
		{"current timestamp", "CURRENT_TIMESTAMP", "CURRENT_TIMESTAMP"},
		{"now", "now()", "CURRENT_TIMESTAMP"},
		{"sysdate", "SYSDATE", "SYSDATE"},
		{"true", "true", "1"},
		{"false", "FALSE", "0"},
		{"sequence", "my_seq.NEXTVAL", "my_seq.NEXTVAL"},
		{"integer", "42", "42"},
		{"float", "1.5", "1.5"},
		{"negative", "-1", "-1"},
		{"date", "2024-03-01", "TO_DATE('2024-03-01', 'YYYY-MM-DD')"},
		// Ten characters with two dashes that are not a date stay text.
		{"not a date", "2024-33-01", "'2024-33-01'"},
		{"already quoted", "'x'", "'x'"},
		{"plain text", "x", "'x'"},
		{"text with a quote", "a'b", "'a''b'"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := gormDefaultExpr(c.in); got != c.want {
				t.Errorf("gormDefaultExpr(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestDBDefaultSQL(t *testing.T) {
	e := editor(t)
	cases := []struct {
		name string
		d    *m.DBDefault
		want string
	}{
		{"expression", m.DBExpr("SYSDATE"), "SYSDATE"},
		{"string", m.DBValue("x"), "'x'"},
		{"bool", m.DBValue(true), "1"},
		// gorm-oracle writes a time default as an explicit conversion.
		{"time", m.DBValue(time.Date(2024, 3, 1, 12, 0, 0, 0, time.UTC)),
			"TO_TIMESTAMP('2024-03-01 12:00:00', 'YYYY-MM-DD HH24:MI:SS')"},
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

func TestConversionSQL(t *testing.T) {
	// The workaround copies the old column into a new one of the new type;
	// Oracle needs the conversion spelled out where the types are not
	// assignable.
	e := editor(t)
	old := &m.ModelField{Name: "day", Column: "day", Field: m.Field{Type: m.String}}
	cases := []struct{ name, oldType, newType, want string }{
		{"no conversion", "NUMBER(19)", "NUMBER(10)", `"day"`},
		{"clob to varchar", "CLOB", "VARCHAR2(50)", `TO_CHAR("day")`},
		{"varchar to date", "VARCHAR2(50)", "DATE", `TO_DATE("day", 'YYYY-MM-DD')`},
		{"varchar to timestamp", "VARCHAR2(50)", "TIMESTAMP(6)", `TO_TIMESTAMP("day", 'YYYY-MM-DD HH24:MI:SS.FF')`},
		{"clob to date", "CLOB", "DATE", `TO_DATE(TO_CHAR("day"), 'YYYY-MM-DD')`},
		{"varchar to number", "VARCHAR2(50)", "NUMBER(19)", `"day"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := e.conversionSQL(old, c.oldType, c.newType); got != c.want {
				t.Errorf("conversionSQL(%q, %q) = %q, want %q", c.oldType, c.newType, got, c.want)
			}
		})
	}
}

func TestGenerateTempName(t *testing.T) {
	e := editor(t)
	got := e.generateTempName("price")
	if !strings.HasPrefix(got, "price_") {
		t.Errorf("generateTempName(%q) = %q, want it to start with the column name", "price", got)
	}
	if got == "price_" {
		t.Errorf("generateTempName(%q) = %q, want a digest after the column name", "price", got)
	}
	// The name has to be the same on every run, so that a retried
	// migration addresses the column it created before.
	if second := e.generateTempName("price"); second != got {
		t.Errorf("generateTempName is not repeatable: %q then %q", got, second)
	}
	if other := e.generateTempName("qty"); other == got {
		t.Errorf("generateTempName(%q) = generateTempName(%q) = %q", "price", "qty", got)
	}
	if long := e.generateTempName(strings.Repeat("a", 200)); len(long) > MaxNameLength {
		t.Errorf("generateTempName of a long column is %d characters, want at most %d", len(long), MaxNameLength)
	}
}

func TestOraCode(t *testing.T) {
	cases := []struct {
		name  string
		err   error
		codes []int
		want  bool
	}{
		{"nil", nil, []int{1439}, false},
		{"match", errors.New("ORA-01439: column to be modified must be empty"), []int{1439}, true},
		{"one of several", errors.New("ORA-22858: invalid alteration of datatype"), []int{1439, 22858}, true},
		{"other code", errors.New("ORA-00942: table or view does not exist"), []int{1439}, false},
		{"no code", errors.New("connection refused"), []int{1439}, false},
		// The code is five digits: ORA-1439 is not ORA-01439.
		{"short code", errors.New("ORA-1439: nope"), []int{1439}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := oraCode(c.err, c.codes...); got != c.want {
				t.Errorf("oraCode(%v, %v) = %v, want %v", c.err, c.codes, got, c.want)
			}
		})
	}
}

func TestGrammar(t *testing.T) {
	// Oracle spells a column alteration MODIFY, not ALTER COLUMN, and
	// dropping a table has to take its constraints with it or a later
	// migration could not recreate a referenced table.
	var g Grammar
	col := base.AlterColumnNullity{Column: "C", Type: "T"}
	cases := []struct{ name, got, want string }{
		{"AddColumn", g.AddColumn(base.AddColumn{Table: "T1", Column: "C", Definition: "D"}), "ALTER TABLE T1 ADD C D"},
		{"AlterColumnType", g.AlterColumnType(base.AlterColumnType{Column: "C", Type: "T"}), "MODIFY C T"},
		{"AlterColumnNull", g.AlterColumnNull(col), "MODIFY C NULL"},
		{"AlterColumnNotNull", g.AlterColumnNotNull(col), "MODIFY C NOT NULL"},
		{"AlterColumnDropDefault", g.AlterColumnDropDefault(base.AlterColumnDefault{Column: "C"}), "MODIFY C DEFAULT NULL"},
		{"DropTable", g.DropTable(base.DropTable{Table: "T1"}), "DROP TABLE T1 CASCADE CONSTRAINTS"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.got != c.want {
				t.Errorf("%s = %q, want %q", c.name, c.got, c.want)
			}
		})
	}
}

func TestCreatePrimaryKeySQL(t *testing.T) {
	// The constraint is left unnamed so that a primary key added by an
	// alteration carries the same system-generated name as one created by
	// CREATE TABLE.
	st := m.NewProjectState()
	st.AddModel(&m.ModelState{App: "shop", Name: "Item", Table: "shop_items", Fields: m.Fields{
		{Name: "id", Field: m.Field{Type: m.Int, Size: 64}},
		{Name: "code", Field: m.Field{Type: m.String}},
	}})
	mdl := st.MustApps().MustModel("shop", "Item")
	got := editor(t).CreatePrimaryKeySQL(mdl, []string{"id", "code"}).String()
	if want := `ALTER TABLE "shop_items" ADD PRIMARY KEY ("id", "code")`; got != want {
		t.Errorf("CreatePrimaryKeySQL = %q, want %q", got, want)
	}
}

func TestSequenceResetSQL(t *testing.T) {
	st := m.NewProjectState()
	st.AddModel(&m.ModelState{App: "shop", Name: "Item", Table: "shop_items", Fields: m.Fields{
		{Name: "id", Field: m.Field{Type: m.Int, Size: 64, PrimaryKey: true, AutoIncrement: true}},
		{Name: "name", Field: m.Field{Type: m.String}},
	}})
	st.AddModel(&m.ModelState{App: "shop", Name: "Tag", Table: "shop_tags", Fields: m.Fields{
		{Name: "name", Field: m.Field{Type: m.String, PrimaryKey: true}},
	}})
	models := st.MustApps().Models()

	// Oracle 23 dropped the FROM DUAL requirement of a bare SELECT.
	for _, c := range []struct{ name, version, wantSuffix string }{
		{"oracle 19", "Oracle Database 19c Release 19.0.0.0.0", " FROM DUAL"},
		{"oracle 23", "Oracle Database 23ai Free Release 23.26.3.0.0", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := SequenceResetSQL(&base.Conn{Backend: Backend, Version: c.version}, base.PlainStyle{}, models)
			if err != nil {
				t.Fatalf("SequenceResetSQL error: %v", err)
			}
			// Only the model with an auto-increment column gets a block.
			if len(got) != 1 {
				t.Fatalf("SequenceResetSQL returned %d statements, want 1", len(got))
			}
			want := fmt.Sprintf(sequenceResetSQL, "shop_items", "id", "SHOP_ITEMS_SQ", `"id"`, `"shop_items"`, c.wantSuffix)
			if got[0] != want {
				t.Errorf("SequenceResetSQL =\n%s\nwant\n%s", got[0], want)
			}
		})
	}
}

func TestColumnType(t *testing.T) {
	// user_tab_cols reports the type name and its parameters separately,
	// and the introspected type has to read back as the DDL spelling or
	// every migration would think the column changed.
	cases := []struct {
		name, dataType         string
		size, precision, scale int
		want                   string
	}{
		{"varchar2", "VARCHAR2", 50, 0, 0, "VARCHAR2(50)"},
		{"nvarchar2", "NVARCHAR2", 20, 0, 0, "NVARCHAR2(20)"},
		{"raw", "RAW", 16, 0, 0, "RAW(16)"},
		{"number without precision", "NUMBER", 22, -1, -1, "NUMBER"},
		{"integer number", "NUMBER", 22, 19, 0, "NUMBER(19)"},
		{"negative scale", "NUMBER", 22, 19, -2, "NUMBER(19)"},
		{"decimal number", "NUMBER", 22, 10, 2, "NUMBER(10,2)"},
		{"float", "FLOAT", 22, 126, 0, "FLOAT(126)"},
		{"float without precision", "FLOAT", 22, -1, 0, "FLOAT"},
		// The timestamp and interval types already carry their parameters.
		{"timestamp", "TIMESTAMP(6)", 11, -1, -1, "TIMESTAMP(6)"},
		{"clob", "CLOB", 4000, -1, -1, "CLOB"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := columnType(c.dataType, c.size, c.precision, c.scale)
			if got != c.want {
				t.Errorf("columnType(%q, %d, %d, %d) = %q, want %q",
					c.dataType, c.size, c.precision, c.scale, got, c.want)
			}
		})
	}
}

func TestSplitList(t *testing.T) {
	cases := []struct {
		name string
		in   sql.NullString
		want []string
	}{
		{"null", sql.NullString{}, nil},
		{"empty", sql.NullString{String: "", Valid: true}, nil},
		{"one", sql.NullString{String: "A", Valid: true}, []string{"A"}},
		{"several", sql.NullString{String: "A,B,C", Valid: true}, []string{"A", "B", "C"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := splitList(c.in); !slices.Equal(got, c.want) {
				t.Errorf("splitList(%q) = %q, want %q", c.in.String, got, c.want)
			}
		})
	}
}

func TestHiddenColumn(t *testing.T) {
	// The virtual columns Oracle creates for a function-based index must
	// not be reported as columns of the table.
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"hidden", "SYS_NC00004$", true},
		{"single digit", "SYS_NC1$", true},
		{"ordinary column", "NAME", false},
		{"no dollar", "SYS_NC00004", false},
		{"no digits", "SYS_NC$", false},
		{"suffixed", "SYS_NC00004$X", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := hiddenColumn.MatchString(c.in); got != c.want {
				t.Errorf("hiddenColumn.MatchString(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

func TestTransactionSQL(t *testing.T) {
	// Oracle has no statement that starts a transaction, but sqlmigrate
	// still has to end one.
	if got := (Ops{}).StartTransactionSQL(); got != "" {
		t.Errorf("StartTransactionSQL = %q, want %q", got, "")
	}
	if got, want := (Ops{}).EndTransactionSQL(), "COMMIT;"; got != want {
		t.Errorf("EndTransactionSQL = %q, want %q", got, want)
	}
}
