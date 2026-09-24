//go:build integration

package itest

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"gorm.io/driver/gaussdb"
	"gorm.io/gorm"

	"github.com/ctolon/gormgate/backends/base"
	"github.com/ctolon/gormgate/itest/internal/dbtest"
)

// TestGaussDBRequiresPGCompatibility checks that connecting to an openGauss
// database created in the default DBCOMPATIBILITY 'A' (Oracle) mode is
// refused with an explanatory error instead of silently producing a schema
// with Oracle semantics (where the empty string is NULL).
func TestGaussDBRequiresPGCompatibility(t *testing.T) {
	skipUnlessVendor(t, "gauss")
	v, ok := dbtest.Lookup("gauss")
	if !ok {
		t.Fatal("no gauss vendor")
	}
	// A throw-away 'PG' database only serves to reach the server; the
	// admin DSN is what we need to create the 'A' database next to it.
	_, info := dbtest.Open(t, "gauss")
	admin, err := gorm.Open(gaussdb.Open(v.DSN()), &gorm.Config{})
	if err != nil {
		t.Fatalf("admin connect: %v", err)
	}
	t.Cleanup(func() {
		if db, err := admin.DB(); err == nil {
			db.Close()
		}
	})
	name := info.Database + "_a"
	if err := admin.Exec(fmt.Sprintf(`CREATE DATABASE "%s" DBCOMPATIBILITY 'A'`, name)).Error; err != nil {
		t.Fatalf("create 'A' database: %v", err)
	}
	t.Cleanup(func() {
		admin.Exec(`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = ? AND pid <> pg_backend_pid()`, name)
		if err := admin.Exec(fmt.Sprintf(`DROP DATABASE IF EXISTS "%s"`, name)).Error; err != nil {
			t.Errorf("drop 'A' database: %v", err)
		}
	})

	u, err := url.Parse(v.DSN())
	if err != nil {
		t.Fatalf("parse DSN: %v", err)
	}
	u.Path = "/" + name
	db, err := gorm.Open(gaussdb.Open(u.String()), &gorm.Config{})
	if err != nil {
		t.Fatalf("connect to the 'A' database: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			sqlDB.Close()
		}
	})
	_, err = base.Open(context.Background(), "default", db, nil)
	if err == nil {
		t.Fatal("connecting to a DBCOMPATIBILITY 'A' database was accepted")
	}
	for _, want := range []string{"DBCOMPATIBILITY 'A'", "requires 'PG'", "CREATE DATABASE"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}
