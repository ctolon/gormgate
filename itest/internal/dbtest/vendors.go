// Package dbtest opens isolated, throw-away databases on the servers defined
// in itest/compose.yaml (or on SQLite files) for gormgate integration tests.
//
// Every call to Open creates a brand-new database (or schema / user, or file)
// with a random name, returns a *gorm.DB connected to it and drops it again in
// t.Cleanup. Tests therefore never see each other's objects and can run in
// parallel.
package dbtest

import (
	"os"
	"sort"
	"strings"
)

// Vendor family names reported in Info.Family.
const (
	FamilyPostgreSQL  = "postgresql"
	FamilyMySQL       = "mysql"
	FamilyMariaDB     = "mariadb"
	FamilyTiDB        = "tidb"
	FamilySQLite      = "sqlite"
	FamilyMSSQL       = "mssql"
	FamilyOracle      = "oracle"
	FamilyClickHouse  = "clickhouse"
	FamilyCockroachDB = "cockroachdb"
	FamilyGaussDB     = "gaussdb"
)

// Env vars understood by the package.
const (
	// EnvRequire turns "server unreachable" from a skip into a test failure.
	EnvRequire = "GORMGATE_ITEST_REQUIRE"
	// EnvSQLLog set to a non-empty value logs every SQL statement via t.Log.
	EnvSQLLog = "GORMGATE_ITEST_SQL_LOG"
)

// Vendor describes one supported server (or embedded engine) flavour.
type Vendor struct {
	// Key is the vendor key used by Open, e.g. "pg14" or "sqlite-purego".
	Key string
	// Family is the vendor family, one of the Family* constants.
	Family string
	// Service is the compose.yaml service providing the server ("" for SQLite).
	Service string
	// DefaultDSN is the admin DSN used when the env var is unset. It must
	// connect as a user allowed to create and drop databases (or, for
	// Oracle, users). For SQLite it is a template in which "{path}" is
	// replaced by the path of a fresh database file.
	DefaultDSN string
}

var vendors = map[string]Vendor{
	"pg14": {Key: "pg14", Family: FamilyPostgreSQL, Service: "pg14",
		DefaultDSN: "postgres://gormgate:gormgate@127.0.0.1:15414/postgres?sslmode=disable&connect_timeout=5"},
	"pg18": {Key: "pg18", Family: FamilyPostgreSQL, Service: "pg18",
		DefaultDSN: "postgres://gormgate:gormgate@127.0.0.1:15418/postgres?sslmode=disable&connect_timeout=5"},
	"mysql80": {Key: "mysql80", Family: FamilyMySQL, Service: "mysql80",
		DefaultDSN: "root:gormgate@tcp(127.0.0.1:13380)/?parseTime=true&timeout=5s"},
	"mysql84": {Key: "mysql84", Family: FamilyMySQL, Service: "mysql84",
		DefaultDSN: "root:gormgate@tcp(127.0.0.1:13384)/?parseTime=true&timeout=5s"},
	"mariadb106": {Key: "mariadb106", Family: FamilyMariaDB, Service: "mariadb106",
		DefaultDSN: "root:gormgate@tcp(127.0.0.1:13406)/?parseTime=true&timeout=5s"},
	"mariadb118": {Key: "mariadb118", Family: FamilyMariaDB, Service: "mariadb118",
		DefaultDSN: "root:gormgate@tcp(127.0.0.1:13418)/?parseTime=true&timeout=5s"},
	"tidb": {Key: "tidb", Family: FamilyTiDB, Service: "tidb",
		DefaultDSN: "root@tcp(127.0.0.1:14000)/?parseTime=true&timeout=5s"},
	"cockroach": {Key: "cockroach", Family: FamilyCockroachDB, Service: "cockroach",
		DefaultDSN: "postgres://root@127.0.0.1:15426/defaultdb?sslmode=disable&connect_timeout=5"},
	"mssql": {Key: "mssql", Family: FamilyMSSQL, Service: "mssql",
		DefaultDSN: "sqlserver://sa:GormGate-Pass123@127.0.0.1:14330?database=master&dial+timeout=5"},
	"oracle": {Key: "oracle", Family: FamilyOracle, Service: "oracle",
		DefaultDSN: `user="gormgate" password="gormgate" connectString="127.0.0.1:15210/FREEPDB1"`},
	"gauss": {Key: "gauss", Family: FamilyGaussDB, Service: "gauss",
		DefaultDSN: "gaussdb://gormgate:Gormgate-123@127.0.0.1:15432/postgres?sslmode=disable&connect_timeout=5"},
	"clickhouse": {Key: "clickhouse", Family: FamilyClickHouse, Service: "clickhouse",
		DefaultDSN: "clickhouse://gormgate:gormgate@127.0.0.1:19000/default?dial_timeout=5s"},
	"sqlite": {Key: "sqlite", Family: FamilySQLite,
		DefaultDSN: "file:{path}?_foreign_keys=1&_busy_timeout=5000"},
	"sqlite-purego": {Key: "sqlite-purego", Family: FamilySQLite,
		DefaultDSN: "file:{path}?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"},
}

// Keys returns all vendor keys in sorted order.
func Keys() []string {
	keys := make([]string, 0, len(vendors))
	for k := range vendors {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Lookup returns the vendor registered under key.
func Lookup(key string) (Vendor, bool) {
	v, ok := vendors[key]
	return v, ok
}

// DSNEnvVar returns the env var that overrides the admin DSN of key,
// e.g. "GORMGATE_ITEST_SQLITE_PUREGO_DSN" for "sqlite-purego".
func DSNEnvVar(key string) string {
	return "GORMGATE_ITEST_" + strings.ToUpper(strings.ReplaceAll(key, "-", "_")) + "_DSN"
}

// DSN returns the admin DSN for the vendor: the env override or the default.
func (v Vendor) DSN() string {
	if s := os.Getenv(DSNEnvVar(v.Key)); s != "" {
		return s
	}
	return v.DefaultDSN
}
