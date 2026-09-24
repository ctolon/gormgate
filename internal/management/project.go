package management

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/schema"

	"github.com/ctolon/gormgate/backends/base"
	"github.com/ctolon/gormgate/internal/fromgorm"
	"github.com/ctolon/gormgate/internal/loader"
	m "github.com/ctolon/gormgate/migrations"
)

// App is an installed app.
type App struct {
	// Label is the app's short name, which names its migrations.
	Label string
	// Models are the gorm model structs the app defines.
	Models []any
	// MigrationsDir is the absolute directory of the migrations package.
	MigrationsDir string
	// MigrationsImport is the import path of the migrations package.
	MigrationsImport string
	// Disabled is MIGRATION_MODULES = {label: None}.
	Disabled bool
}

// Project is the configured project (Django's settings + app registry).
type Project struct {
	// Apps are the installed apps, in installation order.
	Apps []*App
	// Databases opens each configured connection by alias.
	Databases map[string]func() (*gorm.DB, error)
	// Namer is gorm's naming strategy.
	Namer schema.Namer
	// DisableForeignKeyConstraintWhenMigrating mirrors gorm.Config.
	DisableForeignKeyConstraintWhenMigrating bool
	// IgnoreRelationshipsWhenMigrating mirrors gorm.Config.
	IgnoreRelationshipsWhenMigrating bool
	// Routers decide which connection a model is migrated on.
	Routers []base.Router
	// Command replaces "python manage.py" in messages.
	Command string
	// CommandDir is the directory of the gorm-gate command's main package; a
	// file blank-importing every migrations package is kept there.
	CommandDir string
	// CommandPackage is the package name of CommandDir (default "main").
	CommandPackage string
	// SilencedChecks are the IDs of system checks not to report.
	SilencedChecks []string
	// PreMigrateHooks and PostMigrateHooks run around migrate, per app
	// label. They live on the project, not in a package variable, so that
	// two projects in one process cannot see each other's hooks.
	PreMigrateHooks  map[string][]MigrateHook
	PostMigrateHooks map[string][]MigrateHook
	// Ctx is the context of the command invocation this project serves.
	// A Project is built per invocation, so it is the one place that can
	// carry the caller's cancellation down to the database handshake.
	// Nil means context.Background.
	Ctx context.Context

	conns map[string]*base.Conn
	dbs   map[string]*gorm.DB
}

// App returns the installed app with label.
func (p *Project) App(label string) *App {
	for _, a := range p.Apps {
		if a.Label == label {
			return a
		}
	}
	return nil
}

// Labels returns app labels in installation order.
func (p *Project) Labels() []string {
	out := make([]string, len(p.Apps))
	for i, a := range p.Apps {
		out[i] = a.Label
	}
	return out
}

// DatabaseAliases returns the configured aliases, "default" first.
func (p *Project) DatabaseAliases() []string {
	var out []string
	for a := range p.Databases {
		if a != "default" {
			out = append(out, a)
		}
	}
	slices.Sort(out)
	if _, ok := p.Databases["default"]; ok {
		out = append([]string{"default"}, out...)
	}
	return out
}

// Conn opens (once) the pinned connection of alias.
func (p *Project) Conn(alias string) (*base.Conn, error) {
	if c, ok := p.conns[alias]; ok {
		return c, nil
	}
	open, ok := p.Databases[alias]
	if !ok {
		return nil, fmt.Errorf("the connection '%s' doesn't exist", alias)
	}
	db, err := open()
	if err != nil {
		return nil, err
	}
	if db == nil {
		return nil, fmt.Errorf("the Open function of database '%s' returned no handle and no error", alias)
	}
	ctx := p.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	c, err := base.Open(ctx, alias, db, p.Routers)
	if err != nil {
		return nil, err
	}
	if p.conns == nil {
		p.conns, p.dbs = map[string]*base.Conn{}, map[string]*gorm.DB{}
	}
	p.conns[alias], p.dbs[alias] = c, db
	return c, nil
}

// Close closes every opened connection (connections.close_all()).
func (p *Project) Close() {
	for alias, c := range p.conns {
		c.Close()
		if sqlDB, err := p.dbs[alias].DB(); err == nil {
			sqlDB.Close()
		}
	}
	p.conns, p.dbs = nil, nil
}

func (p *Project) fromgormConfig() fromgorm.Config {
	return fromgorm.Config{
		Namer:                                    p.Namer,
		DisableForeignKeyConstraintWhenMigrating: p.DisableForeignKeyConstraintWhenMigrating,
		IgnoreRelationshipsWhenMigrating:         p.IgnoreRelationshipsWhenMigrating,
	}
}

// CurrentState is ProjectState.from_apps(apps): the state of the current
// model definitions.
func (p *Project) CurrentState() (*m.ProjectState, error) {
	var apps []fromgorm.App
	for _, a := range p.Apps {
		apps = append(apps, fromgorm.App{Label: a.Label, Models: a.Models})
	}
	return fromgorm.ProjectState(apps, p.fromgormConfig())
}

// RealModels returns the current model states of the given apps (the
// "real apps" without migrations).
func (p *Project) RealModels(apps map[string]bool) ([]*m.ModelState, error) {
	if len(apps) == 0 {
		return nil, nil
	}
	st, err := p.CurrentState()
	if err != nil {
		return nil, err
	}
	var out []*m.ModelState
	for _, k := range st.SortedKeys() {
		if apps[k.App] {
			out = append(out, st.Models[k])
		}
	}
	return out, nil
}

// LoaderConfig configures a loader over the compiled migrations.
func (p *Project) LoaderConfig(rec loader.Applied, ignoreNoMigrations bool) loader.Config {
	var specs []loader.AppSpec
	for _, a := range p.Apps {
		specs = append(specs, loader.AppSpec{Label: a.Label, Dir: a.MigrationsDir, Disabled: a.Disabled})
	}
	return loader.Config{Apps: specs, Recorder: rec, IgnoreNoMigrations: ignoreNoMigrations, ReplaceMigrations: true}
}

// checkMigratedApp is the guard the migration commands apply to their
// app_label argument: the app must be installed and must have migrations.
// noMigrationsSuffix is appended to the second message, which only
// squashmigrations extends. The loader is built by newLoader only once the
// label is known, so a typo is reported before the migrations are read.
func (p *Project) checkMigratedApp(newLoader func() (*loader.Loader, error), label, noMigrationsSuffix string) (*loader.Loader, error) {
	if p.App(label) == nil {
		return nil, Errorf("no app named %q; is it in the project's Apps?", label)
	}
	l, err := newLoader()
	if err != nil {
		return nil, err
	}
	if !l.MigratedApps[label] {
		return nil, Errorf("app '%s' does not have migrations%s", label, noMigrationsSuffix)
	}
	return l, nil
}

// Loader builds a loader; conn may be nil (no database).
func (p *Project) Loader(conn *base.Conn, ignoreNoMigrations bool) (*loader.Loader, error) {
	var rec loader.Applied
	if conn != nil {
		rec = newRecorder(conn)
	}
	return loader.New(p.LoaderConfig(rec, ignoreNoMigrations))
}

// MigrationsDir reports where the migrations of an app live, for the
// questioner.
func (p *Project) MigrationsDir(label string) (string, bool, bool) {
	a := p.App(label)
	if a == nil {
		return "", false, false
	}
	return a.MigrationsDir, a.Disabled, true
}

// relPath renders path relative to the working directory when it is
// inside it (Django prints paths relative to cwd).
func relPath(env *Env, path string) string {
	getwd := env.Getwd
	if getwd == nil {
		getwd = os.Getwd
	}
	wd, err := getwd()
	if err != nil {
		return path
	}
	rel, err := filepath.Rel(wd, path)
	if err != nil || strings.HasPrefix(rel, "..") {
		return path
	}
	return rel
}
