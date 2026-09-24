// Package store opens the example's database.
package store

import (
	"fmt"
	"os"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Open connects to the database named by DOCKERPROJ_DSN. compose.yaml sets
// it for both services; there is no default, because a container that
// silently falls back to localhost is worse than one that will not start.
func Open() (*gorm.DB, error) {
	dsn := os.Getenv("DOCKERPROJ_DSN")
	if dsn == "" {
		return nil, fmt.Errorf("DOCKERPROJ_DSN is not set")
	}
	return gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Discard})
}
