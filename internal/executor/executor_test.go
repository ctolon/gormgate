package executor

import (
	"errors"
	"strings"
	"testing"

	"github.com/ctolon/gormgate/internal/graph"
	"github.com/ctolon/gormgate/internal/loader"
	m "github.com/ctolon/gormgate/migrations"
)

// Port of Django 6.0's tests/migrations/test_executor.py. Every test keeps
// the Django name it was ported from; the `// django:` comment gives the
// original.
//
// Django runs these against a live database. Here the executor, loader,
// recorder and operations are the real gormgate code, running against the
// in-memory backend in fake_backend_test.go: the fake schema editor maintains
// a table -> columns map that the fake introspection reads back, so the
// "assertTableExists" assertions have the same meaning as Django's.

// django: tests/migrations/test_executor.py ExecutorTests.test_run
func TestExecutor_Run(t *testing.T) {
	conn, db := newFakeConn(t)
	e := newExecutor(t, conn, sets("migrations", testMigrations()), nil)

	// Let's look at the plan first and make sure it's up to scratch.
	plan, err := e.MigrationPlan([]Target{key("migrations", "0002_second")}, false)
	if err != nil {
		t.Fatal(err)
	}
	assertPlan(t, plan, "migrations.0001_initial forwards", "migrations.0002_second forwards")
	// Django compares the plan entries against loader.graph.nodes.
	if plan[0].Migration != e.Loader.Graph.Nodes[key("migrations", "0001_initial")] {
		t.Error("plan entry is not the graph's migration object")
	}

	// Were the tables there before?
	assertTableNotExists(t, db, "migrations_author")
	assertTableNotExists(t, db, "migrations_book")

	mustMigrate(t, e, []Target{key("migrations", "0002_second")}, false)

	assertTableExists(t, db, "migrations_author")
	assertTableExists(t, db, "migrations_book")
	// Tribble was created by 0001 and deleted by 0002.
	assertTableNotExists(t, db, "migrations_tribble")

	// Both migrations are now recorded in the migrations table.
	applied := appliedSet(t, e)
	if !applied[key("migrations", "0001_initial")] || !applied[key("migrations", "0002_second")] {
		t.Fatalf("applied migrations = %v, want both recorded", applied)
	}

	// Rebuild the graph to reflect the new DB state.
	mustBuildGraph(t, e)

	// Undo what we did.
	plan, err = e.MigrationPlan([]Target{key("migrations", "")}, false)
	if err != nil {
		t.Fatal(err)
	}
	assertPlan(t, plan, "migrations.0002_second backwards", "migrations.0001_initial backwards")
	mustMigrate(t, e, []Target{key("migrations", "")}, false)

	assertTableNotExists(t, db, "migrations_author")
	assertTableNotExists(t, db, "migrations_book")
	// Unapplying 0002 recreates Tribble, unapplying 0001 drops it again.
	assertTableNotExists(t, db, "migrations_tribble")

	if applied = appliedSet(t, e); len(applied) != 0 {
		t.Errorf("migrations still recorded as applied: %v", applied)
	}
}

// django: tests/migrations/test_executor.py ExecutorTests.test_run_with_squashed
func TestExecutor_RunWithSquashed(t *testing.T) {
	conn, db := newFakeConn(t)
	e := newExecutor(t, conn, sets("migrations", testMigrationsSquashed()), nil)

	// Check our leaf node is the squashed one.
	var leaves []string
	for _, k := range e.Loader.Graph.LeafNodes("migrations") {
		leaves = append(leaves, k.Name)
	}
	if !equalStrings(leaves, []string{"0001_squashed_0002"}) {
		t.Fatalf("leaf nodes = %v, want [0001_squashed_0002]", leaves)
	}

	plan, err := e.MigrationPlan([]Target{key("migrations", "0001_squashed_0002")}, false)
	if err != nil {
		t.Fatal(err)
	}
	assertPlan(t, plan, "migrations.0001_squashed_0002 forwards")

	assertTableNotExists(t, db, "migrations_author")
	assertTableNotExists(t, db, "migrations_book")

	mustMigrate(t, e, []Target{key("migrations", "0001_squashed_0002")}, false)

	assertTableExists(t, db, "migrations_author")
	assertTableExists(t, db, "migrations_book")

	mustBuildGraph(t, e)

	// Undoing should also just use the squashed migration.
	plan, err = e.MigrationPlan([]Target{key("migrations", "")}, false)
	if err != nil {
		t.Fatal(err)
	}
	assertPlan(t, plan, "migrations.0001_squashed_0002 backwards")
	mustMigrate(t, e, []Target{key("migrations", "")}, false)

	assertTableNotExists(t, db, "migrations_author")
	assertTableNotExists(t, db, "migrations_book")
}

// django: tests/migrations/test_executor.py
// ExecutorTests.test_migrate_backward_to_squashed_migration
func TestExecutor_MigrateBackwardToSquashedMigration(t *testing.T) {
	conn, db := newFakeConn(t)
	e := newExecutor(t, conn, sets("migrations", testMigrationsSquashed()), nil)

	assertTableNotExists(t, db, "migrations_author")
	assertTableNotExists(t, db, "migrations_book")
	mustMigrate(t, e, []Target{key("migrations", "0001_squashed_0002")}, false)
	assertTableExists(t, db, "migrations_author")
	assertTableExists(t, db, "migrations_book")
	mustBuildGraph(t, e)

	// Migrating backward to a migration replaced by the squash reloads the
	// graph without replacements.
	mustMigrate(t, e, []Target{key("migrations", "0001_initial")}, false)
	assertTableExists(t, db, "migrations_author")
	assertTableNotExists(t, db, "migrations_book")

	// Unmigrate everything.
	e = newExecutor(t, conn, sets("migrations", testMigrationsSquashed()), nil)
	mustMigrate(t, e, []Target{key("migrations", "")}, false)
	assertTableNotExists(t, db, "migrations_author")
	assertTableNotExists(t, db, "migrations_book")
}

// django: tests/migrations/test_executor.py ExecutorTests.test_non_atomic_migration
//
// Django's fixture runs a RunPython that inserts a Publisher row and then
// raises; here the RunGo callback records that it ran and the following
// operation fails. Because the migration is not atomic, the work done before
// the failure survives.
func TestExecutor_NonAtomicMigration(t *testing.T) {
	conn, db := newFakeConn(t)
	ran := false
	mig := &m.Migration{
		App: "migrations", Name: "0001_initial", Initial: m.Ptr(true),
		Atomic: m.Ptr(false),
		Operations: []m.Operation{
			&m.CreateModel{Name: "Publisher", Table: "migrations_publisher", Fields: m.Fields{
				{Name: "id", Field: autoPK()},
				{Name: "name", Field: charField(100)},
			}},
			&m.RunGo{Code: func(*m.Apps, m.SchemaEditor) error { ran = true; return nil }},
			&m.CreateModel{Name: "Book", Table: "migrations_book", Fields: m.Fields{
				{Name: "id", Field: autoPK()},
			}},
		},
	}
	db.failCreateTable["migrations_book"] = true
	e := newExecutor(t, conn, sets("migrations", []*m.Migration{mig}), nil)

	_, err := e.MigrateTo(MigrateOptions{Targets: []Target{key("migrations", "0001_initial")}})
	if err == nil || !strings.Contains(err.Error(), "cannot create table migrations_book") {
		t.Fatalf("Migrate error = %v, want the CreateModel failure", err)
	}
	if !ran {
		t.Error("the RunGo operation did not run")
	}
	// The non-atomic migration left the work done before the failure behind.
	assertTableExists(t, db, "migrations_publisher")
	assertTableNotExists(t, db, "migrations_book")
	if appliedSet(t, e)[key("migrations", "0001_initial")] {
		t.Error("the failed migration was recorded as applied")
	}
}

// django: tests/migrations/test_executor.py
// ExecutorTests.test_atomic_operation_in_non_atomic_migration
func TestExecutor_AtomicOperationInNonAtomicMigration(t *testing.T) {
	conn, db := newFakeConn(t)
	boom := errors.New("Abort migration")
	// The second operation is atomic and fails: its work must be rolled back
	// while the first operation's survives, because the migration itself is
	// not atomic.
	mig := &m.Migration{
		App: "migrations", Name: "0001_initial", Initial: m.Ptr(true),
		Atomic: m.Ptr(false),
		Operations: []m.Operation{
			&m.CreateModel{Name: "Editor", Table: "migrations_editor", Fields: m.Fields{
				{Name: "id", Field: autoPK()},
			}},
			&failingAtomicOp{err: boom, table: "migrations_atomic"},
		},
	}

	e := newExecutor(t, conn, sets("migrations", []*m.Migration{mig}), nil)
	_, err := e.MigrateTo(MigrateOptions{Targets: []Target{key("migrations", "0001_initial")}})
	if !errors.Is(err, boom) {
		t.Fatalf("Migrate error = %v, want %v", err, boom)
	}
	assertTableExists(t, db, "migrations_editor")
	// The atomic operation created its table and then failed; the schema
	// editor's transaction rolled that back.
	assertTableNotExists(t, db, "migrations_atomic")
}

// failingAtomicOp creates a table and then fails, inside its own transaction
// (Django's atomic=True operation in a non-atomic migration).
type failingAtomicOp struct {
	m.Operation
	err   error
	table string
}

func (o *failingAtomicOp) StateForwards(app string, s *m.ProjectState) error {
	s.AddModel(&m.ModelState{App: app, Name: "Atomic", Table: o.table,
		Fields: m.Fields{{Name: "id", Field: autoPK()}}})
	return nil
}

func (o *failingAtomicOp) DatabaseForwards(app string, ed m.SchemaEditor, from, to *m.ProjectState) error {
	apps, err := to.Apps()
	if err != nil {
		return err
	}
	model, err := apps.GetModel(app, "Atomic")
	if err != nil {
		return err
	}
	if err := ed.CreateModel(model); err != nil {
		return err
	}
	return o.err
}

func (o *failingAtomicOp) DatabaseBackwards(string, m.SchemaEditor, *m.ProjectState, *m.ProjectState) error {
	return nil
}
func (o *failingAtomicOp) Describe() string                    { return "Failing atomic operation" }
func (o *failingAtomicOp) MigrationNameFragment() string       { return "" }
func (o *failingAtomicOp) Category() m.Category                { return m.CategoryMixed }
func (o *failingAtomicOp) ReferencesModel(string, string) bool { return true }
func (o *failingAtomicOp) ReferencesField(string, string, string) bool {
	return true
}
func (o *failingAtomicOp) Reduce(m.Operation, string) ([]m.Operation, m.ReduceKind) {
	return nil, m.ReduceBlock
}
func (o *failingAtomicOp) Reversible() bool   { return true }
func (o *failingAtomicOp) ReducesToSQL() bool { return true }
func (o *failingAtomicOp) Atomic() *bool      { return m.Ptr(true) }
func (o *failingAtomicOp) Elidable() bool     { return false }

// django: tests/migrations/test_executor.py ExecutorTests.test_empty_plan
func TestExecutor_EmptyPlan(t *testing.T) {
	conn, _ := newFakeConn(t)
	all := sets("migrations", testMigrations(), "migrations2", testMigrations2())
	e := newExecutor(t, conn, all, nil)

	targets := []Target{key("migrations", "0002_second"), key("migrations2", "0001_initial")}
	plan, err := e.MigrationPlan(targets, false)
	if err != nil {
		t.Fatal(err)
	}
	assertPlan(t, plan,
		"migrations.0001_initial forwards",
		"migrations.0002_second forwards",
		"migrations2.0001_initial forwards",
	)

	// Fake-apply all migrations.
	mustMigrate(t, e, targets, true)
	mustBuildGraph(t, e)

	// Now plan a second time and make sure it's empty.
	plan, err = e.MigrationPlan(targets, false)
	if err != nil {
		t.Fatal(err)
	}
	assertPlan(t, plan)

	// The resulting state should include applied migrations.
	state := mustMigrate(t, e, targets, false)
	for _, k := range []m.ModelKey{
		{App: "migrations", Model: "book"},
		{App: "migrations", Model: "author"},
		{App: "migrations2", Model: "otherauthor"},
	} {
		if _, ok := state.Models[k]; !ok {
			t.Errorf("state is missing model %v", k)
		}
	}

	// Erase all the fake records.
	for _, k := range []m.Key{
		key("migrations2", "0001_initial"),
		key("migrations", "0002_second"),
		key("migrations", "0001_initial"),
	} {
		if err := e.Recorder.RecordUnapplied(k.App, k.Name); err != nil {
			t.Fatal(err)
		}
	}
	if applied := appliedSet(t, e); len(applied) != 0 {
		t.Errorf("records left behind: %v", applied)
	}
}

// django: tests/migrations/test_executor.py ExecutorTests.test_mixed_plan_not_supported
func TestExecutor_MixedPlanNotSupported(t *testing.T) {
	conn, db := newFakeConn(t)
	all := sets("migrations", testMigrations(), "migrations2", testMigrations2NoDeps())
	e := newExecutor(t, conn, all, nil)

	plan, err := e.MigrationPlan([]Target{key("migrations", "0002_second")}, false)
	if err != nil {
		t.Fatal(err)
	}
	assertPlan(t, plan, "migrations.0001_initial forwards", "migrations.0002_second forwards")
	if _, err := e.MigrateTo(MigrateOptions{Plan: plan}); err != nil {
		t.Fatal(err)
	}
	mustBuildGraph(t, e)

	applied := e.Loader.Applied
	if !applied[key("migrations", "0001_initial")] || !applied[key("migrations", "0002_second")] {
		t.Fatalf("migrations app not fully applied: %v", applied)
	}
	if applied[key("migrations2", "0001_initial")] {
		t.Fatal("migrations2 should not be applied yet")
	}

	// Generate a mixed plan.
	plan, err = e.MigrationPlan([]Target{key("migrations", ""), key("migrations2", "0001_initial")}, false)
	if err != nil {
		t.Fatal(err)
	}
	assertPlan(t, plan,
		"migrations.0002_second backwards",
		"migrations.0001_initial backwards",
		"migrations2.0001_initial forwards",
	)

	_, err = e.MigrateTo(MigrateOptions{Plan: plan})
	var bad *m.InvalidMigrationPlan
	if !errors.As(err, &bad) {
		t.Fatalf("Migrate error = %v, want InvalidMigrationPlan", err)
	}
	const msg = "migration plans with both forwards and backwards migrations are not " +
		"supported. Please split your migration process into separate plans of only " +
		"forwards OR backwards migrations"
	if bad.Msg != msg {
		t.Errorf("message = %q, want %q", bad.Msg, msg)
	}
	// Django asserts the offending plan is carried on the exception.
	assertPlan(t, bad.Plan,
		"migrations.0002_second backwards",
		"migrations.0001_initial backwards",
		"migrations2.0001_initial forwards",
	)

	mustBuildGraph(t, e)
	mustMigrate(t, e, []Target{key("migrations", ""), key("migrations2", "")}, false)
	assertTableNotExists(t, db, "migrations_author")
	assertTableNotExists(t, db, "migrations_book")
	assertTableNotExists(t, db, "migrations2_otherauthor")
}

// django: tests/migrations/test_executor.py ExecutorTests.test_soft_apply
func TestExecutor_SoftApply(t *testing.T) {
	conn, db := newFakeConn(t)
	// Django's fake_storer keeps the `fake` argument of the last progress
	// callback. apply_start reports the `fake` the caller asked for, while
	// apply_success reports it after detect_soft_applied may have flipped it,
	// so the last value is the one that matters.
	var lastFake *bool
	e := newExecutor(t, conn, sets("migrations", testMigrations()), func(_ Event, mig *m.Migration, fake bool) {
		if mig != nil {
			v := fake
			lastFake = &v
		}
	})
	assertFaked := func(want bool) {
		t.Helper()
		if lastFake == nil {
			t.Fatalf("no progress callback with a migration was made")
		}
		if *lastFake != want {
			t.Fatalf("faked = %v, want %v", *lastFake, want)
		}
	}

	assertTableNotExists(t, db, "migrations_author")
	assertTableNotExists(t, db, "migrations_tribble")

	plan, err := e.MigrationPlan([]Target{key("migrations", "0001_initial")}, false)
	if err != nil {
		t.Fatal(err)
	}
	assertPlan(t, plan, "migrations.0001_initial forwards")
	mustMigrate(t, e, []Target{key("migrations", "0001_initial")}, false)

	assertTableExists(t, db, "migrations_author")
	assertTableExists(t, db, "migrations_tribble")
	// We shouldn't have faked that one.
	assertFaked(false)

	mustBuildGraph(t, e)
	// Fake-reverse that.
	lastFake = nil
	mustMigrate(t, e, []Target{key("migrations", "")}, true)
	// The tables are still there.
	assertTableExists(t, db, "migrations_author")
	assertTableExists(t, db, "migrations_tribble")
	assertFaked(true)

	// Migrating forwards again without --fake-initial hits the database error
	// of re-creating an existing table.
	mustBuildGraph(t, e)
	plan, err = e.MigrationPlan([]Target{key("migrations", "0001_initial")}, false)
	if err != nil {
		t.Fatal(err)
	}
	assertPlan(t, plan, "migrations.0001_initial forwards")
	if _, err := e.MigrateTo(MigrateOptions{Targets: []Target{key("migrations", "0001_initial")}}); err == nil {
		t.Fatal("re-applying the initial migration should raise a database error")
	} else if !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("error = %v, want an 'already exists' database error", err)
	}

	// With fake_initial the executor detects the tables and fakes it.
	lastFake = nil
	if _, err := e.MigrateTo(MigrateOptions{Targets: []Target{key("migrations", "0001_initial")}, FakeInitial: true}); err != nil {
		t.Fatal(err)
	}
	assertFaked(true)

	// And migrate back to clean up the database.
	mustBuildGraph(t, e)
	mustMigrate(t, e, []Target{key("migrations", "")}, false)
	assertTableNotExists(t, db, "migrations_author")
	assertTableNotExists(t, db, "migrations_tribble")
}

// TestExecutor_DetectSoftAppliedAddField covers the AddField branch of
// detect_soft_applied. Django exercises it through
// ExecutorTests.test_detect_soft_applied_add_field_manytomanyfield, which
// relies on the implicit through table of a ManyToManyField; gormgate has no
// implicit through tables (a gorm many2many join table is an ordinary
// auto-created model with its own CreateModel), so the column branch of
// AddField is tested instead. The sequence of executor calls is Django's,
// including the stale loader.applied_migrations it relies on: the loader's
// applied set is only refreshed by BuildGraph, never by Migrate.
//
// django: tests/migrations/test_executor.py
// ExecutorTests.test_detect_soft_applied_add_field_manytomanyfield (adapted)
func TestExecutor_DetectSoftAppliedAddField(t *testing.T) {
	conn, db := newFakeConn(t)
	migs := []*m.Migration{
		{
			App: "migrations", Name: "0001_initial", Initial: m.Ptr(true),
			Operations: []m.Operation{
				&m.CreateModel{Name: "Project", Table: "migrations_project",
					Fields: m.Fields{{Name: "id", Field: autoPK()}}},
				&m.CreateModel{Name: "Task", Table: "migrations_task",
					Fields: m.Fields{{Name: "id", Field: autoPK()}}},
				&m.AddField{ModelName: "project", Name: "note", Field: charField(10)},
			},
		},
		{
			App: "migrations", Name: "0002_initial", Initial: m.Ptr(true),
			Dependencies: []m.Key{{App: "migrations", Name: "0001_initial"}},
			Operations: []m.Operation{
				&m.AddField{ModelName: "task", Name: "note", Field: charField(10)},
			},
		},
	}
	e := newExecutor(t, conn, sets("migrations", migs), nil)

	detect := func(name string) bool {
		t.Helper()
		mig, ok := e.Loader.GetMigration("migrations", name)
		if !ok {
			t.Fatalf("%s is not in the graph", name)
		}
		applied, _, err := e.DetectSoftApplied(nil, mig)
		if err != nil {
			t.Fatalf("DetectSoftApplied(%s): %v", name, err)
		}
		return applied
	}

	// Create the tables for 0001 but make it look like the migration hasn't
	// been applied.
	mustMigrate(t, e, []Target{key("migrations", "0001_initial")}, false)
	mustMigrate(t, e, []Target{key("migrations", "")}, true)
	assertTableExists(t, db, "migrations_project")
	assertTableExists(t, db, "migrations_task")

	// Table detection sees 0001 is applied but not 0002.
	if !detect("0001_initial") {
		t.Error("detect_soft_applied(0001_initial) = false, want true")
	}
	if detect("0002_initial") {
		t.Error("detect_soft_applied(0002_initial) = true, want false")
	}

	// Create the columns for both migrations but make it look like neither
	// has been applied.
	mustBuildGraph(t, e)
	mustMigrate(t, e, []Target{key("migrations", "0001_initial")}, true)
	mustMigrate(t, e, []Target{key("migrations", "0002_initial")}, false)
	mustBuildGraph(t, e)
	mustMigrate(t, e, []Target{key("migrations", "")}, true)

	// Table detection sees 0002 is applied.
	if !detect("0002_initial") {
		t.Error("detect_soft_applied(0002_initial) = false, want true")
	}

	// Leave the tables for 0001 except one. That missing table should make
	// detect_soft_applied() return false.
	db.dropTable("migrations_task")
	if detect("0001_initial") {
		t.Error("detect_soft_applied(0001_initial) = true after dropping a table, want false")
	}
}

// TestExecutor_DetectSoftAppliedOnlyForInitialMigrations pins the two bails at
// the top of detect_soft_applied: a migration explicitly marked non-initial is
// never soft-applied, and a migration with no `initial` flag is only
// considered when it is the first one in its app (no dependency on its own
// app).
//
// django: db/migrations/executor.py MigrationExecutor.detect_soft_applied
func TestExecutor_DetectSoftAppliedOnlyForInitialMigrations(t *testing.T) {
	conn, db := newFakeConn(t)
	create := func(app, name string, initial *bool, deps []m.Key) *m.Migration {
		return &m.Migration{App: app, Name: name, Initial: initial, Dependencies: deps,
			Operations: []m.Operation{&m.CreateModel{
				Name: "Thing", Table: app + "_thing",
				Fields: m.Fields{{Name: "id", Field: autoPK()}}}}}
	}
	all := map[string][]*m.Migration{
		"initial_true":  {create("initial_true", "0001_initial", m.Ptr(true), nil)},
		"initial_false": {create("initial_false", "0001_initial", m.Ptr(false), nil)},
		"initial_none":  {create("initial_none", "0001_initial", nil, nil)},
		"initial_dep": {
			create("initial_dep", "0001_initial", nil, nil),
			{App: "initial_dep", Name: "0002_later", Dependencies: []m.Key{
				{App: "initial_dep", Name: "0001_initial"}},
				Operations: []m.Operation{&m.CreateModel{
					Name: "Later", Table: "initial_dep_later",
					Fields: m.Fields{{Name: "id", Field: autoPK()}}}}},
		},
	}
	e := newExecutor(t, conn, all, nil)
	// Create every table, then forget that anything was applied.
	for _, target := range []Target{
		key("initial_true", "0001_initial"),
		key("initial_false", "0001_initial"),
		key("initial_none", "0001_initial"),
		key("initial_dep", "0002_later"),
	} {
		mustMigrate(t, e, []Target{target}, false)
		mustBuildGraph(t, e)
	}
	for _, table := range []string{
		"initial_true_thing", "initial_false_thing", "initial_none_thing",
		"initial_dep_thing", "initial_dep_later",
	} {
		assertTableExists(t, db, table)
	}

	detect := func(app, name string) bool {
		t.Helper()
		mig, ok := e.Loader.GetMigration(app, name)
		if !ok {
			t.Fatalf("%s.%s is not in the graph", app, name)
		}
		applied, _, err := e.DetectSoftApplied(nil, mig)
		if err != nil {
			t.Fatalf("DetectSoftApplied(%s.%s): %v", app, name, err)
		}
		return applied
	}

	if !detect("initial_true", "0001_initial") {
		t.Error("initial=true migration should be soft-applied")
	}
	if detect("initial_false", "0001_initial") {
		t.Error("initial=false migration must never be soft-applied")
	}
	if !detect("initial_none", "0001_initial") {
		t.Error("initial=nil first migration of its app should be soft-applied")
	}
	if detect("initial_dep", "0002_later") {
		t.Error("initial=nil migration depending on its own app must not be soft-applied")
	}
}

// django: tests/migrations/test_executor.py
// ExecutorTests.test_unrelated_model_lookups_forwards
func TestExecutor_UnrelatedModelLookupsForwards(t *testing.T) {
	conn, db := newFakeConn(t)
	e := newExecutor(t, conn, lookupErrorApps(), nil)

	assertTableNotExists(t, db, "lookuperror_a_a1")
	assertTableNotExists(t, db, "lookuperror_b_b1")
	assertTableNotExists(t, db, "lookuperror_c_c1")

	mustMigrate(t, e, []Target{key("lookuperror_b", "0003_b3")}, false)
	assertTableExists(t, db, "lookuperror_b_b3")
	mustBuildGraph(t, e)

	// Migrating forwards must not fail looking up lookuperror_b.B2, which is
	// already applied.
	mustMigrate(t, e, []Target{
		key("lookuperror_a", "0004_a4"),
		key("lookuperror_c", "0003_c3"),
	}, false)
	assertTableExists(t, db, "lookuperror_a_a4")
	assertTableExists(t, db, "lookuperror_c_c3")

	mustBuildGraph(t, e)
	mustMigrate(t, e, []Target{
		key("lookuperror_a", ""),
		key("lookuperror_b", ""),
		key("lookuperror_c", ""),
	}, false)
	assertTableNotExists(t, db, "lookuperror_a_a1")
	assertTableNotExists(t, db, "lookuperror_b_b1")
	assertTableNotExists(t, db, "lookuperror_c_c1")
}

// django: tests/migrations/test_executor.py
// ExecutorTests.test_unrelated_model_lookups_backwards
func TestExecutor_UnrelatedModelLookupsBackwards(t *testing.T) {
	conn, db := newFakeConn(t)
	e := newExecutor(t, conn, lookupErrorApps(), nil)

	assertTableNotExists(t, db, "lookuperror_a_a1")
	assertTableNotExists(t, db, "lookuperror_b_b1")
	assertTableNotExists(t, db, "lookuperror_c_c1")

	mustMigrate(t, e, []Target{
		key("lookuperror_a", "0004_a4"),
		key("lookuperror_b", "0003_b3"),
		key("lookuperror_c", "0003_c3"),
	}, false)
	assertTableExists(t, db, "lookuperror_b_b3")
	assertTableExists(t, db, "lookuperror_a_a4")
	assertTableExists(t, db, "lookuperror_c_c3")
	mustBuildGraph(t, e)

	// Migrating backwards must not fail because lookuperror_b.B2 is not in
	// the initial state (it is unrelated to app c).
	mustMigrate(t, e, []Target{key("lookuperror_a", "")}, false)
	mustBuildGraph(t, e)

	mustMigrate(t, e, []Target{key("lookuperror_b", ""), key("lookuperror_c", "")}, false)
	assertTableNotExists(t, db, "lookuperror_a_a1")
	assertTableNotExists(t, db, "lookuperror_b_b1")
	assertTableNotExists(t, db, "lookuperror_c_c1")
}

// django: tests/migrations/test_executor.py
// ExecutorTests.test_unrelated_applied_migrations_mutate_state
func TestExecutor_UnrelatedAppliedMigrationsMutateState(t *testing.T) {
	conn, _ := newFakeConn(t)
	e := newExecutor(t, conn, mutateStateApps(), nil)

	mustMigrate(t, e, []Target{key("mutate_state_b", "0002_add_field")}, false)

	// Migrate forward.
	mustBuildGraph(t, e)
	state := mustMigrate(t, e, []Target{key("mutate_state_a", "0001_initial")}, false)
	assertHasField(t, state, m.ModelKey{App: "mutate_state_b", Model: "b"}, "added")

	// Migrate backward.
	mustBuildGraph(t, e)
	state = mustMigrate(t, e, []Target{key("mutate_state_a", "")}, false)
	assertHasField(t, state, m.ModelKey{App: "mutate_state_b", Model: "b"}, "added")

	mustBuildGraph(t, e)
	mustMigrate(t, e, []Target{key("mutate_state_b", "")}, false)
}

func assertHasField(t *testing.T, state *m.ProjectState, k m.ModelKey, field string) {
	t.Helper()
	ms, ok := state.Models[k]
	if !ok {
		t.Fatalf("state has no model %v; models: %v", k, state.SortedKeys())
	}
	if _, ok := ms.Fields.Get(field); !ok {
		t.Errorf("model %v has no field %q; fields: %v", k, field, ms.Fields.Names())
	}
}

// TestExecutor_UnappliedUnrelatedMigrationsNotInState is the counterpart of
// test_unrelated_applied_migrations_mutate_state: _migrate_all_backwards only
// mutates the state with a migration of the full plan when that migration is
// actually applied, "to make sure the resulting state doesn't include changes
// from unrelated migrations".
//
// The unrelated app is named so that it sorts before the app being unapplied,
// otherwise the loop bails out before reaching it.
//
// django: db/migrations/executor.py MigrationExecutor._migrate_all_backwards
func TestExecutor_UnappliedUnrelatedMigrationsNotInState(t *testing.T) {
	conn, _ := newFakeConn(t)
	all := map[string][]*m.Migration{
		"aaa_other": {{
			App: "aaa_other", Name: "0001_initial",
			Operations: []m.Operation{&m.CreateModel{
				Name: "Other", Table: "aaa_other_other",
				Fields: m.Fields{{Name: "id", Field: autoPK()}}}},
		}},
		"zzz_target": {
			{
				App: "zzz_target", Name: "0001_initial",
				Operations: []m.Operation{&m.CreateModel{
					Name: "Target", Table: "zzz_target_target",
					Fields: m.Fields{{Name: "id", Field: autoPK()}}}},
			},
			{
				App: "zzz_target", Name: "0002_extra",
				Dependencies: []m.Key{{App: "zzz_target", Name: "0001_initial"}},
				Operations: []m.Operation{&m.AddField{
					ModelName: "Target", Name: "extra", Field: charField(10)}},
			},
		},
	}
	e := newExecutor(t, conn, all, nil)
	// Apply only zzz_target; aaa_other stays unapplied.
	mustMigrate(t, e, []Target{key("zzz_target", "0002_extra")}, false)
	mustBuildGraph(t, e)

	state := mustMigrate(t, e, []Target{key("zzz_target", "")}, false)
	if _, ok := state.Models[m.ModelKey{App: "aaa_other", Model: "other"}]; ok {
		t.Error("the resulting state includes a model from an unapplied, unrelated migration")
	}
	if len(state.Models) != 0 {
		t.Errorf("state should be empty, got %v", state.SortedKeys())
	}
}

// django: tests/migrations/test_executor.py ExecutorTests.test_process_callback
func TestExecutor_ProcessCallback(t *testing.T) {
	conn, db := newFakeConn(t)
	var calls []string
	e := newExecutor(t, conn, sets("migrations", testMigrations()), func(ev Event, mig *m.Migration, fake bool) {
		if mig == nil {
			calls = append(calls, ev.String())
			return
		}
		name := mig.App + "." + mig.Name
		suffix := " false"
		if fake {
			suffix = " true"
		}
		calls = append(calls, ev.String()+" "+name+suffix)
	})

	assertTableNotExists(t, db, "migrations_author")
	assertTableNotExists(t, db, "migrations_tribble")

	mustMigrate(t, e, []Target{
		key("migrations", "0001_initial"),
		key("migrations", "0002_second"),
	}, false)
	mustBuildGraph(t, e)
	mustMigrate(t, e, []Target{key("migrations", ""), key("migrations", "")}, false)

	assertTableNotExists(t, db, "migrations_author")
	assertTableNotExists(t, db, "migrations_tribble")

	want := []string{
		"render start",
		"render success",
		"apply start migrations.0001_initial false",
		"apply success migrations.0001_initial false",
		"apply start migrations.0002_second false",
		"apply success migrations.0002_second false",
		"render start",
		"render success",
		"unapply start migrations.0002_second false",
		"unapply success migrations.0002_second false",
		"unapply start migrations.0001_initial false",
		"unapply success migrations.0001_initial false",
	}
	if !equalStrings(calls, want) {
		t.Errorf("callbacks =\n%v\nwant\n%v", calls, want)
	}
}

// django: tests/migrations/test_executor.py
// ExecutorTests.test_apply_all_replaced_marks_replacement_as_applied
func TestExecutor_ApplyAllReplacedMarksReplacementAsApplied(t *testing.T) {
	conn, _ := newFakeConn(t)
	e := newExecutor(t, conn, sets("migrations", testMigrationsSquashed()), nil)

	// Place the database in a state where the replaced migrations are
	// partially applied: 0001 is applied, 0002 is not.
	if err := e.Recorder.RecordApplied("migrations", "0001_initial"); err != nil {
		t.Fatal(err)
	}
	e = newExecutor(t, conn, sets("migrations", testMigrationsSquashed()), nil)
	mustMigrate(t, e, []Target{key("migrations", "0002_second")}, true)

	if !appliedSet(t, e)[key("migrations", "0001_squashed_0002")] {
		t.Error("the squashed replacement was not marked applied")
	}
}

// django: tests/migrations/test_executor.py
// ExecutorTests.test_migrate_marks_replacement_applied_even_if_it_did_nothing
func TestExecutor_MigrateMarksReplacementAppliedEvenIfItDidNothing(t *testing.T) {
	conn, _ := newFakeConn(t)
	e := newExecutor(t, conn, sets("migrations", testMigrationsSquashed()), nil)
	// Record all replaced migrations as applied.
	for _, name := range []string{"0001_initial", "0002_second"} {
		if err := e.Recorder.RecordApplied("migrations", name); err != nil {
			t.Fatal(err)
		}
	}
	e = newExecutor(t, conn, sets("migrations", testMigrationsSquashed()), nil)
	mustMigrate(t, e, []Target{key("migrations", "0001_squashed_0002")}, false)

	if !appliedSet(t, e)[key("migrations", "0001_squashed_0002")] {
		t.Error("the squashed replacement was not marked applied")
	}
}

// django: tests/migrations/test_executor.py
// ExecutorTests.test_migrate_marks_replacement_unapplied
func TestExecutor_MigrateMarksReplacementUnapplied(t *testing.T) {
	conn, _ := newFakeConn(t)
	e := newExecutor(t, conn, sets("migrations", testMigrationsSquashed()), nil)
	mustMigrate(t, e, []Target{key("migrations", "0001_squashed_0002")}, false)
	if !appliedSet(t, e)[key("migrations", "0001_squashed_0002")] {
		t.Fatal("the squashed migration was not recorded as applied")
	}

	mustBuildGraph(t, e)
	mustMigrate(t, e, []Target{key("migrations", "")}, false)
	if appliedSet(t, e)[key("migrations", "0001_squashed_0002")] {
		t.Error("the squashed migration is still recorded as applied")
	}
}

// django: tests/migrations/test_executor.py
// ExecutorTests.test_migrations_applied_and_recorded_atomically
func TestExecutor_MigrationsAppliedAndRecordedAtomically(t *testing.T) {
	conn, db := newFakeConn(t)
	mig := &m.Migration{
		App: "record_migration", Name: "0001_initial", Initial: m.Ptr(true),
		Operations: []m.Operation{
			&m.CreateModel{Name: "Model", Table: "record_migration_model",
				Fields: m.Fields{{Name: "id", Field: autoPK()}}},
		},
	}
	e := newExecutor(t, conn, sets("record_migration", []*m.Migration{mig}), nil)
	// Make sure the migrations table exists before we poison the insert, so
	// the failure is the recording and not a missing table.
	if err := e.Recorder.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	db.failInsert = true

	_, err := e.ApplyMigration(m.NewProjectState(), mig, false, false)
	if err == nil || !strings.Contains(err.Error(), "Recording migration failed.") {
		t.Fatalf("ApplyMigration error = %v, want the recording failure", err)
	}
	db.failInsert = false

	// The migration isn't recorded as applied since it failed...
	if appliedSet(t, e)[key("record_migration", "0001_initial")] {
		t.Error("the migration was recorded although recording failed")
	}
	// ...and, because the backend can roll back DDL, its table is gone too.
	assertTableNotExists(t, db, "record_migration_model")
}

// django: tests/migrations/test_executor.py
// ExecutorTests.test_migrations_not_applied_on_deferred_sql_failure
func TestExecutor_MigrationsNotAppliedOnDeferredSQLFailure(t *testing.T) {
	conn, db := newFakeConn(t)
	mig := &m.Migration{
		App: "deferred_sql", Name: "0001_initial", Initial: m.Ptr(true),
		Atomic: m.Ptr(false),
	}
	e := newExecutor(t, conn, sets("deferred_sql", []*m.Migration{mig}), nil)
	if err := e.Recorder.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	db.deferredFails = true

	_, err := e.ApplyMigration(m.NewProjectState(), mig, false, false)
	if err == nil || !strings.Contains(err.Error(), "Failed to apply deferred SQL") {
		t.Fatalf("ApplyMigration error = %v, want the deferred SQL failure", err)
	}
	db.deferredFails = false
	if appliedSet(t, e)[key("deferred_sql", "0001_initial")] {
		t.Error("the migration was recorded although the deferred SQL failed")
	}
}

// django: tests/migrations/test_executor.py
// ExecutorTests.test_migrate_skips_schema_creation
func TestExecutor_MigrateSkipsSchemaCreation(t *testing.T) {
	conn, db := newFakeConn(t)
	e := newExecutor(t, conn, sets("migrations", testMigrations()), nil)

	state, err := e.MigrateTo(MigrateOptions{Plan: []m.PlanStep{}})
	if err != nil {
		t.Fatal(err)
	}
	if state == nil {
		t.Fatal("Migrate returned a nil state")
	}
	// The migrations table is not created when there is nothing to record.
	assertTableNotExists(t, db, "gormgate_migrations")
	if ops := db.operations(); len(ops) != 0 {
		t.Errorf("schema editor was used: %v", ops)
	}
}

// TestExecutor_CollectSQL checks that a plan can be rendered as SQL without
// touching the database, the code path behind sqlmigrate.
//
// django: db/migrations/loader.py MigrationLoader.collect_sql
func TestExecutor_CollectSQL(t *testing.T) {
	conn, db := newFakeConn(t)
	e := newExecutor(t, conn, sets("migrations", testMigrations()), nil)
	plan, err := e.MigrationPlan([]Target{key("migrations", "0001_initial")}, false)
	if err != nil {
		t.Fatal(err)
	}
	stmts, err := e.CollectSQL(plan)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(stmts, "\n")
	for _, want := range []string{
		"-- create model Author",
		"CreateModel migrations_author;",
		"-- create model Tribble",
		"CreateModel migrations_tribble;",
		"-- add field bool to tribble",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("collected SQL is missing %q:\n%s", want, joined)
		}
	}
	// Collecting must not have changed the database.
	if len(db.tableNames()) != 0 {
		t.Errorf("collecting SQL created tables: %v", db.tableNames())
	}
}

// ---------------------------------------------------------------------------
// ExecutorUnitTests: isolated migration_plan tests on a hand-built graph.
// ---------------------------------------------------------------------------

// unitExecutor is Django's MigrationExecutor(None) with a FakeLoader: only
// the graph and the applied set matter for migration_plan.
func unitExecutor(g *graph.Graph, applied ...m.Key) *Executor {
	set := map[m.Key]bool{}
	for _, k := range applied {
		set[k] = true
	}
	return &Executor{
		Loader: &loader.Loader{Graph: g, Applied: set},
		cfg:    loader.Config{ReplaceMigrations: true},
	}
}

func unitGraph(t *testing.T, nodes []m.Key, deps [][2]m.Key) *graph.Graph {
	t.Helper()
	g := graph.New()
	for _, k := range nodes {
		g.AddNode(k, &m.Migration{App: k.App, Name: k.Name})
	}
	for _, d := range deps {
		if err := g.AddDependency("", d[0], d[1], false); err != nil {
			t.Fatal(err)
		}
	}
	return g
}

// django: tests/migrations/test_executor.py ExecutorUnitTests.test_minimize_rollbacks
//
// When you say "migrate appA 0001", rather than migrating to just after
// appA-0001 in the linearized plan (which could roll back migrations in other
// apps that depend on appA 0001 but don't need rolling back), migrate to just
// before appA-0002.
func TestExecutorUnit_MinimizeRollbacks(t *testing.T) {
	a1, a2, b1 := key("a", "1"), key("a", "2"), key("b", "1")
	g := unitGraph(t, []m.Key{a1, a2, b1}, [][2]m.Key{{b1, a1}, {a2, a1}})
	e := unitExecutor(g, a1, b1, a2)

	plan, err := e.MigrationPlan([]Target{a1}, false)
	if err != nil {
		t.Fatal(err)
	}
	assertPlan(t, plan, "a.2 backwards")
}

// django: tests/migrations/test_executor.py
// ExecutorUnitTests.test_minimize_rollbacks_branchy
//
//	a: 1 <---- 3 <--\
//	      \ \- 2 <--- 4
//	       \       \
//	b:      \- 1 <--- 2
func TestExecutorUnit_MinimizeRollbacksBranchy(t *testing.T) {
	a1, a2, a3, a4 := key("a", "1"), key("a", "2"), key("a", "3"), key("a", "4")
	b1, b2 := key("b", "1"), key("b", "2")
	g := unitGraph(t,
		[]m.Key{a1, a2, a3, a4, b1, b2},
		[][2]m.Key{{a2, a1}, {a3, a1}, {a4, a2}, {a4, a3}, {b2, b1}, {b1, a1}, {b2, a2}},
	)
	e := unitExecutor(g, a1, b1, a2, b2, a3, a4)

	plan, err := e.MigrationPlan([]Target{a1}, false)
	if err != nil {
		t.Fatal(err)
	}
	assertPlan(t, plan, "b.2 backwards", "a.4 backwards", "a.2 backwards", "a.3 backwards")
}

// django: tests/migrations/test_executor.py
// ExecutorUnitTests.test_backwards_nothing_to_do
//
//	a: 1 <--- 2
//	b:    \- 1
//	c:     \- 1
//
// If a1 is applied and a2 is not, and we're asked to migrate to a1, don't
// apply or unapply b1 or c1, regardless of their current state.
func TestExecutorUnit_BackwardsNothingToDo(t *testing.T) {
	a1, a2 := key("a", "1"), key("a", "2")
	b1, c1 := key("b", "1"), key("c", "1")
	g := unitGraph(t, []m.Key{a1, a2, b1, c1}, [][2]m.Key{{a2, a1}, {b1, a1}, {c1, a1}})
	e := unitExecutor(g, a1, b1)

	plan, err := e.MigrationPlan([]Target{a1}, false)
	if err != nil {
		t.Fatal(err)
	}
	assertPlan(t, plan)
}

// TestExecutor_CreateProjectState covers _create_project_state, which Django
// exercises indirectly through test_empty_plan: with_applied_migrations=false
// yields an empty state, true replays the applied migrations.
//
// django: db/migrations/executor.py MigrationExecutor._create_project_state
func TestExecutor_CreateProjectState(t *testing.T) {
	conn, _ := newFakeConn(t)
	e := newExecutor(t, conn, sets("migrations", testMigrations()), nil)
	mustMigrate(t, e, []Target{key("migrations", "0001_initial")}, true)
	mustBuildGraph(t, e)

	empty, err := e.CreateProjectState(false)
	if err != nil {
		t.Fatal(err)
	}
	if len(empty.Models) != 0 {
		t.Errorf("state without applied migrations has models: %v", empty.SortedKeys())
	}

	withApplied, err := e.CreateProjectState(true)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []m.ModelKey{
		{App: "migrations", Model: "author"},
		{App: "migrations", Model: "tribble"},
	} {
		if _, ok := withApplied.Models[k]; !ok {
			t.Errorf("state is missing %v; models: %v", k, withApplied.SortedKeys())
		}
	}
	// 0002 is not applied, so Book must not be there.
	if _, ok := withApplied.Models[m.ModelKey{App: "migrations", Model: "book"}]; ok {
		t.Error("state includes a model from an unapplied migration")
	}
}

// TestExecutor_RecordMigrationRecordsReplaced checks that recording a
// squashed migration also records each migration it replaces.
//
// django: db/migrations/executor.py MigrationExecutor.record_migration
func TestExecutor_RecordMigrationRecordsReplaced(t *testing.T) {
	conn, _ := newFakeConn(t)
	e := newExecutor(t, conn, sets("migrations", testMigrationsSquashed()), nil)
	if err := e.RecordMigration("migrations", "0001_squashed_0002", true); err != nil {
		t.Fatal(err)
	}
	applied := appliedSet(t, e)
	for _, k := range []m.Key{
		key("migrations", "0001_initial"),
		key("migrations", "0002_second"),
		key("migrations", "0001_squashed_0002"),
	} {
		if !applied[k] {
			t.Errorf("%v was not recorded", k)
		}
	}

	if err := e.RecordMigration("migrations", "0001_squashed_0002", false); err != nil {
		t.Fatal(err)
	}
	if applied := appliedSet(t, e); len(applied) != 0 {
		t.Errorf("records left after unrecording: %v", applied)
	}
}
