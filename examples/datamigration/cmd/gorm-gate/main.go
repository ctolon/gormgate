// Command gorm-gate runs the project's gormgate management commands.
package main

import (
	"context"

	"gorm.io/gorm"

	"example.com/datamigration/internal/store"
	"example.com/datamigration/shop"
	"github.com/ctolon/gormgate"
	_ "github.com/ctolon/gormgate/backends/sqlite3"
)

func main() {
	gormgate.Execute(&gormgate.Settings{
		Apps: []*gormgate.AppConfig{
			gormgate.App("shop", &shop.Order{}),
		},
		Databases: map[string]gormgate.Database{
			"default": {Open: func(ctx context.Context) (*gorm.DB, error) {
				return store.Open()
			}},
		},
	})
}
