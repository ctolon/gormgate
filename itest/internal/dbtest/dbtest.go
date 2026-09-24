package dbtest

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	glebarezsqlite "github.com/glebarez/sqlite"
	mysqldrv "github.com/go-sql-driver/mysql"
	"github.com/godror/godror"
	"github.com/oracle-samples/gorm-oracle/oracle"
	"gorm.io/driver/clickhouse"
	"gorm.io/driver/gaussdb"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/driver/sqlserver"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Info describes the isolated database handed out by Open.
type Info struct {
	// Key is the vendor key passed to Open.
	Key string
	// Family is the vendor family (one of the Family* constants).
	Family string
	// Dialect is the gorm dialector name (db.Dialector.Name()).
	Dialect string
	// Version is the server version string as reported by the server.
	Version string
	// Database is the name of the isolated database (for Oracle: the
	// schema/user; for SQLite: the file path).
	Database string
	// DSN is the DSN the returned *gorm.DB was opened with.
	DSN string
}

// connectTimeout bounds the reachability probe of the admin connection.
const connectTimeout = 15 * time.Second

// Open creates a fresh, isolated database for the vendor identified by key,
// returns a *gorm.DB connected to it and registers its removal in t.Cleanup.
//
// When the server cannot be reached the test is skipped, unless
// GORMGATE_ITEST_REQUIRE is set, in which case it fails.
func Open(t testing.TB, key string) (*gorm.DB, Info) {
	t.Helper()
	v, ok := Lookup(key)
	if !ok {
		t.Fatalf("dbtest: unknown vendor key %q (known: %s)", key, strings.Join(Keys(), ", "))
	}
	if v.Family == FamilySQLite {
		return openSQLite(t, v)
	}

	adminDSN := v.DSN()
	admin, err := openGorm(t, v, adminDSN)
	if err == nil {
		err = ping(admin)
	}
	if err != nil {
		if admin != nil {
			closeGorm(admin)
		}
		unreachable(t, v, err)
		return nil, Info{} // unreachable never returns
	}
	t.Cleanup(func() { closeGorm(admin) })

	name := randomName()
	testDSN, drop, err := isolate(admin, v, adminDSN, name)
	if err != nil {
		t.Fatalf("dbtest[%s]: create isolated database %s: %v", key, name, err)
	}
	t.Cleanup(func() {
		if err := drop(); err != nil {
			t.Errorf("dbtest[%s]: drop isolated database %s: %v", key, name, err)
		}
	})

	db, err := openGorm(t, v, testDSN)
	if err == nil {
		err = ping(db)
	}
	if err != nil {
		t.Fatalf("dbtest[%s]: connect to isolated database %s: %v", key, name, err)
	}
	// Registered after drop, so it runs before it (cleanups are LIFO).
	t.Cleanup(func() { closeGorm(db) })

	info := Info{Key: key, Family: v.Family, Dialect: db.Dialector.Name(), Database: name, DSN: testDSN}
	if info.Version, err = ServerVersion(db, v.Family); err != nil {
		t.Fatalf("dbtest[%s]: server version: %v", key, err)
	}
	return db, info
}

func unreachable(t testing.TB, v Vendor, err error) {
	t.Helper()
	hint := ""
	if v.Service != "" {
		hint = fmt.Sprintf(" (start it with: docker compose -f itest/compose.yaml up -d --wait %s)", v.Service)
	}
	if strings.Contains(err.Error(), "DPI-1047") {
		hint = " (Oracle Instant Client not found; run the Oracle tests in the runner container: make -C itest oracle-smoke)"
	}
	msg := fmt.Sprintf("dbtest[%s]: server unreachable%s: %v", v.Key, hint, err)
	if os.Getenv(EnvRequire) != "" {
		t.Fatal(msg)
	}
	t.Skip(msg)
}

func ping(db *gorm.DB) error {
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), connectTimeout)
	defer cancel()
	return sqlDB.PingContext(ctx)
}

func closeGorm(db *gorm.DB) {
	if sqlDB, err := db.DB(); err == nil {
		_ = sqlDB.Close()
	}
}

func randomName() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return "gg_" + hex.EncodeToString(b[:])
}

func gormConfig(t testing.TB) *gorm.Config {
	level := logger.Warn
	if os.Getenv(EnvSQLLog) != "" {
		level = logger.Info
	}
	return &gorm.Config{
		Logger: logger.New(tWriter{t}, logger.Config{
			SlowThreshold:             5 * time.Second,
			LogLevel:                  level,
			IgnoreRecordNotFoundError: true,
		}),
	}
}

type tWriter struct{ t testing.TB }

func (w tWriter) Printf(format string, args ...any) {
	w.t.Helper()
	w.t.Logf(format, args...)
}

// openGorm opens (but does not ping) a gorm connection for a server vendor.
func openGorm(t testing.TB, v Vendor, dsn string) (*gorm.DB, error) {
	var d gorm.Dialector
	switch v.Family {
	case FamilyPostgreSQL, FamilyCockroachDB:
		d = postgres.Open(dsn)
	case FamilyGaussDB:
		d = gaussdb.Open(dsn)
	case FamilyMySQL, FamilyMariaDB, FamilyTiDB:
		d = mysql.Open(dsn)
	case FamilyMSSQL:
		d = sqlserver.Open(dsn)
	case FamilyClickHouse:
		d = clickhouse.Open(dsn)
	case FamilyOracle:
		params, err := godror.ParseDSN(dsn)
		if err != nil {
			return nil, fmt.Errorf("parse oracle DSN: %w", err)
		}
		d = oracle.New(oracle.Config{Conn: sql.OpenDB(godror.NewConnector(params))})
	default:
		return nil, fmt.Errorf("unsupported family %q", v.Family)
	}
	return gorm.Open(d, gormConfig(t))
}

// isolate creates the isolated database called name through the admin
// connection and returns the DSN to reach it plus a function dropping it.
func isolate(admin *gorm.DB, v Vendor, adminDSN, name string) (string, func() error, error) {
	switch v.Family {
	case FamilyPostgreSQL:
		if err := admin.Exec(fmt.Sprintf(`CREATE DATABASE "%s"`, name)).Error; err != nil {
			return "", nil, err
		}
		return withPGDatabase(adminDSN, name), func() error {
			return admin.Exec(fmt.Sprintf(`DROP DATABASE IF EXISTS "%s" WITH (FORCE)`, name)).Error
		}, nil

	case FamilyCockroachDB:
		if err := admin.Exec(fmt.Sprintf(`CREATE DATABASE "%s"`, name)).Error; err != nil {
			return "", nil, err
		}
		return withPGDatabase(adminDSN, name), func() error {
			return admin.Exec(fmt.Sprintf(`DROP DATABASE IF EXISTS "%s" CASCADE`, name)).Error
		}, nil

	case FamilyGaussDB:
		// DBCOMPATIBILITY 'PG' gives PostgreSQL semantics (e.g. '' is not
		// NULL); the openGauss default is 'A' (Oracle-like).
		if err := admin.Exec(fmt.Sprintf(`CREATE DATABASE "%s" DBCOMPATIBILITY 'PG'`, name)).Error; err != nil {
			return "", nil, err
		}
		return withPGDatabase(adminDSN, name), func() error {
			// openGauss has no DROP DATABASE ... WITH (FORCE).
			return retry(func() error {
				if err := admin.Exec(`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = ? AND pid <> pg_backend_pid()`, name).Error; err != nil {
					return err
				}
				return admin.Exec(fmt.Sprintf(`DROP DATABASE IF EXISTS "%s"`, name)).Error
			})
		}, nil

	case FamilyMySQL, FamilyMariaDB, FamilyTiDB:
		cfg, err := mysqldrv.ParseDSN(adminDSN)
		if err != nil {
			return "", nil, fmt.Errorf("parse mysql DSN: %w", err)
		}
		if err := admin.Exec(fmt.Sprintf("CREATE DATABASE `%s`", name)).Error; err != nil {
			return "", nil, err
		}
		cfg.DBName = name
		return cfg.FormatDSN(), func() error {
			return admin.Exec(fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", name)).Error
		}, nil

	case FamilyMSSQL:
		u, err := url.Parse(adminDSN)
		if err != nil {
			return "", nil, fmt.Errorf("parse sqlserver DSN (URL form required): %w", err)
		}
		if err := admin.Exec(fmt.Sprintf("CREATE DATABASE [%s]", name)).Error; err != nil {
			return "", nil, err
		}
		q := u.Query()
		q.Set("database", name)
		u.RawQuery = q.Encode()
		return u.String(), func() error {
			return admin.Exec(fmt.Sprintf(
				"IF DB_ID(N'%[1]s') IS NOT NULL BEGIN ALTER DATABASE [%[1]s] SET SINGLE_USER WITH ROLLBACK IMMEDIATE; DROP DATABASE [%[1]s]; END", name)).Error
		}, nil

	case FamilyClickHouse:
		u, err := url.Parse(adminDSN)
		if err != nil {
			return "", nil, fmt.Errorf("parse clickhouse DSN (URL form required): %w", err)
		}
		if err := admin.Exec(fmt.Sprintf("CREATE DATABASE `%s`", name)).Error; err != nil {
			return "", nil, err
		}
		u.Path = "/" + name
		return u.String(), func() error {
			return admin.Exec(fmt.Sprintf("DROP DATABASE IF EXISTS `%s` SYNC", name)).Error
		}, nil

	case FamilyOracle:
		return isolateOracle(admin, adminDSN, name)
	}
	return "", nil, fmt.Errorf("unsupported family %q", v.Family)
}

// isolateOracle creates a dedicated user (= schema) per test. The admin user
// needs the privileges granted in docker/oracle/01-gormgate-admin.sql.
func isolateOracle(admin *gorm.DB, adminDSN, name string) (string, func() error, error) {
	params, err := godror.ParseDSN(adminDSN)
	if err != nil {
		return "", nil, fmt.Errorf("parse oracle DSN: %w", err)
	}
	user := strings.ToUpper(name)
	password := "Pw" + strings.TrimPrefix(randomName(), "gg_")
	stmts := []string{
		fmt.Sprintf(`CREATE USER %s IDENTIFIED BY "%s" DEFAULT TABLESPACE USERS QUOTA UNLIMITED ON USERS`, user, password),
		fmt.Sprintf(`GRANT CREATE SESSION, CREATE TABLE, CREATE VIEW, CREATE SEQUENCE, CREATE PROCEDURE, CREATE TRIGGER, CREATE SYNONYM, CREATE TYPE, CREATE MATERIALIZED VIEW TO %s`, user),
	}
	for _, s := range stmts {
		if err := admin.Exec(s).Error; err != nil {
			return "", nil, err
		}
	}
	params.Username = user
	params.Password = godror.NewPassword(password)
	drop := func() error {
		return retry(func() error {
			var sessions []struct {
				SID    int64 `gorm:"column:SID"`
				Serial int64 `gorm:"column:SERIAL"`
			}
			if err := admin.Raw(`SELECT sid AS "SID", serial# AS "SERIAL" FROM v$session WHERE username = ?`, user).Scan(&sessions).Error; err != nil {
				return err
			}
			for _, s := range sessions {
				_ = admin.Exec(fmt.Sprintf(`ALTER SYSTEM KILL SESSION '%d,%d' IMMEDIATE`, s.SID, s.Serial)).Error
			}
			return admin.Exec(fmt.Sprintf(`DROP USER %s CASCADE`, user)).Error
		})
	}
	return params.StringWithPassword(), drop, nil
}

// withPGDatabase replaces the database of a PostgreSQL-style DSN given in
// URL form (postgres://, postgresql://, gaussdb://) or keyword/value form.
func withPGDatabase(dsn, name string) string {
	if strings.Contains(dsn, "://") {
		if u, err := url.Parse(dsn); err == nil {
			u.Path = "/" + name
			return u.String()
		}
	}
	// keyword/value form: a later dbname overrides an earlier one.
	return dsn + " dbname=" + name
}

func retry(f func() error) error {
	var err error
	for i := 0; i < 10; i++ {
		if err = f(); err == nil {
			return nil
		}
		time.Sleep(time.Duration(i+1) * 200 * time.Millisecond)
	}
	return err
}

func openSQLite(t testing.TB, v Vendor) (*gorm.DB, Info) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	tmpl := v.DSN()
	if !strings.Contains(tmpl, "{path}") {
		t.Fatalf("dbtest[%s]: %s must contain the {path} placeholder, got %q", v.Key, DSNEnvVar(v.Key), tmpl)
	}
	dsn := strings.ReplaceAll(tmpl, "{path}", path)
	var d gorm.Dialector
	if v.Key == "sqlite-purego" {
		d = glebarezsqlite.Open(dsn)
	} else {
		d = sqlite.Open(dsn)
	}
	db, err := gorm.Open(d, gormConfig(t))
	if err == nil {
		err = ping(db)
	}
	if err != nil {
		t.Fatalf("dbtest[%s]: open %s: %v", v.Key, dsn, err)
	}
	t.Cleanup(func() { closeGorm(db) }) // TempDir removal runs after this.
	info := Info{Key: v.Key, Family: v.Family, Dialect: db.Dialector.Name(), Database: path, DSN: dsn}
	if info.Version, err = ServerVersion(db, v.Family); err != nil {
		t.Fatalf("dbtest[%s]: server version: %v", v.Key, err)
	}
	return db, info
}

// ServerVersion queries the server (or engine) version string.
func ServerVersion(db *gorm.DB, family string) (string, error) {
	var q string
	switch family {
	case FamilyPostgreSQL, FamilyCockroachDB, FamilyGaussDB, FamilyClickHouse:
		q = "SELECT version()"
	case FamilyMySQL, FamilyMariaDB, FamilyTiDB:
		q = "SELECT VERSION()"
	case FamilyMSSQL:
		q = "SELECT @@VERSION"
	case FamilyOracle:
		q = "SELECT banner_full FROM v$version"
	case FamilySQLite:
		q = "SELECT sqlite_version()"
	default:
		return "", fmt.Errorf("unsupported family %q", family)
	}
	var version string
	if err := db.Raw(q).Row().Scan(&version); err != nil {
		return "", err
	}
	return strings.TrimSpace(version), nil
}
