// Command gorm-gate runs the project's gormgate management commands.
package main

import (
	"context"

	"gorm.io/gorm"

	"example.com/adoptdb/crm"
	"example.com/adoptdb/internal/store"
	"github.com/ctolon/gormgate"
	_ "github.com/ctolon/gormgate/backends/sqlite3"
)

func main() {
	gormgate.Execute(&gormgate.Settings{
		Apps: []*gormgate.AppConfig{
			gormgate.App("crm", &crm.Customer{}, &crm.Contract{}),
		},
		Databases: map[string]gormgate.Database{
			"default": {Open: func(ctx context.Context) (*gorm.DB, error) {
				return store.Open()
			}},
		},
	})
}
