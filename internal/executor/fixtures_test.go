package executor

import (
	"sort"
	"testing"

	"github.com/ctolon/gormgate/backends/base"
	"github.com/ctolon/gormgate/internal/loader"
	m "github.com/ctolon/gormgate/migrations"
)

// The migration sets below mirror the fixtures Django's
// tests/migrations/test_executor.py points MIGRATION_MODULES at. They are
// built in memory and handed to the loader through loader.Config.Registered
// instead of living on disk.
//
// gormgate difference: CreateModel always carries an explicit table name, so
// every model below spells out the table Django would have derived from
// "<app>_<model>".

func autoPK() m.Field {
	return m.Field{Type: m.Int, Size: 32, PrimaryKey: true, AutoIncrement: true}
}

func charField(size int) m.Field { return m.Field{Type: m.String, Size: size} }

func slugField() m.Field { return m.Field{Type: m.String, Size: 50, Null: true} }

func intField(def int64) m.Field {
	return m.Field{Type: m.Int, Size: 32, Default: def}
}

func boolField(def bool) m.Field { return m.Field{Type: m.Bool, Default: def} }

// fkField is Django's ForeignKey(to, SET_NULL, null=True).
func fkField(to string) m.Field {
	return m.Field{
		Type: m.Int, Size: 32, Null: true,
		ForeignKey: &m.ForeignKey{To: to, OnDelete: m.SetNull},
	}
}

// fkCascade is Django's ForeignKey(to, CASCADE).
func fkCascade(to string) m.Field {
	return m.Field{Type: m.Int, Size: 32, ForeignKey: &m.ForeignKey{To: to, OnDelete: m.Cascade}}
}

func authorFields(withSillyField, withRating bool) m.Fields {
	fs := m.Fields{
		{Name: "id", Field: autoPK()},
		{Name: "name", Field: charField(255)},
		{Name: "slug", Field: slugField()},
		{Name: "age", Field: intField(0)},
	}
	if withSillyField {
		fs = append(fs, m.NamedField{Name: "silly_field", Field: boolField(false)})
	}
	if withRating {
		fs = append(fs, m.NamedField{Name: "rating", Field: intField(0)})
	}
	return fs
}

// testMigrations is migrations/test_migrations.
func testMigrations() []*m.Migration {
	return []*m.Migration{
		{
			App: "migrations", Name: "0001_initial", Initial: m.Ptr(true),
			Operations: []m.Operation{
				&m.CreateModel{Name: "Author", Table: "migrations_author", Fields: authorFields(true, false)},
				&m.CreateModel{Name: "Tribble", Table: "migrations_tribble", Fields: m.Fields{
					{Name: "id", Field: autoPK()},
					{Name: "fluffy", Field: boolField(true)},
				}},
				&m.AddField{ModelName: "tribble", Name: "bool", Field: boolField(false)},
				&m.AlterUniqueTogether{Name: "author", UniqueTogether: [][]string{{"name", "slug"}}},
			},
		},
		{
			App: "migrations", Name: "0002_second",
			Dependencies: []m.Key{{App: "migrations", Name: "0001_initial"}},
			Operations: []m.Operation{
				&m.DeleteModel{Name: "Tribble"},
				&m.RemoveField{ModelName: "Author", Name: "silly_field"},
				&m.AddField{ModelName: "Author", Name: "rating", Field: intField(0)},
				&m.CreateModel{Name: "Book", Table: "migrations_book", Fields: m.Fields{
					{Name: "id", Field: autoPK()},
					{Name: "author", Field: fkField("migrations.Author")},
				}},
			},
		},
	}
}

// testMigrationsSquashed is migrations/test_migrations_squashed.
func testMigrationsSquashed() []*m.Migration {
	return []*m.Migration{
		{
			App: "migrations", Name: "0001_initial",
			Operations: []m.Operation{
				&m.CreateModel{Name: "Author", Table: "migrations_author", Fields: authorFields(true, false)},
				&m.CreateModel{Name: "Tribble", Table: "migrations_tribble", Fields: m.Fields{
					{Name: "id", Field: autoPK()},
					{Name: "fluffy", Field: boolField(true)},
				}},
			},
		},
		{
			App: "migrations", Name: "0002_second",
			Dependencies: []m.Key{{App: "migrations", Name: "0001_initial"}},
			Operations: []m.Operation{
				&m.DeleteModel{Name: "Tribble"},
				&m.RemoveField{ModelName: "Author", Name: "silly_field"},
				&m.AddField{ModelName: "Author", Name: "rating", Field: intField(0)},
				&m.CreateModel{Name: "Book", Table: "migrations_book", Fields: m.Fields{
					{Name: "id", Field: autoPK()},
					{Name: "author", Field: fkField("migrations.Author")},
				}},
			},
		},
		{
			App: "migrations", Name: "0001_squashed_0002",
			Replaces: []m.Key{
				{App: "migrations", Name: "0001_initial"},
				{App: "migrations", Name: "0002_second"},
			},
			Operations: []m.Operation{
				&m.CreateModel{Name: "Author", Table: "migrations_author", Fields: authorFields(false, true)},
				&m.CreateModel{Name: "Book", Table: "migrations_book", Fields: m.Fields{
					{Name: "id", Field: autoPK()},
					{Name: "author", Field: fkField("migrations.Author")},
				}},
			},
		},
	}
}

// testMigrations2 is migrations2/test_migrations_2 (depends on migrations).
func testMigrations2() []*m.Migration {
	return []*m.Migration{{
		App: "migrations2", Name: "0001_initial",
		Dependencies: []m.Key{{App: "migrations", Name: "0002_second"}},
		Operations: []m.Operation{
			&m.CreateModel{Name: "OtherAuthor", Table: "migrations2_otherauthor",
				Fields: authorFields(true, false)},
		},
	}}
}

// testMigrations2NoDeps is migrations2/test_migrations_2_no_deps.
func testMigrations2NoDeps() []*m.Migration {
	return []*m.Migration{{
		App: "migrations2", Name: "0001_initial",
		Operations: []m.Operation{
			&m.CreateModel{Name: "OtherAuthor", Table: "migrations2_otherauthor",
				Fields: authorFields(true, false)},
		},
	}}
}

// lookupErrorApps is migrations_test_apps.lookuperror_{a,b,c}.
func lookupErrorApps() map[string][]*m.Migration {
	simple := func(app, name, model, table string) *m.Migration {
		return &m.Migration{App: app, Name: name, Operations: []m.Operation{
			&m.CreateModel{Name: model, Table: table, Fields: m.Fields{{Name: "id", Field: autoPK()}}},
		}}
	}
	a1 := simple("lookuperror_a", "0001_initial", "A1", "lookuperror_a_a1")
	a2 := simple("lookuperror_a", "0002_a2", "A2", "lookuperror_a_a2")
	a2.Dependencies = []m.Key{{App: "lookuperror_a", Name: "0001_initial"}}
	a3 := &m.Migration{
		App: "lookuperror_a", Name: "0003_a3",
		Dependencies: []m.Key{
			{App: "lookuperror_c", Name: "0002_c2"},
			{App: "lookuperror_b", Name: "0002_b2"},
			{App: "lookuperror_a", Name: "0002_a2"},
		},
		Operations: []m.Operation{&m.CreateModel{
			Name: "A3", Table: "lookuperror_a_a3", Fields: m.Fields{
				{Name: "id", Field: autoPK()},
				{Name: "b2", Field: fkCascade("lookuperror_b.B2")},
				{Name: "c2", Field: fkCascade("lookuperror_c.C2")},
			}}},
	}
	a4 := simple("lookuperror_a", "0004_a4", "A4", "lookuperror_a_a4")
	a4.Dependencies = []m.Key{{App: "lookuperror_a", Name: "0003_a3"}}

	b1 := simple("lookuperror_b", "0001_initial", "B1", "lookuperror_b_b1")
	b2 := &m.Migration{
		App: "lookuperror_b", Name: "0002_b2",
		Dependencies: []m.Key{
			{App: "lookuperror_a", Name: "0002_a2"},
			{App: "lookuperror_b", Name: "0001_initial"},
		},
		Operations: []m.Operation{&m.CreateModel{
			Name: "B2", Table: "lookuperror_b_b2", Fields: m.Fields{
				{Name: "id", Field: autoPK()},
				{Name: "a1", Field: fkCascade("lookuperror_a.A1")},
			}}},
	}
	b3 := simple("lookuperror_b", "0003_b3", "B3", "lookuperror_b_b3")
	b3.Dependencies = []m.Key{{App: "lookuperror_b", Name: "0002_b2"}}

	c1 := simple("lookuperror_c", "0001_initial", "C1", "lookuperror_c_c1")
	c2 := &m.Migration{
		App: "lookuperror_c", Name: "0002_c2",
		Dependencies: []m.Key{
			{App: "lookuperror_a", Name: "0002_a2"},
			{App: "lookuperror_c", Name: "0001_initial"},
		},
		Operations: []m.Operation{&m.CreateModel{
			Name: "C2", Table: "lookuperror_c_c2", Fields: m.Fields{
				{Name: "id", Field: autoPK()},
				{Name: "a1", Field: fkCascade("lookuperror_a.A1")},
			}}},
	}
	c3 := simple("lookuperror_c", "0003_c3", "C3", "lookuperror_c_c3")
	c3.Dependencies = []m.Key{{App: "lookuperror_c", Name: "0002_c2"}}

	return map[string][]*m.Migration{
		"lookuperror_a": {a1, a2, a3, a4},
		"lookuperror_b": {b1, b2, b3},
		"lookuperror_c": {c1, c2, c3},
	}
}

// mutateStateApps is migrations_test_apps.mutate_state_{a,b}: state-only
// migrations wrapped in SeparateDatabaseAndState.
func mutateStateApps() map[string][]*m.Migration {
	return map[string][]*m.Migration{
		"mutate_state_a": {{
			App: "mutate_state_a", Name: "0001_initial",
			Dependencies: []m.Key{{App: "mutate_state_b", Name: "0001_initial"}},
			Operations: []m.Operation{&m.SeparateDatabaseAndState{
				StateOperations: []m.Operation{&m.CreateModel{
					Name: "A", Table: "mutate_state_a_a",
					Fields: m.Fields{{Name: "id", Field: autoPK()}}}},
			}},
		}},
		"mutate_state_b": {
			{
				App: "mutate_state_b", Name: "0001_initial",
				Operations: []m.Operation{&m.SeparateDatabaseAndState{
					StateOperations: []m.Operation{&m.CreateModel{
						Name: "B", Table: "mutate_state_b_b",
						Fields: m.Fields{{Name: "id", Field: autoPK()}}}},
				}},
			},
			{
				App: "mutate_state_b", Name: "0002_add_field",
				Dependencies: []m.Key{{App: "mutate_state_b", Name: "0001_initial"}},
				Operations: []m.Operation{&m.SeparateDatabaseAndState{
					StateOperations: []m.Operation{&m.AddField{
						ModelName: "B", Name: "added", Field: m.Field{Type: m.String}}},
				}},
			},
		},
	}
}

// ---------------------------------------------------------------------------
// executor construction helpers
// ---------------------------------------------------------------------------

// newExecutor builds an executor over the given in-memory migration sets.
func newExecutor(t *testing.T, conn *base.Conn, sets map[string][]*m.Migration, progress Progress) *Executor {
	t.Helper()
	e, err := New(conn, configFor(sets), nil, progress)
	if err != nil {
		t.Fatalf("executor.New: %v", err)
	}
	return e
}

func configFor(sets map[string][]*m.Migration) loader.Config {
	apps := make([]string, 0, len(sets))
	for app := range sets {
		apps = append(apps, app)
	}
	sort.Strings(apps)
	specs := make([]loader.AppSpec, len(apps))
	for i, app := range apps {
		specs[i] = loader.AppSpec{Label: app}
	}
	return loader.Config{
		Apps: specs,
		Registered: func(app string) []*m.Migration {
			out := append([]*m.Migration(nil), sets[app]...)
			sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
			return out
		},
		Compiled: func(app string) bool {
			_, ok := sets[app]
			return ok
		},
		SkipDiskCheck: true,
		// Django's MigrationLoader.replace_migrations defaults to True; in Go
		// the zero value is false, so every caller sets it explicitly.
		ReplaceMigrations: true,
	}
}

func sets(pairs ...any) map[string][]*m.Migration {
	out := map[string][]*m.Migration{}
	for i := 0; i < len(pairs); i += 2 {
		out[pairs[i].(string)] = pairs[i+1].([]*m.Migration)
	}
	return out
}

// ---------------------------------------------------------------------------
// assertions
// ---------------------------------------------------------------------------

func key(app, name string) m.Key { return m.Key{App: app, Name: name} }

// planNames renders a plan the way Django's assertEqual on
// [(migration, backwards)] reads.
func planNames(plan []m.PlanStep) []string {
	out := make([]string, len(plan))
	for i, s := range plan {
		dir := "forwards"
		if s.Backwards {
			dir = "backwards"
		}
		out[i] = s.Migration.App + "." + s.Migration.Name + " " + dir
	}
	return out
}

func assertPlan(t *testing.T, plan []m.PlanStep, want ...string) {
	t.Helper()
	got := planNames(plan)
	if len(want) == 0 && len(got) == 0 {
		return
	}
	if !equalStrings(got, want) {
		t.Errorf("plan = %v, want %v", got, want)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func assertTableExists(t *testing.T, db *fakeDB, table string) {
	t.Helper()
	if !db.hasTable(table) {
		t.Errorf("table %q does not exist; tables: %v", table, db.tableNames())
	}
}

func assertTableNotExists(t *testing.T, db *fakeDB, table string) {
	t.Helper()
	if db.hasTable(table) {
		t.Errorf("table %q exists but should not; tables: %v", table, db.tableNames())
	}
}

func appliedSet(t *testing.T, e *Executor) map[m.Key]bool {
	t.Helper()
	keys, err := e.Recorder.AppliedMigrations()
	if err != nil {
		t.Fatalf("AppliedMigrations: %v", err)
	}
	out := map[m.Key]bool{}
	for _, k := range keys {
		out[k] = true
	}
	return out
}

func mustMigrate(t *testing.T, e *Executor, targets []Target, fake bool) *m.ProjectState {
	t.Helper()
	st, err := e.MigrateTo(MigrateOptions{Targets: targets, Fake: fake})
	if err != nil {
		t.Fatalf("Migrate(%v, fake=%v): %v", targets, fake, err)
	}
	return st
}

func mustBuildGraph(t *testing.T, e *Executor) {
	t.Helper()
	if err := e.Loader.BuildGraph(); err != nil {
		t.Fatalf("BuildGraph: %v", err)
	}
}
