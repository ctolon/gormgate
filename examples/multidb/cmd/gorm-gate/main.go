// Command gorm-gate runs the project's gormgate management commands
// against either of its two databases.
package main

import (
	"context"

	"gorm.io/gorm"

	"example.com/multidb/analytics"
	"example.com/multidb/internal/store"
	"example.com/multidb/shop"
	"github.com/ctolon/gormgate"
	_ "github.com/ctolon/gormgate/backends/sqlite3"
)

func main() {
	gormgate.Execute(&gormgate.Settings{
		Apps: []*gormgate.AppConfig{
			gormgate.App("shop", &shop.Order{}),
			gormgate.App("analytics", &analytics.PageView{}),
		},
		Databases: map[string]gormgate.Database{
			"default": {Open: func(ctx context.Context) (*gorm.DB, error) {
				return store.Open("MULTIDB_DEFAULT_DB", "default.sqlite3")
			}},
			"reports": {Open: func(ctx context.Context) (*gorm.DB, error) {
				return store.Open("MULTIDB_REPORTS_DB", "reports.sqlite3")
			}},
		},
		Routers: []gormgate.Router{reportsRouter{}},
	})
}
