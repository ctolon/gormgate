package mysql

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ctolon/gormgate/backends/base"
	m "github.com/ctolon/gormgate/migrations"
)

func TestVersionAtLeast(t *testing.T) {
	cases := []struct {
		name            string
		v               Version
		major, minor, p int
		want            bool
	}{
		{"equal", Version{8, 0, 11}, 8, 0, 11, true},
		{"patch below", Version{8, 0, 10}, 8, 0, 11, false},
		{"patch above", Version{8, 0, 12}, 8, 0, 11, true},
		// The comparison has to be lexicographic over the three numbers,
		// not numeric on a single one: 8.1.0 is newer than 8.0.11 even
		// though 1 < 11.
		{"higher minor, lower patch", Version{8, 1, 0}, 8, 0, 11, true},
		{"lower minor, higher patch", Version{8, 0, 99}, 8, 1, 0, false},
		{"higher major", Version{10, 6, 0}, 8, 0, 11, true},
		{"lower major", Version{5, 7, 44}, 8, 0, 11, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.v.AtLeast(c.major, c.minor, c.p); got != c.want {
				t.Errorf("%s.AtLeast(%d, %d, %d) = %v, want %v", c.v, c.major, c.minor, c.p, got, c.want)
			}
		})
	}
}

func TestVersionString(t *testing.T) {
	if got, want := (Version{10, 6, 21}).String(), "10.6.21"; got != want {
		t.Errorf("Version.String = %q, want %q", got, want)
	}
}

func TestParseVersion(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want Version
		ok   bool
	}{
		{"plain", "8.0.41", Version{8, 0, 41}, true},
		{"mysql suffix", "8.4.3-MySQL Community Server - GPL", Version{8, 4, 3}, true},
		{"mariadb", "10.6.21-MariaDB-ubu2004", Version{10, 6, 21}, true},
		{"tidb", "8.0.11-TiDB-v8.5.1", Version{8, 0, 11}, true},
		{"no version", "MariaDB", Version{}, false},
		// Two digits per part is the whole pattern; a three-digit part
		// would be read as two digits and leave the rest behind, so a
		// version that cannot be read has to say so rather than round.
		{"two digits", "11.4.5", Version{11, 4, 5}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := ParseVersion(c.in)
			if ok != c.ok || got != c.want {
				t.Errorf("ParseVersion(%q) = %v, %v, want %v, %v", c.in, got, ok, c.want, c.ok)
			}
		})
	}
}

func TestQuoteName(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"plain", "t", "`t`"},
		{"already quoted", "`t`", "`t`"},
		// A backtick inside a name is doubled, so that a column named a`b
		// cannot end its own quoting.
		{"embedded backtick", "a`b", "`a``b`"},
		// A dot is part of the name, not a qualifier: gorm's mysql
		// dialector quotes the whole string.
		{"dotted", "db.t", "`db.t`"},
		{"empty", "", "``"},
		// A single backtick is not a quoted name, so it gets quoted.
		{"lone backtick", "`", "````"},
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
		// MySQL's client library escapes with backslashes, not by
		// doubling the quote.
		{"quote", "a'b", `'a\'b'`},
		{"backslash", `a\b`, `'a\\b'`},
		{"double quote", `a"b`, `'a\"b'`},
		{"newline", "a\nb", `'a\nb'`},
		{"nul", "a\x00b", `'a\0b'`},
		{"true", true, "1"},
		{"false", false, "0"},
		{"nil", nil, "NULL"},
		{"int", 42, "42"},
		{"time", time.Date(2024, 3, 1, 12, 0, 0, 500000000, time.UTC), "'2024-03-01 12:00:00.5'"},
		{"bytes", []byte{0xde, 0xad}, "X'dead'"},
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
	// A prompt-entered default that is Go source, not a value, must be
	// reported rather than pasted into the statement as SQL.
	_, err := (Ops{}).QuoteValue(&m.GoExpr{Source: "time.Now()"})
	if err == nil {
		t.Fatal("QuoteValue of a GoExpr returned no error")
	}
	if !strings.Contains(err.Error(), "time.Now()") || !strings.Contains(err.Error(), "makemigrations") {
		t.Errorf("QuoteValue of a GoExpr error = %q, want it to name the source and makemigrations", err)
	}
}

func TestFeaturesFor(t *testing.T) {
	mysql8011 := Server{Version: Version{8, 0, 11}, StorageEngine: "InnoDB"}
	mysql8016 := Server{Version: Version{8, 0, 16}, StorageEngine: "InnoDB"}
	mariadb := Server{MariaDB: true, Version: Version{10, 6, 21}, StorageEngine: "InnoDB"}
	mariadb108 := Server{MariaDB: true, Version: Version{10, 8, 0}, StorageEngine: "InnoDB"}
	myisam := Server{Version: Version{8, 4, 0}, StorageEngine: "MyISAM"}

	cases := []struct {
		name string
		s    Server
		get  func(base.Features) bool
		want bool
	}{
		{"MyISAM has no transactions", myisam, func(f base.Features) bool { return f.SupportsTransactions }, false},
		{"InnoDB has transactions", mysql8011, func(f base.Features) bool { return f.SupportsTransactions }, true},
		{"MyISAM has no foreign keys", myisam, func(f base.Features) bool { return f.SupportsForeignKeys }, false},
		{"checks need 8.0.16", mysql8011, func(f base.Features) bool { return f.SupportsTableCheckConstraints }, false},
		{"checks on 8.0.16", mysql8016, func(f base.Features) bool { return f.SupportsTableCheckConstraints }, true},
		{"MariaDB always has checks", mariadb, func(f base.Features) bool { return f.SupportsTableCheckConstraints }, true},
		{"MariaDB has no expression indexes", mariadb108, func(f base.Features) bool { return f.SupportsExpressionIndexes }, false},
		{"MySQL 8.0.16 has expression indexes", mysql8016, func(f base.Features) bool { return f.SupportsExpressionIndexes }, true},
		{"MyISAM has no expression indexes", myisam, func(f base.Features) bool { return f.SupportsExpressionIndexes }, false},
		{"MariaDB 10.6 has no index ordering", mariadb, func(f base.Features) bool { return f.SupportsIndexColumnOrdering }, false},
		{"MariaDB 10.8 has index ordering", mariadb108, func(f base.Features) bool { return f.SupportsIndexColumnOrdering }, true},
		{"MyISAM has no index ordering", myisam, func(f base.Features) bool { return f.SupportsIndexColumnOrdering }, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.get(FeaturesFor(c.s)); got != c.want {
				t.Errorf("FeaturesFor(%+v) = %v, want %v", c.s, got, c.want)
			}
		})
	}
}

func TestFeaturesForIgnoresTableNameCase(t *testing.T) {
	for _, lower := range []int{0, 1, 2} {
		s := Server{Version: Version{8, 4, 0}, StorageEngine: "InnoDB", LowerCaseTableNames: lower}
		want := lower != 0
		if got := FeaturesFor(s).IgnoresTableNameCase; got != want {
			t.Errorf("lower_case_table_names = %d: IgnoresTableNameCase = %v, want %v", lower, got, want)
		}
	}
}

func TestCheckMinimumVersion(t *testing.T) {
	cases := []struct {
		name string
		s    Server
		want string
	}{
		{"mysql ok", Server{Version: Version{8, 0, 11}}, ""},
		{"mysql too old", Server{Version: Version{5, 7, 44}}, "MySQL 8.0.11 or later is required (found 5.7.44)"},
		{"mysql patch too old", Server{Version: Version{8, 0, 10}}, "MySQL 8.0.11 or later is required (found 8.0.10)"},
		{"mariadb ok", Server{MariaDB: true, Version: Version{10, 6, 0}}, ""},
		{"mariadb too old", Server{MariaDB: true, Version: Version{10, 5, 9}}, "MariaDB 10.6 or later is required (found 10.5.9)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := CheckMinimumVersion(c.s)
			if c.want == "" {
				if err != nil {
					t.Fatalf("CheckMinimumVersion(%+v) = %v, want nil", c.s, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("CheckMinimumVersion(%+v) = nil, want %q", c.s, c.want)
			}
			var nse *m.NotSupportedError
			if !errors.As(err, &nse) {
				t.Errorf("CheckMinimumVersion(%+v) error is %T, want *migrations.NotSupportedError", c.s, err)
			}
			if err.Error() != c.want {
				t.Errorf("CheckMinimumVersion(%+v) = %q, want %q", c.s, err, c.want)
			}
		})
	}
}

func TestSupportsExpressionDefaults(t *testing.T) {
	cases := []struct {
		name string
		s    Server
		want bool
	}{
		{"mysql 8.0.12", Server{Version: Version{8, 0, 12}}, false},
		{"mysql 8.0.13", Server{Version: Version{8, 0, 13}}, true},
		{"mariadb 10.6", Server{MariaDB: true, Version: Version{10, 6, 0}}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := SupportsExpressionDefaults(c.s); got != c.want {
				t.Errorf("SupportsExpressionDefaults(%+v) = %v, want %v", c.s, got, c.want)
			}
		})
	}
}

func TestCanIntrospectCheckConstraints(t *testing.T) {
	cases := []struct {
		name string
		s    Server
		want bool
	}{
		{"mysql 8.0.15", Server{Version: Version{8, 0, 15}}, false},
		{"mysql 8.0.16", Server{Version: Version{8, 0, 16}}, true},
		{"mariadb 10.6", Server{MariaDB: true, Version: Version{10, 6, 0}}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := CanIntrospectCheckConstraints(c.s); got != c.want {
				t.Errorf("CanIntrospectCheckConstraints(%+v) = %v, want %v", c.s, got, c.want)
			}
		})
	}
}

func TestBackendFor(t *testing.T) {
	my := Server{Version: Version{8, 4, 0}, StorageEngine: "InnoDB"}
	maria := Server{MariaDB: true, Version: Version{10, 6, 21}, StorageEngine: "InnoDB"}
	if got, want := BackendFor(my).DisplayName, "MySQL"; got != want {
		t.Errorf("BackendFor(mysql).DisplayName = %q, want %q", got, want)
	}
	if got, want := BackendFor(maria).DisplayName, "MariaDB"; got != want {
		t.Errorf("BackendFor(mariadb).DisplayName = %q, want %q", got, want)
	}
	// MariaDB and MySQL differ only in a flag of the key, so a cache that
	// ignored part of the key would hand out the wrong backend.
	if BackendFor(my) == BackendFor(maria) {
		t.Error("BackendFor returned the same backend for MySQL and MariaDB")
	}
	// The same server must come back from the cache, not be rebuilt:
	// callers compare backend pointers.
	if first, again := BackendFor(my), BackendFor(my); first != again {
		t.Error("BackendFor returned a different backend for the same server")
	}
	if got, want := BackendFor(my).Vendor, base.MySQL; got != want {
		t.Errorf("BackendFor(mysql).Vendor = %q, want %q", got, want)
	}
}

func TestIsMariaDBIsTiDB(t *testing.T) {
	// The three detectors share the mysql dialector and pick each other
	// apart by these two predicates alone.
	cases := []struct {
		name          string
		version       string
		maria, tidb   bool
		wantDetectors string
	}{
		{"mysql", "8.4.3", false, false, "mysql"},
		{"mariadb", "10.6.21-MariaDB-ubu2004", true, false, "mariadb"},
		{"tidb", "8.0.11-TiDB-v8.5.1", false, true, "tidb"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IsMariaDB(c.version); got != c.maria {
				t.Errorf("IsMariaDB(%q) = %v, want %v", c.version, got, c.maria)
			}
			if got := IsTiDB(c.version); got != c.tidb {
				t.Errorf("IsTiDB(%q) = %v, want %v", c.version, got, c.tidb)
			}
		})
	}
}

func TestStripNullSuffix(t *testing.T) {
	// gorm's mysql dialector spells a nullable datetime "datetime(3) NULL";
	// the editor has to take that suffix off before it sets the
	// nullability itself, but must never touch a NOT NULL type or a type
	// whose name merely ends in "null".
	cases := []struct{ name, in, want string }{
		{"null suffix", "datetime(3) NULL", "datetime(3)"},
		{"lower case", "datetime(3) null", "datetime(3)"},
		{"not null kept", "datetime(3) NOT NULL", "datetime(3) NOT NULL"},
		{"trailing space", "datetime(3) NULL  ", "datetime(3)"},
		{"no suffix", "varchar(191)", "varchar(191)"},
		{"word ending in null", "isnull", "isnull"},
		{"exactly NULL", "NULL", "NULL"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := stripNullSuffix(c.in); got != c.want {
				t.Errorf("stripNullSuffix(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestDefaultIsEmpty(t *testing.T) {
	field := func(def any) *m.ModelField {
		return &m.ModelField{Name: "c", Column: "c", Field: m.Field{Type: m.String, Default: def}}
	}
	cases := []struct {
		name string
		f    *m.ModelField
		want bool
	}{
		{"empty string", field(""), true},
		{"non-empty string", field("x"), false},
		{"empty bytes", field([]byte{}), true},
		{"non-empty bytes", field([]byte{1}), false},
		{"no default", field(nil), false},
		{"zero int", field(0), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := defaultIsEmpty(c.f); got != c.want {
				t.Errorf("defaultIsEmpty(%v) = %v, want %v", c.f.Field.Default, got, c.want)
			}
		})
	}
}

func TestGrammarCheckDrop(t *testing.T) {
	// A MariaDB column check constraint disappears with the MODIFY that
	// rewrites its column, so its drop has to tolerate it being gone;
	// MySQL spells the drop differently altogether.
	maria, my := Grammar{MariaDB: true}, Grammar{}
	c := base.DropConstraint{Table: "T", Name: "N"}
	if want := "ALTER TABLE T DROP CONSTRAINT IF EXISTS N"; maria.DropCheck(c) != want {
		t.Errorf("MariaDB DropCheck = %q, want %q", maria.DropCheck(c), want)
	}
	if want := "ALTER TABLE T DROP CHECK N"; my.DropCheck(c) != want {
		t.Errorf("MySQL DropCheck = %q, want %q", my.DropCheck(c), want)
	}
	// A column comment is part of the column definition, so there is no
	// separate statement for it.
	if got := (Grammar{}).ColumnComment(base.ColumnComment{Table: "T", Column: "C", Comment: "'x'"}); got != "" {
		t.Errorf("ColumnComment = %q, want %q", got, "")
	}
	if want := "ALTER TABLE T DROP FOREIGN KEY N"; my.DropForeignKey(c) != want {
		t.Errorf("DropForeignKey = %q, want %q", my.DropForeignKey(c), want)
	}
}

func TestParseConstraintColumns(t *testing.T) {
	columns := map[string]bool{"price": true, "discount": true}
	cases := []struct {
		name  string
		check string
		want  []string
	}{
		{"one column", "(`price` > 0)", []string{"price"}},
		{"two columns in order", "(`discount` < `price`)", []string{"discount", "price"}},
		// A column must not be listed twice, and a quoted name that is not
		// a column of the table (a function name, another table's column)
		// is not one of its columns.
		{"repetition dropped", "(`price` > 0 and `price` < 100)", []string{"price"}},
		{"unknown name dropped", "(`other` > 0)", nil},
		{"unquoted names ignored", "(price > 0)", nil},
		{"empty", "", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := parseConstraintColumns(c.check, columns); !slices.Equal(got, c.want) {
				t.Errorf("parseConstraintColumns(%q) = %q, want %q", c.check, got, c.want)
			}
		})
	}
}

func TestParseConstraintColumnsDoubledBacktick(t *testing.T) {
	// A backtick inside a column name is doubled in the check clause and
	// has to be folded back before the name is matched.
	columns := map[string]bool{"a`b": true}
	got := parseConstraintColumns("(`a``b` > 0)", columns)
	if want := []string{"a`b"}; !slices.Equal(got, want) {
		t.Errorf("parseConstraintColumns = %q, want %q", got, want)
	}
}

func TestNewIntrospectionCheckQuery(t *testing.T) {
	// The query differs because MySQL's information_schema.check_constraints
	// has no table_name column while MariaDB's has, and a server too old to
	// report check constraints at all must not be asked.
	cases := []struct {
		name string
		s    Server
		want string
	}{
		{"mysql 8.0.16", Server{Version: Version{8, 0, 16}}, mysqlCheckQuery},
		{"mariadb", Server{MariaDB: true, Version: Version{10, 6, 0}}, mariaDBCheckQuery},
		{"mysql 8.0.15", Server{Version: Version{8, 0, 15}}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := NewIntrospection(&base.Conn{Backend: BackendFor(c.s)}, c.s).CheckConstraintQuery
			if got != c.want {
				t.Errorf("CheckConstraintQuery = %q, want %q", got, c.want)
			}
		})
	}
}

func TestSequenceResetSQL(t *testing.T) {
	// MySQL's AUTO_INCREMENT counter follows the rows of its own table, so
	// sqlsequencereset has nothing to print.
	got, err := SequenceResetSQL(nil, base.PlainStyle{}, nil)
	if err != nil || got != nil {
		t.Errorf("SequenceResetSQL = %q, %v, want nil, nil", got, err)
	}
}
