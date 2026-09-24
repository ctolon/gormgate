//go:build integration

package itest

import (
	"errors"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/ctolon/gormgate/itest/internal/dbtest"
)

// smokeSpec holds the vendor-specific SQL of the smoke test.
type smokeSpec struct {
	// createItems creates table smoke_items(id, name, price, active, created_at).
	createItems string
	// trueLit is the literal stored in the boolean column.
	trueLit string
	// createRollback is the DDL executed (and rolled back) inside a transaction.
	createRollback string
	// createCheck creates table smoke_check with a CHECK (qty > 0) constraint.
	createCheck string
	// versionMarker must appear in the server version string ("" = no check).
	versionMarker string
	// ddlRollback is whether rolling back a transaction undoes a CREATE TABLE
	// on this server with default session settings.
	ddlRollback bool
}

const (
	pgItems    = `CREATE TABLE smoke_items (id BIGINT PRIMARY KEY, name VARCHAR(100) NOT NULL, price NUMERIC(10,2), active BOOLEAN, created_at TIMESTAMP)`
	pgRollback = `CREATE TABLE smoke_ddl_rb (id INT)`
	pgCheck    = `CREATE TABLE smoke_check (id INT PRIMARY KEY, qty INT CHECK (qty > 0))`
	myItems    = `CREATE TABLE smoke_items (id BIGINT PRIMARY KEY, name VARCHAR(100) NOT NULL, price DECIMAL(10,2), active BOOLEAN, created_at DATETIME(6))`
)

var smokeSpecs = map[string]smokeSpec{
	dbtest.FamilyPostgreSQL: {createItems: pgItems, trueLit: "TRUE", createRollback: pgRollback, createCheck: pgCheck,
		versionMarker: "PostgreSQL", ddlRollback: true},
	dbtest.FamilyGaussDB: {createItems: pgItems, trueLit: "TRUE", createRollback: pgRollback, createCheck: pgCheck,
		versionMarker: "openGauss", ddlRollback: true},
	// CockroachDB 25.2 defaults autocommit_before_ddl to on: a DDL statement
	// inside an explicit transaction first commits that transaction and then
	// runs in its own implicit transaction, so ROLLBACK cannot undo it. With
	// autocommit_before_ddl = off the DDL is transactional (see
	// testCockroachTransactionalDDL).
	dbtest.FamilyCockroachDB: {createItems: pgItems, trueLit: "TRUE", createRollback: pgRollback, createCheck: pgCheck,
		versionMarker: "CockroachDB", ddlRollback: false},
	dbtest.FamilyMySQL: {createItems: myItems, trueLit: "TRUE", createRollback: pgRollback, createCheck: pgCheck,
		ddlRollback: false},
	dbtest.FamilyMariaDB: {createItems: myItems, trueLit: "TRUE", createRollback: pgRollback, createCheck: pgCheck,
		versionMarker: "MariaDB", ddlRollback: false},
	dbtest.FamilyTiDB: {createItems: myItems, trueLit: "TRUE", createRollback: pgRollback, createCheck: pgCheck,
		versionMarker: "TiDB", ddlRollback: false},
	dbtest.FamilyMSSQL: {
		createItems:    `CREATE TABLE smoke_items (id BIGINT PRIMARY KEY, name NVARCHAR(100) NOT NULL, price DECIMAL(10,2), active BIT, created_at DATETIME2)`,
		trueLit:        "1",
		createRollback: pgRollback, createCheck: pgCheck,
		versionMarker: "Microsoft SQL Server", ddlRollback: true},
	dbtest.FamilyOracle: {
		createItems:    `CREATE TABLE smoke_items (id NUMBER(19) PRIMARY KEY, name VARCHAR2(100) NOT NULL, price NUMBER(10,2), active NUMBER(1), created_at TIMESTAMP)`,
		trueLit:        "1",
		createRollback: `CREATE TABLE smoke_ddl_rb (id NUMBER(10))`,
		createCheck:    `CREATE TABLE smoke_check (id NUMBER(10) PRIMARY KEY, qty NUMBER(10) CHECK (qty > 0))`,
		versionMarker:  "Oracle", ddlRollback: false},
	dbtest.FamilyClickHouse: {
		createItems:    `CREATE TABLE smoke_items (id Int64, name String, price Decimal(10,2), active Bool, created_at DateTime) ENGINE = MergeTree ORDER BY id`,
		trueLit:        "true",
		createRollback: `CREATE TABLE smoke_ddl_rb (id Int32) ENGINE = Memory`,
		createCheck:    `CREATE TABLE smoke_check (id Int32, qty Int32, CONSTRAINT qty_positive CHECK qty > 0) ENGINE = MergeTree ORDER BY id`,
		ddlRollback:    false},
	dbtest.FamilySQLite: {
		createItems:    `CREATE TABLE smoke_items (id INTEGER PRIMARY KEY, name TEXT NOT NULL, price NUMERIC, active BOOLEAN, created_at DATETIME)`,
		trueLit:        "1",
		createRollback: pgRollback, createCheck: pgCheck,
		ddlRollback: true},
}

// TestSmoke checks, for every vendor key, that dbtest hands out a working
// isolated database and records how the server treats DDL in transactions.
func TestSmoke(t *testing.T) {
	for _, key := range vendorsUnderTest(t) {
		t.Run(key, func(t *testing.T) {
			t.Parallel()
			db, info := dbtest.Open(t, key)
			spec, ok := smokeSpecs[info.Family]
			if !ok {
				t.Fatalf("no smoke spec for family %q", info.Family)
			}
			t.Logf("%s: family=%s dialect=%s database=%s", key, info.Family, info.Dialect, info.Database)
			t.Logf("%s: version: %s", key, info.Version)

			if info.Version == "" {
				t.Fatal("empty server version")
			}
			if spec.versionMarker != "" && !strings.Contains(info.Version, spec.versionMarker) {
				t.Fatalf("version %q does not contain %q", info.Version, spec.versionMarker)
			}
			if info.Family == dbtest.FamilyMySQL && (strings.Contains(info.Version, "MariaDB") || strings.Contains(info.Version, "TiDB")) {
				t.Fatalf("mysql key connected to a non-MySQL server: %q", info.Version)
			}

			testRoundTrip(t, db, spec)
			testCheckConstraint(t, db, info, spec)

			got := testDDLRollback(t, db, info, spec)
			t.Logf("FINDING %s ddl_rollback=%v", key, got)
			if got != spec.ddlRollback {
				t.Errorf("DDL rollback: got %v, expected %v for %s", got, spec.ddlRollback, info.Family)
			}

			switch info.Family {
			case dbtest.FamilyCockroachDB:
				testCockroachTransactionalDDL(t, db, info)
			case dbtest.FamilyGaussDB:
				testGaussCompatibility(t, db, info)
			case dbtest.FamilyTiDB:
				var on string
				mustScan(t, db, &on, "SELECT @@global.tidb_enable_check_constraint")
				if on != "1" && !strings.EqualFold(on, "ON") {
					t.Errorf("tidb_enable_check_constraint = %q, want ON", on)
				}
			}
		})
	}
}

func testRoundTrip(t *testing.T, db *gorm.DB, spec smokeSpec) {
	t.Helper()
	mustExec(t, db, spec.createItems)
	created := time.Date(2024, 5, 6, 7, 8, 9, 0, time.UTC)
	mustExec(t, db, "INSERT INTO smoke_items (id, name, price, active, created_at) VALUES (?, ?, ?, "+spec.trueLit+", ?)",
		1, "alpha", 12.34, created)
	mustExec(t, db, "INSERT INTO smoke_items (id, name, price, active, created_at) VALUES (?, ?, ?, "+spec.trueLit+", ?)",
		2, "beta", 5.5, created)

	var name string
	mustScan(t, db, &name, "SELECT name FROM smoke_items WHERE id = ?", 1)
	if name != "alpha" {
		t.Fatalf("select name: got %q, want alpha", name)
	}
	var n int64
	mustScan(t, db, &n, "SELECT COUNT(*) FROM smoke_items WHERE price = 12.34 AND active = "+spec.trueLit)
	if n != 1 {
		t.Fatalf("count by price/active: got %d, want 1", n)
	}
	var ts time.Time
	mustScan(t, db, &ts, "SELECT created_at FROM smoke_items WHERE id = ?", 2)
	if !ts.Equal(created) && !sameWallClock(ts, created) {
		t.Fatalf("created_at: got %v, want %v", ts, created)
	}
	mustScan(t, db, &n, "SELECT COUNT(*) FROM smoke_items")
	if n != 2 {
		t.Fatalf("row count: got %d, want 2", n)
	}
	mustExec(t, db, "DROP TABLE smoke_items")
	if tableExists(t, db, "smoke_items") {
		t.Fatal("smoke_items still exists after DROP TABLE")
	}
}

// sameWallClock accepts servers that store TIMESTAMP without zone and hand it
// back in another location with the same wall-clock reading.
func sameWallClock(a, b time.Time) bool {
	return a.Year() == b.Year() && a.YearDay() == b.YearDay() &&
		a.Hour() == b.Hour() && a.Minute() == b.Minute() && a.Second() == b.Second()
}

func testCheckConstraint(t *testing.T, db *gorm.DB, info dbtest.Info, spec smokeSpec) {
	t.Helper()
	mustExec(t, db, spec.createCheck)
	mustExec(t, db, "INSERT INTO smoke_check (id, qty) VALUES (1, 5)")
	quiet := db.Session(&gorm.Session{Logger: db.Logger.LogMode(logger.Silent)})
	err := quiet.Exec("INSERT INTO smoke_check (id, qty) VALUES (2, -1)").Error
	if err == nil {
		t.Errorf("CHECK (qty > 0) not enforced on %s", info.Key)
	} else {
		t.Logf("check constraint violation rejected: %v", err)
	}
	mustExec(t, db, "DROP TABLE smoke_check")
}

// testDDLRollback runs CREATE TABLE inside a transaction, rolls back and
// reports whether the table is gone (true = DDL was rolled back).
func testDDLRollback(t *testing.T, db *gorm.DB, info dbtest.Info, spec smokeSpec) bool {
	t.Helper()
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatalf("begin: %v", tx.Error)
	}
	if err := tx.Exec(spec.createRollback).Error; err != nil {
		_ = tx.Rollback()
		t.Fatalf("create table in transaction: %v", err)
	}
	if err := tx.Rollback().Error; err != nil {
		t.Logf("rollback returned: %v", err)
	}
	exists := tableExists(t, db, "smoke_ddl_rb")
	if exists {
		mustExec(t, db, "DROP TABLE smoke_ddl_rb")
	}
	return !exists
}

// testCockroachTransactionalDDL shows that with autocommit_before_ddl = off,
// CockroachDB does roll back a CREATE TABLE.
func testCockroachTransactionalDDL(t *testing.T, db *gorm.DB, info dbtest.Info) {
	t.Helper()
	var def string
	mustScan(t, db, &def, "SHOW autocommit_before_ddl")
	t.Logf("FINDING cockroach default autocommit_before_ddl=%s", def)

	err := db.Connection(func(conn *gorm.DB) error {
		if err := conn.Exec("SET autocommit_before_ddl = false").Error; err != nil {
			return err
		}
		errRollback := errors.New("rollback")
		err := conn.Transaction(func(tx *gorm.DB) error {
			if err := tx.Exec("CREATE TABLE smoke_ddl_rb (id INT)").Error; err != nil {
				return err
			}
			return errRollback
		})
		if !errors.Is(err, errRollback) {
			return err
		}
		return conn.Exec("RESET autocommit_before_ddl").Error
	})
	if err != nil {
		t.Fatalf("transactional DDL with autocommit_before_ddl=off: %v", err)
	}
	rolledBack := !tableExists(t, db, "smoke_ddl_rb")
	t.Logf("FINDING cockroach ddl_rollback with autocommit_before_ddl=off: %v", rolledBack)
	if !rolledBack {
		mustExec(t, db, "DROP TABLE smoke_ddl_rb")
		t.Error("CREATE TABLE not rolled back although autocommit_before_ddl=off")
	}
}

// testGaussCompatibility verifies the isolated openGauss database really is a
// PG-compatible one and that the gaussdb driver authenticated with sha256.
func testGaussCompatibility(t *testing.T, db *gorm.DB, info dbtest.Info) {
	t.Helper()
	var compat string
	mustScan(t, db, &compat, "SELECT datcompatibility FROM pg_database WHERE datname = current_database()")
	if compat != "PG" {
		t.Errorf("datcompatibility = %q, want PG", compat)
	}
	var enc string
	mustScan(t, db, &enc, "SHOW password_encryption_type")
	var method string
	mustScan(t, db, &method, "SELECT substr(rolpassword, 1, 6) FROM pg_authid WHERE rolname = current_user")
	t.Logf("FINDING gauss datcompatibility=%s password_encryption_type=%s stored_hash_prefix=%s", compat, enc, method)
	if method != "sha256" {
		t.Errorf("gormgate password stored as %q, want sha256", method)
	}
}

func tableExists(t *testing.T, db *gorm.DB, table string) bool {
	t.Helper()
	var q string
	switch db.Dialector.Name() {
	case "postgres", "gaussdb":
		q = "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = ?"
	case "mysql":
		q = "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = ?"
	case "sqlserver":
		q = "SELECT COUNT(*) FROM sys.tables WHERE name = ?"
	case "oracle":
		q = "SELECT COUNT(*) FROM user_tables WHERE table_name = UPPER(?)"
	case "clickhouse":
		q = "SELECT count() FROM system.tables WHERE database = currentDatabase() AND name = ?"
	case "sqlite":
		q = "SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?"
	default:
		t.Fatalf("tableExists: unsupported dialect %q", db.Dialector.Name())
	}
	var n int64
	mustScan(t, db, &n, q, table)
	return n > 0
}

func mustExec(t *testing.T, db *gorm.DB, sql string, args ...any) {
	t.Helper()
	if err := db.Exec(sql, args...).Error; err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

func mustScan(t *testing.T, db *gorm.DB, dest any, sql string, args ...any) {
	t.Helper()
	if err := db.Raw(sql, args...).Row().Scan(dest); err != nil {
		t.Fatalf("query %q: %v", sql, err)
	}
}
