// Package store opens the example's two databases.
package store

import (
	"os"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Open connects to the SQLite file named by env, or to fallback when env is
// unset. Two SQLite files keep the example runnable with nothing installed;
// a real project would open two database servers here.
func Open(env, fallback string) (*gorm.DB, error) {
	path := os.Getenv(env)
	if path == "" {
		path = fallback
	}
	return gorm.Open(sqlite.Open(path), &gorm.Config{Logger: logger.Discard})
}
