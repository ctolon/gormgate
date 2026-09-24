// Package mysql is the MySQL and MariaDB backend (gorm.io/driver/mysql,
// dialector "mysql").
//
// One package serves both servers because their DDL is the same. What is
// not the same -- CHECK constraints, functional indexes, index column
// ordering, whether table names fold to lower case, whether the storage
// engine has transactions at all -- depends on the server, its version and
// its settings, so this backend has no fixed Features: its detectors
// Resolve a Backend per connection from a Server value, as Django computes
// those flags lazily from connection.mysql_version and mysql_server_data.
// backends/tidb reuses the editor, introspection and operations here and
// replaces only what TiDB does differently.
//
// django: db/backends/mysql/
package mysql

import (
	"database/sql"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ctolon/gormgate/backends/base"
	m "github.com/ctolon/gormgate/migrations"
)

// MaxNameLength is MySQL's identifier length limit.
//
// django: mysql/operations.py DatabaseOperations.max_name_length
const MaxNameLength = 64

// Version is a (major, minor, patch) server version.
type Version [3]int

// AtLeast reports whether v >= (major, minor, patch).
func (v Version) AtLeast(major, minor, patch int) bool {
	want := Version{major, minor, patch}
	return slices.Compare(v[:], want[:]) >= 0
}

// String renders the version as "major.minor.patch".
func (v Version) String() string { return fmt.Sprintf("%d.%d.%d", v[0], v[1], v[2]) }

var versionRe = regexp.MustCompile(`(\d{1,2})\.(\d{1,2})\.(\d{1,2})`)

// ParseVersion extracts the first x.y.z found in a version string.
//
// django: mysql/base.py DatabaseWrapper.mysql_version (server_version_re)
func ParseVersion(s string) (Version, bool) {
	mt := versionRe.FindStringSubmatch(s)
	if mt == nil {
		return Version{}, false
	}
	var v Version
	for i := range v {
		v[i], _ = strconv.Atoi(mt[i+1])
	}
	return v, true
}

// Server describes the connected server: Django's mysql_is_mariadb,
// mysql_version and the mysql_server_data entries the features read.
type Server struct {
	// MariaDB is Django's connection.mysql_is_mariadb.
	MariaDB bool
	// Version is mysql_version: for MariaDB the MariaDB version, for TiDB
	// the MySQL version TiDB reports compatibility with.
	Version Version
	// LowerCaseTableNames is @@lower_case_table_names.
	LowerCaseTableNames int
	// StorageEngine is @@default_storage_engine.
	StorageEngine string
}

// ReadServer queries the settings Django keeps in mysql_server_data.
//
// django: mysql/base.py DatabaseWrapper.mysql_server_data
func ReadServer(c *base.Conn, version string) (Server, error) {
	v, ok := ParseVersion(version)
	if !ok {
		return Server{}, fmt.Errorf("gormgate: cannot determine the MySQL version from %q", version)
	}
	s := Server{MariaDB: strings.Contains(version, "MariaDB"), Version: v}
	var lower sql.NullInt64
	var engine sql.NullString
	if err := c.DB().Raw("SELECT @@lower_case_table_names, @@default_storage_engine").Row().Scan(&lower, &engine); err != nil {
		return Server{}, fmt.Errorf("gormgate: reading the MySQL server settings: %w", err)
	}
	s.LowerCaseTableNames = int(lower.Int64)
	s.StorageEngine = engine.String
	return s, nil
}

// FeaturesFor computes Django's DatabaseFeatures values for a server.
//
// django: db/backends/mysql/features.py DatabaseFeatures
func FeaturesFor(s Server) base.Features {
	innodb := strings.EqualFold(s.StorageEngine, "InnoDB")
	myisam := strings.EqualFold(s.StorageEngine, "MyISAM")
	f := base.Features{
		// All storage engines except MyISAM support transactions.
		SupportsTransactions: !myisam,
		// Every DDL statement commits implicitly.
		CanRollbackDDL:         false,
		SupportsCombinedAlters: false,
		SupportsForeignKeys:    !myisam,
		// Neither MySQL nor MariaDB support partial indexes.
		SupportsPartialIndexes:                 false,
		SupportsCoveringIndexes:                false,
		SupportsDeferrableUniqueConstraints:    false,
		SupportsNullsDistinctUniqueConstraints: false,
		SupportsUniqueConstraints:              true,
		SupportsComments:                       true,
		SupportsCommentsInline:                 true,
		CanRenameIndex:                         true,
		AllowsMultipleConstraintsOnSameFields:  true,
		IgnoresTableNameCase:                   s.LowerCaseTableNames != 0,
	}
	if s.MariaDB {
		f.SupportsTableCheckConstraints = true
		f.SupportsIndexColumnOrdering = innodb && s.Version.AtLeast(10, 8, 0)
		// MariaDB has no functional indexes.
		f.SupportsExpressionIndexes = false
	} else {
		f.SupportsTableCheckConstraints = s.Version.AtLeast(8, 0, 16)
		f.SupportsIndexColumnOrdering = innodb
		f.SupportsExpressionIndexes = !myisam && s.Version.AtLeast(8, 0, 13)
	}
	return f
}

// SupportsExpressionDefaults reports whether DEFAULT (expression) is
// accepted, which is also what makes defaults possible on TEXT and BLOB
// columns.
//
// django: mysql/features.py DatabaseFeatures.supports_expression_defaults
func SupportsExpressionDefaults(s Server) bool {
	return s.MariaDB || s.Version.AtLeast(8, 0, 13)
}

// CanIntrospectCheckConstraints reports whether check constraints show up
// in information_schema.
//
// django: mysql/features.py DatabaseFeatures.can_introspect_check_constraints
func CanIntrospectCheckConstraints(s Server) bool {
	return s.MariaDB || s.Version.AtLeast(8, 0, 16)
}

// Ops are MySQL's DatabaseOperations helpers.
type Ops struct{}

// QuoteName quotes an identifier with backticks.
//
// django: mysql/operations.py DatabaseOperations.quote_name
func (Ops) QuoteName(name string) string {
	if len(name) >= 2 && strings.HasPrefix(name, "`") && strings.HasSuffix(name, "`") {
		return name // Quoting once is enough.
	}
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}

// stringEscaper escapes a string literal the way the MySQL client library
// does (the server's default sql_mode, i.e. without NO_BACKSLASH_ESCAPES).
var stringEscaper = strings.NewReplacer(
	`\`, `\\`,
	`'`, `\'`,
	`"`, `\"`,
	"\x00", `\0`,
	"\n", `\n`,
	"\r", `\r`,
	"\x1a", `\Z`,
)

// QuoteString renders a MySQL string literal.
func QuoteString(s string) string { return "'" + stringEscaper.Replace(s) + "'" }

// QuoteValue renders a literal the way MySQLdb's escape() does.
//
// django: mysql/schema.py DatabaseSchemaEditor.quote_value
func (Ops) QuoteValue(v any) (string, error) {
	switch x := v.(type) {
	case *m.GoExpr:
		// A prompt-entered Go expression is not a value; the fallthrough
		// to base.StandardQuoteValue reports it. It has to be matched
		// before fmt.Stringer, which a GoExpr satisfies, or its source
		// would be written out as a string literal.
	case string:
		return QuoteString(x), nil
	case time.Time:
		return QuoteString(x.Format("2006-01-02 15:04:05.999999")), nil
	case fmt.Stringer:
		return QuoteString(x.String()), nil
	}
	return base.StandardQuoteValue(v, base.QuoteValueOptions{
		True: "1", False: "0", TimeFormat: "2006-01-02 15:04:05.999999",
	})
}

// SplitOptions are MySQL's lexical rules: backslash escapes inside string
// literals and backtick-quoted identifiers, and no dollar quoting ($ is an
// ordinary identifier character).
var SplitOptions = base.SplitOptions{BackslashEscapes: true, BacktickQuotes: true}

// PrepareSQLScript splits a script into statements.
func (Ops) PrepareSQLScript(script string) []string { return base.SplitSQL(script, SplitOptions) }

// StartTransactionSQL is used by sqlmigrate.
func (Ops) StartTransactionSQL() string { return "BEGIN;" }

// EndTransactionSQL is used by sqlmigrate.
func (Ops) EndTransactionSQL() string { return "COMMIT;" }

// SequenceResetSQL returns no statements: MySQL's DatabaseOperations does
// not override sequence_reset_sql (an AUTO_INCREMENT counter follows the
// rows of its own table), so sqlsequencereset prints nothing.
//
// django: base/operations.py BaseDatabaseOperations.sequence_reset_sql
func SequenceResetSQL(*base.Conn, base.Style, []*m.Model) ([]string, error) {
	return nil, nil
}

var backends sync.Map // Server -> *base.Backend

// BackendFor returns the (cached) backend of a MySQL or MariaDB server.
func BackendFor(s Server) *base.Backend {
	if b, ok := backends.Load(s); ok {
		return b.(*base.Backend)
	}
	display := "MySQL"
	if s.MariaDB {
		display = "MariaDB"
	}
	b := &base.Backend{
		Vendor:        "mysql",
		DisplayName:   display,
		Features:      FeaturesFor(s),
		MaxNameLength: MaxNameLength,
		NewEditor: func(c *base.Conn, collect, atomic bool) base.SchemaEditor {
			return NewEditor(c, s, collect, atomic)
		},
		NewIntrospection: func(c *base.Conn) base.Introspection { return NewIntrospection(c, s) },
		SequenceResetSQL: SequenceResetSQL,
		Ops:              Ops{},
	}
	actual, _ := backends.LoadOrStore(s, b)
	return actual.(*base.Backend)
}

// CheckMinimumVersion rejects servers older than the ones Django supports.
//
// django: mysql/features.py DatabaseFeatures.minimum_database_version
func CheckMinimumVersion(s Server) error {
	if s.MariaDB {
		if !s.Version.AtLeast(10, 6, 0) {
			return &m.NotSupportedError{Msg: fmt.Sprintf("MariaDB 10.6 or later is required (found %s)", s.Version)}
		}
		return nil
	}
	if !s.Version.AtLeast(8, 0, 11) {
		return &m.NotSupportedError{Msg: fmt.Sprintf("MySQL 8.0.11 or later is required (found %s)", s.Version)}
	}
	return nil
}

func resolve(c *base.Conn, version string) (*base.Backend, error) {
	s, err := ReadServer(c, version)
	if err != nil {
		return nil, err
	}
	if err := CheckMinimumVersion(s); err != nil {
		return nil, err
	}
	return BackendFor(s), nil
}

// VersionQuery is the statement every MySQL-family detector reads the
// server version with.
const VersionQuery = "SELECT VERSION()"

// IsMariaDB reports whether a version string comes from MariaDB.
func IsMariaDB(v string) bool { return strings.Contains(v, "MariaDB") }

// IsTiDB reports whether a version string comes from TiDB.
func IsTiDB(v string) bool { return strings.Contains(v, "TiDB") }

func init() {
	base.Register(base.Detector{
		Name:         "mariadb",
		Priority:     base.PriorityDerived,
		Dialector:    "mysql",
		VersionQuery: VersionQuery,
		Match:        func(v string) bool { return IsMariaDB(v) && !IsTiDB(v) },
		Resolve:      resolve,
	})
	base.Register(base.Detector{
		Name:         "mysql",
		Priority:     base.PriorityFamily,
		Dialector:    "mysql",
		VersionQuery: VersionQuery,
		Match:        func(v string) bool { return !IsMariaDB(v) && !IsTiDB(v) },
		Resolve:      resolve,
	})
}
