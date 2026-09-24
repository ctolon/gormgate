//go:build integration

package itest

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/ctolon/gormgate"
	"github.com/ctolon/gormgate/backends/base"
	"github.com/ctolon/gormgate/itest/internal/dbtest"
	"github.com/ctolon/gormgate/itest/internal/harness"
	m "github.com/ctolon/gormgate/migrations"
)

type apiUser struct {
	ID   uint   `gorm:"primaryKey"`
	Name string `gorm:"size:50;not null"`
}

func (apiUser) TableName() string { return "api_users" }

// TestCallCommand drives gormgate as a library (Django's call_command) and
// checks the safety net that protects a stale binary: once makemigrations
// has written a file that is not compiled in, the next command refuses to
// run.
func TestCallCommand(t *testing.T) {
	_, info := dbtest.Open(t, "pg18")
	dir := t.TempDir()
	var out, errOut bytes.Buffer
	settings := &gormgate.Settings{
		Apps: []*gormgate.AppConfig{
			gormgate.App("apiauth", &apiUser{}).Migrations(dir+"/apiauth", "example.com/api/apiauth/migrations"),
		},
		Databases: map[string]gormgate.Database{
			"default": {Open: func(context.Context) (*gorm.DB, error) {
				return gorm.Open(postgres.Open(info.DSN), &gorm.Config{Logger: logger.Discard})
			}},
		},
		CommandDir:     dir,
		CommandPackage: "main",
	}
	// The streams are per invocation, not per settings.
	streams := &gormgate.IO{Stdout: &out, Stderr: &errOut}
	ctx := context.Background()
	if err := gormgate.CallCommand(ctx, settings, streams, "makemigrations", "apiauth"); err != nil {
		t.Fatalf("makemigrations: %v\n%s%s", err, out.String(), errOut.String())
	}
	if !strings.Contains(out.String(), "apiauth/0001_initial.go") || !strings.Contains(out.String(), "+ create model apiUser") {
		t.Fatalf("makemigrations output:\n%s", out.String())
	}
	written := filepath.Join(dir, "apiauth", "0001_initial.go")
	if _, err := os.Stat(written); err != nil {
		t.Fatalf("expected %s: %v", written, err)
	}

	// The file exists but is not part of this binary: every command must
	// refuse rather than migrate a history it cannot see.
	err := gormgate.CallCommand(ctx, settings, streams, "migrate")
	if err == nil || !strings.Contains(err.Error(), "not compiled into this command") {
		t.Fatalf("migrate after makemigrations: error %v, want the stale-binary error", err)
	}

	// Without the files, the command works again and reports no
	// migrations.
	if err := os.RemoveAll(filepath.Join(dir, "apiauth")); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := gormgate.CallCommand(ctx, settings, streams, "showmigrations"); err != nil {
		t.Fatalf("showmigrations: %v\n%s%s", err, out.String(), errOut.String())
	}
}

// TestRouters checks that a router keeps an app off a database: the
// migration is applied on the database the router allows and skipped on
// the other, while the migration is still recorded on both.
func TestRouters(t *testing.T) {
	dbAllowed, _ := dbtest.Open(t, "pg18")
	dbDenied, _ := dbtest.Open(t, "pg18")

	mig := &m.Migration{App: "routed", Name: "0001_initial", Operations: []m.Operation{
		&m.CreateModel{Name: "Thing", Table: "routed_thing", Fields: m.Fields{
			{Name: "id", Field: m.Field{Type: m.Int, Size: 64, PrimaryKey: true, AutoIncrement: true}},
		}},
	}}

	for _, tc := range []struct {
		name  string
		db    *gorm.DB
		allow bool
	}{{"allowed", dbAllowed, true}, {"denied", dbDenied, false}} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			p := harness.New(t, tc.db, "routed")
			p.Conn.Routers = []base.Router{routerFunc(func(db, app string, h gormgate.Hints) *bool {
				if app != "routed" {
					return nil
				}
				v := tc.allow
				return &v
			})}
			p.Register(mig)
			p.MustMigrate(mig.Key())

			tables, err := p.Conn.Introspection().TableNames(false)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, tb := range tables {
				if tb.Name == "routed_thing" {
					found = true
				}
			}
			if found != tc.allow {
				t.Errorf("table routed_thing exists = %v, want %v", found, tc.allow)
			}
			applied, err := p.Executor().Recorder.AppliedMigrations()
			if err != nil {
				t.Fatal(err)
			}
			if len(applied) != 1 || applied[0] != mig.Key() {
				t.Errorf("applied = %v, want the migration to be recorded on both databases", applied)
			}
		})
	}
}

type routerFunc func(db, app string, h gormgate.Hints) *bool

func (f routerFunc) AllowMigrate(db, app string, h gormgate.Hints) *bool {
	return f(db, app, h)
}

// TestMigrateHooks checks that a pre/post-migrate hook is told about the
// run the way Django's pre_migrate / post_migrate receivers are: the app,
// the plan, the verbosity, and whether the command may prompt (--no-input
// clears Interactive).
func TestMigrateHooks(t *testing.T) {
	_, info := dbtest.Open(t, "pg18")
	dir := t.TempDir()
	// The migration registry is process-global by design — a migration
	// file registers itself from init() — and nothing ever unregisters.
	// A fresh app label per run therefore keeps "go test -count=N" from
	// registering the same key twice, which panics.
	app := fmt.Sprintf("hookapp%d", hookAppSeq.Add(1))
	var out, errOut bytes.Buffer
	var pre, post []gormgate.MigrateRun
	settings := &gormgate.Settings{
		Apps: []*gormgate.AppConfig{
			gormgate.App(app, &apiUser{}).Migrations(dir+"/"+app, "example.com/api/"+app+"/migrations"),
		},
		Databases: map[string]gormgate.Database{
			"default": {Open: func(context.Context) (*gorm.DB, error) {
				return gorm.Open(postgres.Open(info.DSN), &gorm.Config{Logger: logger.Discard})
			}},
		},
		PreMigrate: map[string][]gormgate.Hook{
			app: {func(r gormgate.MigrateRun) error { pre = append(pre, r); return nil }},
		},
		PostMigrate: map[string][]gormgate.Hook{
			app: {func(r gormgate.MigrateRun) error { post = append(post, r); return nil }},
		},
		CommandDir:     dir,
		CommandPackage: "main",
	}
	// The streams are per invocation, not per settings.
	streams := &gormgate.IO{Stdout: &out, Stderr: &errOut}
	ctx := context.Background()
	mig := &m.Migration{App: app, Name: "0001_initial", Operations: []m.Operation{
		&m.CreateModel{Name: "apiUser", Table: "api_users", Fields: m.Fields{
			{Name: "id", Field: m.Field{Type: m.Int, Size: 64, PrimaryKey: true, AutoIncrement: true}},
			{Name: "name", Field: m.Field{Type: m.String, Size: 50}},
		}},
	}}
	m.Register(mig)

	if err := gormgate.CallCommand(ctx, settings, streams, "migrate", "--no-input", "-v"); err != nil {
		t.Fatalf("migrate: %v\n%s%s", err, out.String(), errOut.String())
	}
	for _, tc := range []struct {
		name string
		runs []gormgate.MigrateRun
	}{{"pre", pre}, {"post", post}} {
		if len(tc.runs) != 1 {
			t.Fatalf("%s-migrate hook ran %d times, want 1", tc.name, len(tc.runs))
		}
		r := tc.runs[0]
		if r.App != app || r.Conn == nil {
			t.Errorf("%s-migrate hook: app = %q, conn = %v", tc.name, r.App, r.Conn)
		}
		if r.Verbosity != 2 {
			t.Errorf("%s-migrate hook: verbosity = %d, want 2", tc.name, r.Verbosity)
		}
		if r.Interactive {
			t.Errorf("%s-migrate hook: Interactive is set although --no-input was given", tc.name)
		}
	}
	if len(pre[0].Plan) != 1 {
		t.Errorf("pre-migrate plan has %d steps, want the one migration", len(pre[0].Plan))
	}
	if len(post[0].Plan) != 1 {
		t.Errorf("post-migrate plan has %d steps, want the one migration", len(post[0].Plan))
	}
}

// hookAppSeq numbers the app label TestMigrateHooks registers, so that
// repeated runs in one process do not collide in the global registry.
var hookAppSeq atomic.Int64
