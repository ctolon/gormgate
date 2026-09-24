// Package store opens the example's database.
package store

import (
	"os"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Path is the SQLite file the example works on. SQLite keeps the example
// runnable with nothing installed; a real project would open its database
// server here and take the whole DSN from the environment.
func Path() string {
	if p := os.Getenv("DATAMIGRATION_DB"); p != "" {
		return p
	}
	return "shop.sqlite3"
}

// Open connects to it.
func Open() (*gorm.DB, error) {
	return gorm.Open(sqlite.Open(Path()), &gorm.Config{Logger: logger.Discard})
}
