// Command manage runs the gormgate side of the parity harness.
package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"example.com/parityproj/auth_app"
	"example.com/parityproj/blog"
	"github.com/ctolon/gormgate"
	_ "github.com/ctolon/gormgate/backends/postgresql"
)

func main() {
	var disabled map[string]*string
	if v := os.Getenv("PARITY_DISABLE_MIGRATIONS"); v != "" {
		disabled = map[string]*string{}
		for _, app := range strings.Split(v, ",") {
			disabled[app] = nil
		}
	}
	gormgate.Execute(&gormgate.Settings{
		MigrationModules: disabled,
		Apps: []*gormgate.AppConfig{
			gormgate.App("auth_app", auth_app.Models()...),
			gormgate.App("blog", blog.Models()...),
		},
		Databases: map[string]gormgate.Database{
			"default": {Open: func(ctx context.Context) (*gorm.DB, error) {
				dsn := os.Getenv("PARITY_DSN")
				if dsn == "" {
					return nil, fmt.Errorf("PARITY_DSN is not set")
				}
				return gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Discard})
			}},
		},
		Command: "python manage.py",
	})
}
