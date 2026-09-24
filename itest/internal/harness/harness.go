// Package harness drives gormgate's migration machinery in-process against
// a real database: autodetect changes of gorm models, apply and unapply
// them, and introspect the result.
package harness

import (
	"context"
	"sort"
	"testing"

	"gorm.io/gorm"

	"github.com/ctolon/gormgate/backends/base"
	"github.com/ctolon/gormgate/internal/autodetector"
	"github.com/ctolon/gormgate/internal/executor"
	"github.com/ctolon/gormgate/internal/fromgorm"
	"github.com/ctolon/gormgate/internal/loader"
	"github.com/ctolon/gormgate/internal/questioner"
	m "github.com/ctolon/gormgate/migrations"

	_ "github.com/ctolon/gormgate/backends/postgresql"
)

// Project is an in-memory migration registry bound to one database.
type Project struct {
	T    testing.TB
	DB   *gorm.DB
	Conn *base.Conn
	Apps []string
	regs map[string][]*m.Migration
}

// New pins a connection of db.
func New(t testing.TB, db *gorm.DB, apps ...string) *Project {
	t.Helper()
	c, err := base.Open(context.Background(), "default", db, nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return &Project{T: t, DB: db, Conn: c, Apps: apps, regs: map[string][]*m.Migration{}}
}

func (p *Project) loaderConfig() loader.Config {
	var specs []loader.AppSpec
	for _, a := range p.Apps {
		specs = append(specs, loader.AppSpec{Label: a})
	}
	return loader.Config{
		Apps:              specs,
		Registered:        func(app string) []*m.Migration { return p.regs[app] },
		Compiled:          func(string) bool { return true },
		ReplaceMigrations: true,
		SkipDiskCheck:     true,
	}
}

// Register adds hand-written migrations.
func (p *Project) Register(migs ...*m.Migration) {
	for _, mig := range migs {
		p.regs[mig.App] = append(p.regs[mig.App], mig)
		sort.Slice(p.regs[mig.App], func(i, j int) bool { return p.regs[mig.App][i].Name < p.regs[mig.App][j].Name })
	}
}

// MakeMigrations autodetects the changes from the current migrations to
// the given models and registers the new migrations; it returns them.
func (p *Project) MakeMigrations(q questioner.Questioner, apps ...fromgorm.App) []*m.Migration {
	p.T.Helper()
	l, err := loader.New(p.loaderConfig())
	if err != nil {
		p.T.Fatalf("loader: %v", err)
	}
	from, err := l.ProjectState(nil, true, nil)
	if err != nil {
		p.T.Fatalf("project state: %v", err)
	}
	to, err := fromgorm.ProjectState(apps, fromgorm.Config{})
	if err != nil {
		p.T.Fatalf("fromgorm: %v", err)
	}
	if q == nil {
		specified := map[string]bool{}
		for _, a := range p.Apps {
			specified[a] = true
		}
		q = &questioner.Base{SpecifiedApps: specified}
	}
	changes, err := autodetector.New(from, to, q).ChangesFor(l.Graph, autodetector.ChangesOptions{})
	if err != nil {
		p.T.Fatalf("autodetector: %v", err)
	}
	var out []*m.Migration
	for _, app := range p.Apps {
		for _, mig := range changes[app] {
			p.Register(mig)
			out = append(out, mig)
		}
	}
	return out
}

// Executor builds an executor over the registered migrations.
func (p *Project) Executor() *executor.Executor {
	p.T.Helper()
	e, err := executor.New(p.Conn, p.loaderConfig(), nil, nil)
	if err != nil {
		p.T.Fatalf("executor: %v", err)
	}
	return e
}

// Migrate migrates to targets (nil: all leaf nodes).
func (p *Project) Migrate(targets ...m.Key) error {
	e := p.Executor()
	if targets == nil {
		targets = e.Loader.Graph.LeafNodes("")
	}
	_, err := e.MigrateTo(executor.MigrateOptions{Targets: targets})
	return err
}

// MustMigrate is Migrate that fails the test.
func (p *Project) MustMigrate(targets ...m.Key) {
	p.T.Helper()
	if err := p.Migrate(targets...); err != nil {
		p.T.Fatalf("migrate %v: %v", targets, err)
	}
}

// SQL collects the forwards (or backwards) SQL of one migration.
func (p *Project) SQL(app, name string, backwards bool) []string {
	p.T.Helper()
	e := p.Executor()
	mig, ok := e.Loader.GetMigration(app, name)
	if !ok {
		p.T.Fatalf("no migration %s.%s", app, name)
	}
	plan := []m.PlanStep{{Migration: mig, Backwards: backwards}}
	if backwards {
		// sqlmigrate --backwards collects against the state after the
		// migration.
		plan = []m.PlanStep{{Migration: mig, Backwards: true}}
	}
	sqls, err := e.CollectSQL(plan)
	if err != nil {
		p.T.Fatalf("collect sql: %v", err)
	}
	return sqls
}
