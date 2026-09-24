// Command gorm-gate runs the project's gormgate management commands.
package main

import (
	"context"
	"fmt"
	"os"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"example.com/blogproj/auth"
	"example.com/blogproj/blog"
	"github.com/ctolon/gormgate"
	_ "github.com/ctolon/gormgate/backends/postgresql"
)

func main() {
	gormgate.Execute(&gormgate.Settings{
		Apps: []*gormgate.AppConfig{
			gormgate.App("auth", &auth.User{}),
			gormgate.App("blog", &blog.Post{}, &blog.Comment{}),
		},
		Databases: map[string]gormgate.Database{
			"default": {Open: func(ctx context.Context) (*gorm.DB, error) {
				dsn := os.Getenv("BLOGPROJ_DSN")
				if dsn == "" {
					return nil, fmt.Errorf("BLOGPROJ_DSN is not set")
				}
				return gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Discard})
			}},
		},
	})
}
