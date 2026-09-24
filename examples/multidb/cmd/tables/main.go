// Command tables prints the tables that exist in each database, which is
// where the router's effect is visible: showmigrations records every
// migration on both databases, but only one of them has the tables.
package main

import (
	"fmt"
	"os"
	"strings"

	"gorm.io/gorm"

	"example.com/multidb/internal/store"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "tables:", err)
		os.Exit(1)
	}
}

func run() error {
	for _, db := range []struct {
		alias, env, fallback string
	}{
		{"default", "MULTIDB_DEFAULT_DB", "default.sqlite3"},
		{"reports", "MULTIDB_REPORTS_DB", "reports.sqlite3"},
	} {
		conn, err := store.Open(db.env, db.fallback)
		if err != nil {
			return err
		}
		names, err := appTables(conn)
		if err != nil {
			return fmt.Errorf("%s: %w", db.alias, err)
		}
		fmt.Printf("%-8s %s\n", db.alias, strings.Join(names, " "))
	}
	return nil
}

// appTables returns the tables of the project's apps, leaving out
// gormgate_migrations and SQLite's own bookkeeping tables.
func appTables(conn *gorm.DB) ([]string, error) {
	all, err := conn.Migrator().GetTables()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, name := range all {
		if name == "gormgate_migrations" || strings.HasPrefix(name, "sqlite_") {
			continue
		}
		out = append(out, name)
	}
	return out, nil
}
