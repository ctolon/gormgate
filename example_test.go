package gormgate_test

import (
	"context"
	"fmt"
	"os"

	"gorm.io/gorm"

	"github.com/ctolon/gormgate"
)

type user struct {
	ID   uint   `gorm:"primaryKey"`
	Name string `gorm:"size:100;not null"`
}

type post struct {
	ID       uint `gorm:"primaryKey"`
	Title    string
	AuthorID uint
	Author   user `gorm:"constraint:OnDelete:CASCADE"`
}

// Execute runs the command line in os.Args. This is the whole of a
// project's cmd/gorm-gate/main.go: it declares which apps exist and how to
// reach the databases, and gormgate provides makemigrations, migrate and
// the rest.
func ExampleExecute() {
	gormgate.Execute(&gormgate.Settings{
		Apps: []*gormgate.AppConfig{
			gormgate.App("auth", &user{}),
			gormgate.App("blog", &post{}),
		},
		Databases: map[string]gormgate.Database{
			"default": {Open: func(ctx context.Context) (*gorm.DB, error) {
				return gorm.Open(yourDriver(os.Getenv("DSN")))
			}},
		},
	})
}

// CallCommand runs one command in-process instead of from the command line,
// which is how a test or a deployment step drives gormgate. Passing an IO
// captures what the command writes.
func ExampleCallCommand() {
	settings := &gormgate.Settings{
		Apps: []*gormgate.AppConfig{gormgate.App("blog", &post{})},
		Databases: map[string]gormgate.Database{
			"default": {Open: func(ctx context.Context) (*gorm.DB, error) {
				return gorm.Open(yourDriver(os.Getenv("DSN")))
			}},
		},
	}

	var out, errOut safeBuffer
	streams := &gormgate.IO{Stdout: &out, Stderr: &errOut}
	if err := gormgate.CallCommand(context.Background(), settings, streams, "migrate", "--noinput"); err != nil {
		fmt.Println("migrate failed:", err)
		return
	}
	fmt.Print(out.String())
}

// reportsRouter keeps the "analytics" app on the "reports" database and
// every other app off it. A nil result means "no opinion", which lets the
// next router decide.
//
// --8<-- [start:router]
type reportsRouter struct{}

func (reportsRouter) AllowMigrate(db, app string, h gormgate.Hints) *bool {
	switch {
	case app == "analytics":
		return gormgate.Ptr(db == "reports")
	case db == "reports":
		return gormgate.Ptr(false)
	default:
		return nil
	}
}

// --8<-- [end:router]

// A Router decides which app may be migrated on which database. Register
// it in Settings.Routers; gormgate asks each router in turn and takes the
// first opinion.
func ExampleRouter() {
	r := reportsRouter{}
	fmt.Println("analytics on reports:", *r.AllowMigrate("reports", "analytics", gormgate.Hints{ModelName: "event"}))
	fmt.Println("analytics on default:", *r.AllowMigrate("default", "analytics", gormgate.Hints{ModelName: "event"}))
	fmt.Println("blog on default is left to the next router:", r.AllowMigrate("default", "blog", gormgate.Hints{ModelName: "post"}) == nil)
	// Output:
	// analytics on reports: true
	// analytics on default: false
	// blog on default is left to the next router: true
}

// A Hook runs before or after migrate for one app, like Django's
// pre_migrate and post_migrate signals. It is told which app ran, on which
// connection, with which plan, and whether the command may prompt --
// Interactive is false when --noinput was given.
func ExampleHook() {
	var announce gormgate.Hook = func(r gormgate.MigrateRun) error {
		if r.Verbosity >= 1 {
			fmt.Printf("%s: %d migration(s), interactive=%v\n", r.App, len(r.Plan), r.Interactive)
		}
		return nil
	}

	// Register it as PostMigrate: map[string][]gormgate.Hook{"blog": {announce}}.
	// gormgate calls it like this once the app's migrations have run:
	_ = announce(gormgate.MigrateRun{App: "blog", Verbosity: 1, Interactive: false})
	// Output:
	// blog: 0 migration(s), interactive=false
}
