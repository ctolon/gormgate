// Command gorm-gate runs the project's gormgate management commands. In
// this project it is an image of its own concern: compose runs it as a step
// before the server starts, never from inside the server.
package main

import (
	"context"

	"gorm.io/gorm"

	"example.com/dockerproj/internal/store"
	"example.com/dockerproj/shop"
	"github.com/ctolon/gormgate"
	_ "github.com/ctolon/gormgate/backends/postgresql"
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
