// Package executor runs migration plans: it works out which migrations to
// apply or unapply to reach a target, runs their operations through the
// backend's schema editor, and records the result.
//
// django: db/migrations/executor.py
package executor

import (
	"strings"

	"github.com/ctolon/gormgate/backends/base"
	"github.com/ctolon/gormgate/internal/graph"
	"github.com/ctolon/gormgate/internal/loader"
	"github.com/ctolon/gormgate/internal/recorder"
	m "github.com/ctolon/gormgate/migrations"
)

// Event is a step an Executor reports through its Progress callback.
type Event int

// The events. Each start is followed by the matching success. mig is nil
// for the render events, which report building the initial model state.
const (
	RenderStart Event = iota
	RenderSuccess
	ApplyStart
	ApplySuccess
	UnapplyStart
	UnapplySuccess
)

// String names the event, for a test failure or a log line.
func (e Event) String() string {
	switch e {
	case RenderStart:
		return "render start"
	case RenderSuccess:
		return "render success"
	case ApplyStart:
		return "apply start"
	case ApplySuccess:
		return "apply success"
	case UnapplyStart:
		return "unapply start"
	case UnapplySuccess:
		return "unapply success"
	}
	return "unknown event"
}

// Progress is called as the executor works. fake reports whether the
// migration is only being recorded, not run.
type Progress func(ev Event, mig *m.Migration, fake bool)

// Executor applies and unapplies the migrations of one connection.
//
// django: executor.py MigrationExecutor
type Executor struct {
	Conn       *base.Conn
	Loader     *loader.Loader
	Recorder   *recorder.Recorder
	Progress   Progress
	RealModels []*m.ModelState
	cfg        loader.Config
}

// New builds an executor; cfg configures its loader (the recorder is set
// here).
func New(conn *base.Conn, cfg loader.Config, realModels []*m.ModelState, progress Progress) (*Executor, error) {
	rec := recorder.New(conn)
	cfg.Recorder = rec
	l, err := loader.New(cfg)
	if err != nil {
		return nil, err
	}
	return &Executor{Conn: conn, Loader: l, Recorder: rec, Progress: progress, RealModels: realModels, cfg: cfg}, nil
}

// Target names a migration to migrate to. A Target whose Name is empty
// means "unapply every migration of this app".
type Target = m.Key

// MigrationPlan returns the (migration, backwards) steps to reach targets.
//
// django: executor.py MigrationExecutor.migration_plan
func (e *Executor) MigrationPlan(targets []Target, cleanStart bool) ([]m.PlanStep, error) {
	var plan []m.PlanStep
	applied := map[m.Key]bool{}
	if !cleanStart {
		for k := range e.Loader.Applied {
			applied[k] = true
		}
	}
	g := e.Loader.Graph
	for _, target := range targets {
		switch {
		case target.Name == "":
			for _, root := range g.RootNodes("") {
				if root.App != target.App {
					continue
				}
				bp, err := g.BackwardsPlan(root)
				if err != nil {
					return nil, err
				}
				for _, k := range bp {
					if applied[k] {
						plan = append(plan, m.PlanStep{Migration: g.Nodes[k], Backwards: true})
						delete(applied, k)
					}
				}
			}
		case e.cfg.ReplaceMigrations && g.NodeMap[target] == nil:
			// A migration replaced by a squash was targeted, so it
			// is not in the graph. Rebuild the loader without
			// replacements and plan again; the executor keeps that
			// loader for the rest of its life.
			e.cfg.ReplaceMigrations = false
			l, err := loader.New(e.cfg)
			if err != nil {
				return nil, err
			}
			e.Loader = l
			return e.MigrationPlan(targets, cleanStart)
		case applied[target]:
			var next []m.Key
			for c := range g.NodeMap[target].Children {
				if c.Key.App == target.App {
					next = append(next, c.Key)
				}
			}
			graph.SortKeys(next)
			for _, n := range next {
				bp, err := g.BackwardsPlan(n)
				if err != nil {
					return nil, err
				}
				for _, k := range bp {
					if applied[k] {
						plan = append(plan, m.PlanStep{Migration: g.Nodes[k], Backwards: true})
						delete(applied, k)
					}
				}
			}
		default:
			fp, err := g.ForwardsPlan(target)
			if err != nil {
				return nil, err
			}
			for _, k := range fp {
				if !applied[k] {
					plan = append(plan, m.PlanStep{Migration: g.Nodes[k], Backwards: false})
					applied[k] = true
				}
			}
		}
	}
	return plan, nil
}

func (e *Executor) newState() *m.ProjectState {
	st := m.NewProjectState()
	st.RealApps = e.Loader.UnmigratedApps
	st.RealModels = e.RealModels
	return st
}

// CreateProjectState returns a state with the unmigrated apps and, when
// withApplied, the applied migrations.
//
// django: executor.py MigrationExecutor._create_project_state
func (e *Executor) CreateProjectState(withApplied bool) (*m.ProjectState, error) {
	st := e.newState()
	if !withApplied {
		return st, nil
	}
	full, err := e.MigrationPlan(e.Loader.Graph.LeafNodes(""), true)
	if err != nil {
		return nil, err
	}
	for _, s := range full {
		if e.Loader.Applied[s.Migration.Key()] {
			if _, err := s.Migration.MutateState(st, false); err != nil {
				return nil, err
			}
		}
	}
	return st, nil
}

// MigrateOptions are the arguments of MigrateTo.
type MigrateOptions struct {
	// Targets are the migrations to migrate to. They are only used when
	// Plan is nil.
	Targets []Target
	// Plan is the plan to run. A nil Plan means "work it out from
	// Targets"; a non-nil empty Plan means "there is nothing to do", which
	// is not the same thing.
	Plan []m.PlanStep
	// State is the project state the plan starts from; nil builds it.
	State *m.ProjectState
	// Fake records the migrations without running them.
	Fake bool
	// FakeInitial fakes an initial migration whose tables already exist.
	FakeInitial bool
}

// MigrateTo migrates the database to opts.Targets, or runs opts.Plan when
// one is given.
//
// django: executor.py MigrationExecutor.migrate
func (e *Executor) MigrateTo(opts MigrateOptions) (*m.ProjectState, error) {
	targets, plan, state := opts.Targets, opts.Plan, opts.State
	fake, fakeInitial := opts.Fake, opts.FakeInitial
	planGiven := plan != nil
	if planGiven && len(plan) == 0 {
		ok, err := e.Recorder.HasTable()
		if err != nil {
			return nil, err
		}
		if !ok {
			return e.CreateProjectState(false)
		}
	} else if err := e.Recorder.EnsureSchema(); err != nil {
		return nil, err
	}
	if !planGiven {
		var err error
		if plan, err = e.MigrationPlan(targets, false); err != nil {
			return nil, err
		}
	}
	full, err := e.MigrationPlan(e.Loader.Graph.LeafNodes(""), true)
	if err != nil {
		return nil, err
	}
	allForwards, allBackwards := true, true
	for _, s := range plan {
		if s.Backwards {
			allForwards = false
		} else {
			allBackwards = false
		}
	}
	switch {
	case len(plan) == 0:
		if state == nil {
			state, err = e.CreateProjectState(true)
		}
	case allForwards == allBackwards:
		return nil, &m.InvalidMigrationPlan{Msg: "migration plans with both forwards and backwards migrations are not supported. Please split your migration process into separate plans of only forwards OR backwards migrations", Plan: plan}
	case allForwards:
		if state == nil {
			state, err = e.CreateProjectState(true)
		}
		if err == nil {
			state, err = e.migrateAllForwards(state, plan, full, fake, fakeInitial)
		}
	default:
		state, err = e.migrateAllBackwards(plan, full, fake)
	}
	if err != nil {
		return nil, err
	}
	return state, e.CheckReplacements()
}

func (e *Executor) progress(ev Event, mig *m.Migration, fake bool) {
	if e.Progress != nil {
		e.Progress(ev, mig, fake)
	}
}

// django: executor.py MigrationExecutor._migrate_all_forwards
func (e *Executor) migrateAllForwards(state *m.ProjectState, plan, full []m.PlanStep, fake, fakeInitial bool) (*m.ProjectState, error) {
	toRun := map[m.Key]bool{}
	for _, s := range plan {
		toRun[s.Migration.Key()] = true
	}
	rendered := state.Rendered()
	for _, s := range full {
		if len(toRun) == 0 {
			break
		}
		if !toRun[s.Migration.Key()] {
			continue
		}
		if !rendered {
			e.progress(RenderStart, nil, false)
			if _, err := state.Apps(); err != nil {
				return nil, err
			}
			e.progress(RenderSuccess, nil, false)
			rendered = true
		}
		var err error
		if state, err = e.ApplyMigration(state, s.Migration, fake, fakeInitial); err != nil {
			return nil, err
		}
		delete(toRun, s.Migration.Key())
	}
	return state, nil
}

// django: executor.py MigrationExecutor._migrate_all_backwards
func (e *Executor) migrateAllBackwards(plan, full []m.PlanStep, fake bool) (*m.ProjectState, error) {
	toRun := map[m.Key]bool{}
	for _, s := range plan {
		toRun[s.Migration.Key()] = true
	}
	states := map[m.Key]*m.ProjectState{}
	state := e.newState()
	applied := map[m.Key]bool{}
	for k := range e.Loader.Applied {
		if e.Loader.Graph.Nodes[k] != nil {
			applied[k] = true
		}
	}
	e.progress(RenderStart, nil, false)
	for _, s := range full {
		if len(toRun) == 0 {
			break
		}
		k := s.Migration.Key()
		if toRun[k] {
			if _, err := state.Apps(); err != nil {
				return nil, err
			}
			states[k] = state
			var err error
			if state, err = s.Migration.MutateState(state, true); err != nil {
				return nil, err
			}
			delete(toRun, k)
		} else if applied[k] {
			if _, err := s.Migration.MutateState(state, false); err != nil {
				return nil, err
			}
		}
	}
	e.progress(RenderSuccess, nil, false)
	for _, s := range plan {
		if _, err := e.UnapplyMigration(states[s.Migration.Key()], s.Migration, fake); err != nil {
			return nil, err
		}
		delete(applied, s.Migration.Key())
	}
	last := plan[len(plan)-1].Migration.Key()
	state = states[last].Clone()
	for i, s := range full {
		if s.Migration.Key() != last {
			continue
		}
		for _, r := range full[i:] {
			if applied[r.Migration.Key()] {
				if _, err := r.Migration.MutateState(state, false); err != nil {
					return nil, err
				}
			}
		}
		break
	}
	return state, nil
}

// ApplyMigration runs a migration forwards.
//
// django: executor.py MigrationExecutor.apply_migration
func (e *Executor) ApplyMigration(state *m.ProjectState, mig *m.Migration, fake, fakeInitial bool) (*m.ProjectState, error) {
	recorded := false
	e.progress(ApplyStart, mig, fake)
	if !fake {
		if fakeInitial {
			applied, after, err := e.DetectSoftApplied(state, mig)
			if err != nil {
				return nil, err
			}
			state = after
			if applied {
				fake = true
			}
		}
		if !fake {
			ed := e.Conn.Backend.NewEditor(e.Conn, false, mig.IsAtomic())
			err := ed.Begin()
			if err == nil {
				state, err = mig.Apply(state, ed)
			}
			if err == nil && len(ed.Deferred()) == 0 {
				err = e.RecordMigration(mig.App, mig.Name, true)
				recorded = err == nil
			}
			if err = ed.Finish(err); err != nil {
				return nil, err
			}
		}
	}
	if !recorded {
		if err := e.RecordMigration(mig.App, mig.Name, true); err != nil {
			return nil, err
		}
	}
	e.progress(ApplySuccess, mig, fake)
	return state, nil
}

// RecordMigration records a migration and, for squashed ones, the
// migrations it replaces.
//
// django: executor.py MigrationExecutor.record_migration
func (e *Executor) RecordMigration(app, name string, forward bool) error {
	if mig, ok := e.Loader.DiskMigrations[m.Key{App: app, Name: name}]; ok {
		for _, r := range mig.Replaces {
			if err := e.RecordMigration(r.App, r.Name, forward); err != nil {
				return err
			}
		}
	}
	if forward {
		return e.Recorder.RecordApplied(app, name)
	}
	return e.Recorder.RecordUnapplied(app, name)
}

// UnapplyMigration runs a migration backwards.
//
// django: executor.py MigrationExecutor.unapply_migration
func (e *Executor) UnapplyMigration(state *m.ProjectState, mig *m.Migration, fake bool) (*m.ProjectState, error) {
	e.progress(UnapplyStart, mig, fake)
	if !fake {
		ed := e.Conn.Backend.NewEditor(e.Conn, false, mig.IsAtomic())
		err := ed.Begin()
		if err == nil {
			state, err = mig.Unapply(state, ed)
		}
		if err = ed.Finish(err); err != nil {
			return nil, err
		}
	}
	if err := e.RecordMigration(mig.App, mig.Name, false); err != nil {
		return nil, err
	}
	e.progress(UnapplySuccess, mig, fake)
	return state, nil
}

// CheckReplacements marks squashed migrations applied when all the
// migrations they replace are.
//
// django: executor.py MigrationExecutor.check_replacements
func (e *Executor) CheckReplacements() error {
	keys, err := e.Recorder.AppliedMigrations()
	if err != nil {
		return err
	}
	applied := map[m.Key]bool{}
	for _, k := range keys {
		applied[k] = true
	}
	var reps []m.Key
	for k := range e.Loader.Replacements {
		reps = append(reps, k)
	}
	graph.SortKeys(reps)
	for _, k := range reps {
		if !applied[k] && e.Loader.AllReplacedApplied(k, applied) {
			if err := e.Recorder.RecordApplied(k.App, k.Name); err != nil {
				return err
			}
		}
	}
	return nil
}

// DetectSoftApplied reports whether an initial migration's tables and
// columns already exist (--fake-initial).
//
// django: executor.py MigrationExecutor.detect_soft_applied
func (e *Executor) DetectSoftApplied(state *m.ProjectState, mig *m.Migration) (bool, *m.ProjectState, error) {
	if mig.Initial == nil {
		for _, d := range mig.Dependencies {
			if d.App == mig.App {
				return false, state, nil
			}
		}
	} else if !*mig.Initial {
		return false, state, nil
	}
	var after *m.ProjectState
	var err error
	if state == nil {
		after, err = e.Loader.ProjectState([]m.Key{mig.Key()}, true, e.RealModels)
	} else {
		after, err = mig.MutateState(state, true)
	}
	if err != nil {
		return false, nil, err
	}
	apps, err := after.Apps()
	if err != nil {
		return false, nil, err
	}
	fold := e.Conn.Backend.Features.IgnoresTableNameCase
	intro := e.Conn.Introspection()
	tables, err := intro.TableNames(false)
	if err != nil {
		return false, nil, err
	}
	norm := func(s string) string {
		if fold {
			return strings.ToLower(s)
		}
		return s
	}
	existing := map[string]bool{}
	for _, t := range tables {
		existing[norm(t.Name)] = true
	}
	skip := func(model *m.Model) bool {
		return !model.Managed() || !e.Conn.AllowMigrate(mig.App, m.Hints{Model: model, ModelName: strings.ToLower(model.Name)})
	}
	foundCreate, foundAdd := false, false
	for _, op := range mig.Operations {
		switch o := op.(type) {
		case *m.CreateModel:
			model, err := apps.GetModel(mig.App, o.Name)
			if err != nil {
				return false, nil, err
			}
			if skip(model) {
				continue
			}
			if !existing[norm(intro.IdentifierConverter(model.Table))] {
				return false, state, nil
			}
			foundCreate = true
		case *m.AddField:
			model, err := apps.GetModel(mig.App, o.ModelName)
			if err != nil {
				return false, nil, err
			}
			if skip(model) {
				continue
			}
			cols, err := intro.TableDescription(model.Table)
			if err != nil {
				return false, nil, err
			}
			found := false
			for _, c := range cols {
				if norm(c.Name) == norm(intro.IdentifierConverter(o.Name)) {
					found = true
					break
				}
			}
			if !found {
				return false, state, nil
			}
			foundAdd = true
		}
	}
	return foundCreate || foundAdd, after, nil
}

// CollectSQL returns the SQL the plan would run on the executor's
// connection, without running it.
func (e *Executor) CollectSQL(plan []m.PlanStep) ([]string, error) {
	return CollectSQL(e.Conn, e.Loader, e.RealModels, plan)
}

// CollectSQL runs a plan against schema editors in collect mode and returns
// the statements they rendered. It is how sqlmigrate prints a migration
// without touching the database.
//
// django: loader.py MigrationLoader.collect_sql
func CollectSQL(conn *base.Conn, l *loader.Loader, realModels []*m.ModelState, plan []m.PlanStep) ([]string, error) {
	var out []string
	var state *m.ProjectState
	for _, s := range plan {
		ed := conn.Backend.NewEditor(conn, true, s.Migration.IsAtomic())
		if err := ed.Begin(); err != nil {
			// Every other exit from this loop closes the editor; a failed
			// Begin must too, so that a half-opened one is not leaked.
			return nil, ed.Finish(err)
		}
		var err error
		if state == nil {
			state, err = l.ProjectState([]m.Key{s.Migration.Key()}, false, realModels)
		}
		if err == nil {
			if s.Backwards {
				state, err = s.Migration.Unapply(state, ed)
			} else {
				state, err = s.Migration.Apply(state, ed)
			}
		}
		if err = ed.Finish(err); err != nil {
			return nil, err
		}
		out = append(out, ed.CollectedSQL()...)
	}
	return out, nil
}
