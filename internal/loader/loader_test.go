package loader

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/ctolon/gormgate/internal/graph"
	m "github.com/ctolon/gormgate/migrations"
)

// django: tests/migrations/test_loader.py
//
// Django's loader imports migration modules from disk; gormgate loads them
// from a registry the generated files compile into the binary. The fixtures
// Django keeps under tests/migrations/test_migrations*/ are therefore built
// here in memory and handed to the loader through Config.Registered, and the
// MIGRATION_MODULES settings the Django tests override become AppSpec entries.
// The separate TestLoader_Disk* tests below cover the on-disk consistency
// check that has no Django counterpart.

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func k(app, name string) m.Key { return m.Key{App: app, Name: name} }

// noop is the operation Django's fixtures use as filler
// (migrations.RunPython(migrations.RunPython.noop)).
func noop() m.Operation { return &m.RunGo{Code: m.RunGoNoop, ReverseCode: m.RunGoNoop} }

// traceOp records the order in which the graph applies migrations, so that a
// test can observe the plan MakeState generated.
type traceOp struct {
	*m.RunGo
	log *[]m.Key
	key m.Key
}

func (o *traceOp) StateForwards(app string, s *m.ProjectState) error {
	*o.log = append(*o.log, o.key)
	return nil
}

// appliedSet is the in-memory stand-in for MigrationRecorder.
type appliedSet struct{ keys []m.Key }

func (a *appliedSet) AppliedMigrations() ([]m.Key, error) {
	return append([]m.Key(nil), a.keys...), nil
}

// recordApplied is the test helper of the same name in Django's LoaderTests.
func (a *appliedSet) recordApplied(app, name string) { a.keys = append(a.keys, k(app, name)) }

// migset maps an app label to its migrations, standing in for a set of
// migration directories.
type migset map[string][]*m.Migration

func (s migset) config(applied Applied, extra ...AppSpec) Config {
	labels := make([]string, 0, len(s))
	for app := range s {
		labels = append(labels, app)
	}
	sort.Strings(labels)
	apps := make([]AppSpec, 0, len(labels)+len(extra))
	for _, app := range labels {
		apps = append(apps, AppSpec{Label: app})
	}
	apps = append(apps, extra...)
	return Config{
		Apps:              apps,
		Registered:        func(app string) []*m.Migration { return s[app] },
		Compiled:          func(app string) bool { _, ok := s[app]; return ok },
		Recorder:          applied,
		ReplaceMigrations: true,
		SkipDiskCheck:     true,
	}
}

func newLoader(t *testing.T, cfg Config) *Loader {
	t.Helper()
	l, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return l
}

func mustForwardsPlan(t *testing.T, l *Loader, target m.Key) []m.Key {
	t.Helper()
	plan, err := l.Graph.ForwardsPlan(target)
	if err != nil {
		t.Fatalf("ForwardsPlan(%v): %v", target, err)
	}
	return plan
}

func assertPlan(t *testing.T, got []m.Key, want ...m.Key) {
	t.Helper()
	if want == nil {
		want = []m.Key{}
	}
	if got == nil {
		got = []m.Key{}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("plan mismatch\n got: %s\nwant: %s", fmtKeys(got), fmtKeys(want))
	}
}

// assertPlanSet compares plans that Django compares as sets.
func assertPlanSet(t *testing.T, got []m.Key, want ...m.Key) {
	t.Helper()
	g := append([]m.Key(nil), got...)
	w := append([]m.Key(nil), want...)
	graph.SortKeys(g)
	graph.SortKeys(w)
	if len(g) == 0 {
		g = []m.Key{}
	}
	if len(w) == 0 {
		w = []m.Key{}
	}
	if !reflect.DeepEqual(g, w) {
		t.Fatalf("plan mismatch\n got: %s\nwant: %s", fmtKeys(g), fmtKeys(w))
	}
}

func fmtKeys(keys []m.Key) string {
	parts := make([]string, len(keys))
	for i, key := range keys {
		parts[i] = graph.FormatKey(key)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// unapplied is Django's `plan -= loader.applied_migrations.keys()`.
func unapplied(l *Loader, plan []m.Key) []m.Key {
	var out []m.Key
	for _, key := range plan {
		if !l.Applied[key] {
			out = append(out, key)
		}
	}
	return out
}

func appNodes(l *Loader, app string) int {
	n := 0
	for key := range l.Graph.Nodes {
		if key.App == app {
			n++
		}
	}
	return n
}

// ---------------------------------------------------------------------------
// fixtures (ports of tests/migrations/test_migrations*/)
// ---------------------------------------------------------------------------

func autoPK() m.Field {
	return m.Field{Type: m.Int, Size: 32, PrimaryKey: true, AutoIncrement: true}
}

func charField(size int) m.Field { return m.Field{Type: m.String, Size: size} }

func fkField(to string) m.Field {
	return m.Field{Type: m.Int, Size: 32, Null: true,
		ForeignKey: &m.ForeignKey{To: to, ToField: "id", OnDelete: m.SetNull}}
}

// django: tests/migrations/test_migrations/
func testMigrations() migset {
	return migset{"migrations": {
		{App: "migrations", Name: "0001_initial", Initial: m.Ptr(true), Operations: []m.Operation{
			&m.CreateModel{Name: "Author", Table: "migrations_author", Fields: m.Fields{
				{Name: "id", Field: autoPK()},
				{Name: "name", Field: charField(255)},
				{Name: "slug", Field: m.Field{Type: m.String, Size: 50, Null: true}},
				{Name: "age", Field: m.Field{Type: m.Int, Size: 32, Default: 0}},
				{Name: "silly_field", Field: m.Field{Type: m.Bool, Default: false}},
			}},
			&m.CreateModel{Name: "Tribble", Table: "migrations_tribble", Fields: m.Fields{
				{Name: "id", Field: autoPK()},
				{Name: "fluffy", Field: m.Field{Type: m.Bool, Default: true}},
			}},
			&m.AddField{ModelName: "tribble", Name: "bool", Field: m.Field{Type: m.Bool, Default: false}},
			&m.AlterUniqueTogether{Name: "author", UniqueTogether: [][]string{{"name", "slug"}}},
		}},
		{App: "migrations", Name: "0002_second", Dependencies: []m.Key{k("migrations", "0001_initial")},
			Operations: []m.Operation{
				&m.DeleteModel{Name: "Tribble"},
				&m.RemoveField{ModelName: "Author", Name: "silly_field"},
				&m.AddField{ModelName: "Author", Name: "rating", Field: m.Field{Type: m.Int, Size: 32, Default: 0}},
				&m.CreateModel{Name: "Book", Table: "migrations_book", Fields: m.Fields{
					{Name: "id", Field: autoPK()},
					{Name: "author", Field: fkField("migrations.Author")},
				}},
			}},
	}}
}

// django: tests/migrations/test_migrations_run_before/
func runBeforeMigrations() migset {
	return migset{"migrations": {
		{App: "migrations", Name: "0001_initial", Operations: []m.Operation{noop()}},
		{App: "migrations", Name: "0002_second",
			Dependencies: []m.Key{k("migrations", "0001_initial")},
			Operations:   []m.Operation{noop()}},
		{App: "migrations", Name: "0003_third",
			Dependencies: []m.Key{k("migrations", "0001_initial")},
			RunBefore:    []m.Key{k("migrations", "0002_second")},
			Operations:   []m.Operation{noop()}},
	}}
}

// django: tests/migrations/test_migrations_first/ and
// tests/migrations2/test_migrations_2_first/
func firstMigrations() migset {
	return migset{
		"migrations": {
			{App: "migrations", Name: "thefirst", Operations: []m.Operation{noop()}},
			{App: "migrations", Name: "second", Dependencies: []m.Key{
				k("migrations", "thefirst"), k("migrations2", "0002_second"),
			}, Operations: []m.Operation{noop()}},
		},
		"migrations2": {
			{App: "migrations2", Name: "0001_initial",
				Dependencies: []m.Key{m.First("migrations")},
				Operations:   []m.Operation{noop()}},
			{App: "migrations2", Name: "0002_second",
				Dependencies: []m.Key{k("migrations2", "0001_initial")},
				Operations:   []m.Operation{noop()}},
		},
	}
}

// django: tests/migrations/test_migrations_squashed/
func squashedMigrations() migset {
	s := testMigrations()
	s["migrations"] = append(s["migrations"], &m.Migration{
		App: "migrations", Name: "0001_squashed_0002",
		Replaces:   []m.Key{k("migrations", "0001_initial"), k("migrations", "0002_second")},
		Operations: []m.Operation{noop()},
	})
	return s
}

// django: tests/migrations/test_migrations_squashed_extra/
func squashedExtraMigrations() migset {
	s := squashedMigrations()
	s["migrations"] = append(s["migrations"], &m.Migration{
		App: "migrations", Name: "0003_third",
		Dependencies: []m.Key{k("migrations", "0002_second")},
		Operations:   []m.Operation{noop()},
	})
	return s
}

// django: tests/migrations/test_migrations_squashed_complex/
func squashedComplexMigrations() migset {
	migs := []*m.Migration{
		{App: "migrations", Name: "1_auto", Operations: []m.Operation{noop()}},
		{App: "migrations", Name: "3_squashed_5",
			Replaces:     []m.Key{k("migrations", "3_auto"), k("migrations", "4_auto"), k("migrations", "5_auto")},
			Dependencies: []m.Key{k("migrations", "2_auto")},
			Operations:   []m.Operation{noop()}},
	}
	for _, n := range []string{"2_auto", "3_auto", "4_auto", "5_auto", "6_auto", "7_auto"} {
		prev := string(n[0]-1) + "_auto"
		migs = append(migs, &m.Migration{App: "migrations", Name: n,
			Dependencies: []m.Key{k("migrations", prev)}, Operations: []m.Operation{noop()}})
	}
	return migset{"migrations": migs}
}

// django: tests/migrations/test_migrations_squashed_erroneous/
//
// 3_auto, 4_auto and 5_auto are missing from disk, so the squash can only be
// used while none of them is applied.
func squashedErroneousMigrations() migset {
	return migset{"migrations": {
		{App: "migrations", Name: "1_auto", Operations: []m.Operation{noop()}},
		{App: "migrations", Name: "2_auto",
			Dependencies: []m.Key{k("migrations", "1_auto")}, Operations: []m.Operation{noop()}},
		{App: "migrations", Name: "3_squashed_5",
			Replaces:     []m.Key{k("migrations", "3_auto"), k("migrations", "4_auto"), k("migrations", "5_auto")},
			Dependencies: []m.Key{k("migrations", "2_auto")},
			Operations:   []m.Operation{noop()}},
		{App: "migrations", Name: "6_auto",
			Dependencies: []m.Key{k("migrations", "5_auto")}, Operations: []m.Operation{noop()}},
		{App: "migrations", Name: "7_auto",
			Dependencies: []m.Key{k("migrations", "6_auto")}, Operations: []m.Operation{noop()}},
	}}
}

// django: tests/migrations/test_migrations_squashed_complex_multi_apps/
func squashedComplexMultiApps() migset {
	return migset{
		"app1": {
			{App: "app1", Name: "1_auto", Operations: []m.Operation{noop()}},
			{App: "app1", Name: "2_auto",
				Dependencies: []m.Key{k("app1", "1_auto")}, Operations: []m.Operation{noop()}},
			{App: "app1", Name: "2_squashed_3",
				Replaces:     []m.Key{k("app1", "2_auto"), k("app1", "3_auto")},
				Dependencies: []m.Key{k("app1", "1_auto"), k("app2", "2_auto")},
				Operations:   []m.Operation{noop()}},
			{App: "app1", Name: "3_auto",
				Dependencies: []m.Key{k("app1", "2_auto"), k("app2", "2_auto")},
				Operations:   []m.Operation{noop()}},
			{App: "app1", Name: "4_auto",
				Dependencies: []m.Key{k("app1", "3_auto")}, Operations: []m.Operation{noop()}},
		},
		"app2": {
			{App: "app2", Name: "1_auto",
				Dependencies: []m.Key{k("app1", "1_auto")}, Operations: []m.Operation{noop()}},
			{App: "app2", Name: "1_squashed_2",
				Replaces:     []m.Key{k("app2", "1_auto"), k("app2", "2_auto")},
				Dependencies: []m.Key{k("app1", "1_auto")},
				Operations:   []m.Operation{noop()}},
			{App: "app2", Name: "2_auto",
				Dependencies: []m.Key{k("app2", "1_auto")}, Operations: []m.Operation{noop()}},
		},
	}
}

// django: tests/migrations/test_migrations_squashed_ref_squashed/
func squashedRefSquashed() migset {
	return migset{
		"app1": {
			{App: "app1", Name: "1_auto", Operations: []m.Operation{noop()}},
			{App: "app1", Name: "2_auto",
				Dependencies: []m.Key{k("app1", "1_auto")}, Operations: []m.Operation{noop()}},
			{App: "app1", Name: "2_squashed_3",
				Replaces:     []m.Key{k("app1", "2_auto"), k("app1", "3_auto")},
				Dependencies: []m.Key{k("app1", "1_auto"), k("app2", "1_squashed_2")},
				Operations:   []m.Operation{noop()}},
			{App: "app1", Name: "3_auto",
				Dependencies: []m.Key{k("app1", "2_auto"), k("app2", "2_auto")},
				Operations:   []m.Operation{noop()}},
			{App: "app1", Name: "4_auto",
				Dependencies: []m.Key{k("app1", "2_squashed_3")}, Operations: []m.Operation{noop()}},
		},
		"app2": {
			{App: "app2", Name: "1_auto",
				Dependencies: []m.Key{k("app1", "1_auto")}, Operations: []m.Operation{noop()}},
			{App: "app2", Name: "1_squashed_2",
				Replaces:     []m.Key{k("app2", "1_auto"), k("app2", "2_auto")},
				Dependencies: []m.Key{k("app1", "1_auto")},
				Operations:   []m.Operation{noop()}},
			{App: "app2", Name: "2_auto",
				Dependencies: []m.Key{k("app2", "1_auto")}, Operations: []m.Operation{noop()}},
		},
	}
}

// ---------------------------------------------------------------------------
// LoaderTests
// ---------------------------------------------------------------------------

// django: tests/migrations/test_loader.py LoaderTests.test_load
func TestLoader_Load(t *testing.T) {
	// INSTALLED_APPS append "basic": an app without migrations.
	cfg := testMigrations().config(&appliedSet{}, AppSpec{Label: "basic"})
	l := newLoader(t, cfg)

	assertPlan(t, mustForwardsPlan(t, l, k("migrations", "0002_second")),
		k("migrations", "0001_initial"), k("migrations", "0002_second"))

	state, err := l.ProjectState([]m.Key{k("migrations", "0002_second")}, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Models) != 2 {
		t.Fatalf("len(models) = %d, want 2", len(state.Models))
	}
	author := state.Models[m.ModelKey{App: "migrations", Model: "author"}]
	if got, want := author.Fields.Names(), []string{"id", "name", "slug", "age", "rating"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("author fields = %v, want %v", got, want)
	}
	book := state.Models[m.ModelKey{App: "migrations", Model: "book"}]
	if got, want := book.Fields.Names(), []string{"id", "author"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("book fields = %v, want %v", got, want)
	}
	// Unmigrated apps are included as real apps.
	if !state.RealApps["basic"] {
		t.Fatalf("real apps = %v, want it to contain \"basic\"", state.RealApps)
	}
}

// django: tests/migrations/test_loader.py LoaderTests.test_plan_handles_repeated_migrations
//
// MigrationGraph._generate_plan() is unexported in Go, so the test observes
// it through MakeState: a migration common to two targets must be applied
// exactly once, in the order the plan dictates.
func TestLoader_PlanHandlesRepeatedMigrations(t *testing.T) {
	var log []m.Key
	trace := func(key m.Key) m.Operation {
		return &traceOp{RunGo: &m.RunGo{Code: m.RunGoNoop}, log: &log, key: key}
	}
	set := migset{
		"migrations": {
			{App: "migrations", Name: "0001_initial",
				Operations: []m.Operation{trace(k("migrations", "0001_initial"))}},
			{App: "migrations", Name: "0002_second",
				Dependencies: []m.Key{k("migrations", "0001_initial")},
				Operations:   []m.Operation{trace(k("migrations", "0002_second"))}},
		},
		"migrations2": {
			{App: "migrations2", Name: "0001_initial",
				Dependencies: []m.Key{k("migrations", "0002_second")},
				Operations:   []m.Operation{trace(k("migrations2", "0001_initial"))}},
		},
	}
	l := newLoader(t, set.config(&appliedSet{}))
	nodes := []m.Key{k("migrations", "0002_second"), k("migrations2", "0001_initial")}
	if _, err := l.ProjectState(nodes, true, nil); err != nil {
		t.Fatal(err)
	}
	assertPlan(t, log,
		k("migrations", "0001_initial"),
		k("migrations", "0002_second"),
		k("migrations2", "0001_initial"))
}

// django: tests/migrations/test_loader.py LoaderTests.test_load_unmigrated_dependency
//
// Django's fixture depends on ("auth", "__first__") where auth has real
// migrations; the interesting gormgate cases are both halves of check_key:
// an app with migrations resolves __first__ to its root node, an app without
// migrations drops the dependency.
func TestLoader_LoadUnmigratedDependency(t *testing.T) {
	book := &m.CreateModel{Name: "Book", Table: "migrations_book", Fields: m.Fields{
		{Name: "id", Field: autoPK()},
		{Name: "user", Field: fkField("auth.User")},
	}}

	t.Run("dependency_on_unmigrated_app", func(t *testing.T) {
		set := migset{"migrations": {
			{App: "migrations", Name: "0001_initial",
				Dependencies: []m.Key{m.First("auth")},
				Operations:   []m.Operation{book}},
		}}
		l := newLoader(t, set.config(&appliedSet{}, AppSpec{Label: "auth"}))
		assertPlan(t, mustForwardsPlan(t, l, k("migrations", "0001_initial")),
			k("migrations", "0001_initial"))

		state, err := l.ProjectState([]m.Key{k("migrations", "0001_initial")}, true, nil)
		if err != nil {
			t.Fatal(err)
		}
		if n := len(state.Models); n != 1 {
			t.Fatalf("len(models) = %d, want 1", n)
		}
		bookState := state.Models[m.ModelKey{App: "migrations", Model: "book"}]
		if got, want := bookState.Fields.Names(), []string{"id", "user"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("book fields = %v, want %v", got, want)
		}
	})

	t.Run("dependency_on_migrated_app", func(t *testing.T) {
		set := migset{
			"migrations": {
				{App: "migrations", Name: "0001_initial",
					Dependencies: []m.Key{m.First("auth")},
					Operations:   []m.Operation{book}},
			},
			"auth": {
				{App: "auth", Name: "0001_initial", Operations: []m.Operation{noop()}},
				{App: "auth", Name: "0002_second",
					Dependencies: []m.Key{k("auth", "0001_initial")}, Operations: []m.Operation{noop()}},
			},
		}
		l := newLoader(t, set.config(&appliedSet{}))
		// __first__ resolves to auth's root node, not its leaf.
		assertPlan(t, mustForwardsPlan(t, l, k("migrations", "0001_initial")),
			k("auth", "0001_initial"), k("migrations", "0001_initial"))
	})

	t.Run("latest_dependency_on_migrated_app", func(t *testing.T) {
		set := migset{
			"migrations": {
				{App: "migrations", Name: "0001_initial",
					Dependencies: []m.Key{m.Latest("auth")},
					Operations:   []m.Operation{book}},
			},
			"auth": {
				{App: "auth", Name: "0001_initial", Operations: []m.Operation{noop()}},
				{App: "auth", Name: "0002_second",
					Dependencies: []m.Key{k("auth", "0001_initial")}, Operations: []m.Operation{noop()}},
			},
		}
		l := newLoader(t, set.config(&appliedSet{}))
		assertPlan(t, mustForwardsPlan(t, l, k("migrations", "0001_initial")),
			k("auth", "0001_initial"), k("auth", "0002_second"), k("migrations", "0001_initial"))
	})
}

// django: tests/migrations/test_loader.py LoaderTests.test_run_before
func TestLoader_RunBefore(t *testing.T) {
	l := newLoader(t, runBeforeMigrations().config(&appliedSet{}))
	assertPlan(t, mustForwardsPlan(t, l, k("migrations", "0002_second")),
		k("migrations", "0001_initial"),
		k("migrations", "0003_third"),
		k("migrations", "0002_second"))
}

// django: tests/migrations/test_loader.py LoaderTests.test_first
func TestLoader_First(t *testing.T) {
	l := newLoader(t, firstMigrations().config(&appliedSet{}))
	assertPlan(t, mustForwardsPlan(t, l, k("migrations", "second")),
		k("migrations", "thefirst"),
		k("migrations2", "0001_initial"),
		k("migrations2", "0002_second"),
		k("migrations", "second"))
}

// django: tests/migrations/test_loader.py LoaderTests.test_name_match
func TestLoader_NameMatch(t *testing.T) {
	l := newLoader(t, testMigrations().config(&appliedSet{}))

	mig, err := l.GetMigrationByPrefix("migrations", "0001")
	if err != nil {
		t.Fatal(err)
	}
	if mig.Name != "0001_initial" {
		t.Fatalf("name = %q, want %q", mig.Name, "0001_initial")
	}

	_, err = l.GetMigrationByPrefix("migrations", "0")
	var ambiguity *m.AmbiguityError
	if !errors.As(err, &ambiguity) {
		t.Fatalf("expected *m.AmbiguityError, got %v (%T)", err, err)
	}
	if want := "there is more than one migration for 'migrations' with the prefix '0'"; ambiguity.Msg != want {
		t.Fatalf("message = %q, want %q", ambiguity.Msg, want)
	}

	_, err = l.GetMigrationByPrefix("migrations", "blarg")
	var keyErr *MigrationNotFoundError
	if !errors.As(err, &keyErr) {
		t.Fatalf("expected *MigrationNotFoundError, got %v (%T)", err, err)
	}
	if want := "there is no migration for 'migrations' with the prefix 'blarg'"; keyErr.Msg != want {
		t.Fatalf("message = %q, want %q", keyErr.Msg, want)
	}
}

// django: tests/migrations/test_loader.py LoaderTests.test_marked_as_migrated
func TestLoader_MarkedAsMigrated(t *testing.T) {
	set := migset{"migrated_app": {
		{App: "migrated_app", Name: "0001_initial", Operations: []m.Operation{noop()}},
	}}
	l := newLoader(t, set.config(&appliedSet{}))
	if got, want := l.MigratedApps, map[string]bool{"migrated_app": true}; !reflect.DeepEqual(got, want) {
		t.Fatalf("migrated apps = %v, want %v", got, want)
	}
	if len(l.UnmigratedApps) != 0 {
		t.Fatalf("unmigrated apps = %v, want empty", l.UnmigratedApps)
	}
}

// django: tests/migrations/test_loader.py LoaderTests.test_marked_as_unmigrated
//
// Django disables an app's migrations with MIGRATION_MODULES = {app: None};
// gormgate's equivalent is AppSpec.Disabled.
func TestLoader_MarkedAsUnmigrated(t *testing.T) {
	set := migset{"migrated_app": {
		{App: "migrated_app", Name: "0001_initial", Operations: []m.Operation{noop()}},
	}}
	cfg := set.config(&appliedSet{})
	cfg.Apps = []AppSpec{{Label: "migrated_app", Disabled: true}}
	l := newLoader(t, cfg)
	if len(l.MigratedApps) != 0 {
		t.Fatalf("migrated apps = %v, want empty", l.MigratedApps)
	}
	if got, want := l.UnmigratedApps, map[string]bool{"migrated_app": true}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unmigrated apps = %v, want %v", got, want)
	}
	if len(l.DiskMigrations) != 0 {
		t.Fatalf("disk migrations = %v, want none", l.DiskMigrations)
	}
}

// django: tests/migrations/test_loader.py LoaderTests.test_explicit_missing_module
//
// Django raises ImportError for a migrations module that can't be imported
// unless ignore_no_migrations=True. gormgate has no import step; the
// equivalent failure is check_key finding an app that is migrated but has no
// migrations at all, which is also what ignore_no_migrations covers.
func TestLoader_ExplicitMissingModule(t *testing.T) {
	set := migset{
		"migrations": {
			{App: "migrations", Name: "0001_initial",
				Dependencies: []m.Key{m.First("empty_app")},
				Operations:   []m.Operation{noop()}},
		},
		"empty_app": {},
	}

	_, err := New(set.config(&appliedSet{}))
	var valueErr *m.ValueError
	if !errors.As(err, &valueErr) {
		t.Fatalf("expected *m.ValueError, got %v (%T)", err, err)
	}
	if want := "dependency on app with no migrations: empty_app"; valueErr.Msg != want {
		t.Fatalf("message = %q, want %q", valueErr.Msg, want)
	}

	cfg := set.config(&appliedSet{})
	cfg.IgnoreNoMigrations = true
	l := newLoader(t, cfg)
	assertPlan(t, mustForwardsPlan(t, l, k("migrations", "0001_initial")),
		k("migrations", "0001_initial"))
}

// django: db/migrations/loader.py MigrationLoader.check_key (the
// "Dependency on unknown app" branch, which Django covers in
// tests/migrations/test_commands.py).
func TestLoader_DependencyOnUnknownApp(t *testing.T) {
	set := migset{"migrations": {
		{App: "migrations", Name: "0001_initial",
			Dependencies: []m.Key{m.First("nope")},
			Operations:   []m.Operation{noop()}},
	}}
	_, err := New(set.config(&appliedSet{}))
	var valueErr *m.ValueError
	if !errors.As(err, &valueErr) {
		t.Fatalf("expected *m.ValueError, got %v (%T)", err, err)
	}
	if want := "dependency on unknown app: nope"; valueErr.Msg != want {
		t.Fatalf("message = %q, want %q", valueErr.Msg, want)
	}
}

// django: tests/migrations/test_loader.py LoaderTests.test_loading_squashed
func TestLoader_LoadingSquashed(t *testing.T) {
	applied := &appliedSet{}
	l := newLoader(t, squashedMigrations().config(applied))
	// Nothing applied: the squashed migration replaces both nodes.
	if got := appNodes(l, "migrations"); got != 1 {
		t.Fatalf("nodes = %d, want 1", got)
	}
	// Fake-apply one of the replaced migrations: the squash can no longer be
	// used, so the two original nodes come back.
	applied.recordApplied("migrations", "0001_initial")
	if err := l.BuildGraph(); err != nil {
		t.Fatal(err)
	}
	if got := appNodes(l, "migrations"); got != 2 {
		t.Fatalf("nodes = %d, want 2", got)
	}
}

// django: tests/migrations/test_loader.py LoaderTests.test_loading_squashed_complex
func TestLoader_LoadingSquashedComplex(t *testing.T) {
	applied := &appliedSet{}
	l := newLoader(t, squashedComplexMigrations().config(applied))

	numNodes := func() int {
		return len(unapplied(l, mustForwardsPlan(t, l, k("migrations", "7_auto"))))
	}
	rebuild := func() {
		if err := l.BuildGraph(); err != nil {
			t.Fatal(err)
		}
	}

	// Empty database: use the squashed migration.
	if got := numNodes(); got != 5 {
		t.Fatalf("nodes = %d, want 5", got)
	}
	for _, step := range []struct {
		apply string
		want  int
	}{
		// Starting at 1 or 2 should use the squashed migration too.
		{"1_auto", 4},
		{"2_auto", 3},
		// However, starting at 3 to 5 cannot use the squashed migration.
		{"3_auto", 4},
		{"4_auto", 3},
		// Starting at 5 to 7 we are past the squashed migrations.
		{"5_auto", 2},
		{"6_auto", 1},
		{"7_auto", 0},
	} {
		applied.recordApplied("migrations", step.apply)
		rebuild()
		if got := numNodes(); got != step.want {
			t.Fatalf("after applying %s: nodes = %d, want %d", step.apply, got, step.want)
		}
	}
}

// django: tests/migrations/test_loader.py LoaderTests.test_loading_squashed_complex_multi_apps
func TestLoader_LoadingSquashedComplexMultiApps(t *testing.T) {
	l := newLoader(t, squashedComplexMultiApps().config(&appliedSet{}))
	assertPlanSet(t, mustForwardsPlan(t, l, k("app1", "4_auto")),
		k("app1", "1_auto"),
		k("app2", "1_squashed_2"),
		k("app1", "2_squashed_3"),
		k("app1", "4_auto"))
}

// django: tests/migrations/test_loader.py LoaderTests.test_loading_squashed_complex_multi_apps_partially_applied
func TestLoader_LoadingSquashedComplexMultiAppsPartiallyApplied(t *testing.T) {
	applied := &appliedSet{}
	applied.recordApplied("app1", "1_auto")
	applied.recordApplied("app1", "2_auto")
	l := newLoader(t, squashedComplexMultiApps().config(applied))

	plan := unapplied(l, mustForwardsPlan(t, l, k("app1", "4_auto")))
	assertPlanSet(t, plan,
		k("app2", "1_squashed_2"),
		k("app1", "3_auto"),
		k("app1", "4_auto"))
}

// django: tests/migrations/test_loader.py LoaderTests.test_loading_squashed_erroneous
func TestLoader_LoadingSquashedErroneous(t *testing.T) {
	applied := &appliedSet{}
	cfg := squashedErroneousMigrations().config(applied)
	l := newLoader(t, cfg)

	numNodes := func() int {
		return len(unapplied(l, mustForwardsPlan(t, l, k("migrations", "7_auto"))))
	}

	// Empty database: use the squashed migration.
	if got := numNodes(); got != 5 {
		t.Fatalf("nodes = %d, want 5", got)
	}
	applied.recordApplied("migrations", "1_auto")
	if err := l.BuildGraph(); err != nil {
		t.Fatal(err)
	}
	if got := numNodes(); got != 4 {
		t.Fatalf("nodes = %d, want 4", got)
	}
	applied.recordApplied("migrations", "2_auto")
	if err := l.BuildGraph(); err != nil {
		t.Fatal(err)
	}
	if got := numNodes(); got != 3 {
		t.Fatalf("nodes = %d, want 3", got)
	}

	// However, starting at 3 or 4, nonexistent migrations would be needed.
	// Django's message names Django; gormgate's names gormgate.
	const wantMsg = "Migration migrations.6_auto depends on nonexistent node " +
		"('migrations', '5_auto'). gormgate tried to replace migration " +
		"migrations.5_auto with any of [migrations.3_squashed_5] but wasn't able " +
		"to because some of the replaced migrations are already applied."
	for _, name := range []string{"3_auto", "4_auto"} {
		applied.recordApplied("migrations", name)
		err := l.BuildGraph()
		var notFound *m.NodeNotFoundError
		if !errors.As(err, &notFound) {
			t.Fatalf("after applying %s: expected *m.NodeNotFoundError, got %v (%T)", name, err, err)
		}
		if notFound.Message != wantMsg {
			t.Fatalf("after applying %s:\n got: %s\nwant: %s", name, notFound.Message, wantMsg)
		}
	}

	// Starting at 5 to 7 we are past the squashed migrations.
	for _, step := range []struct {
		apply string
		want  int
	}{{"5_auto", 2}, {"6_auto", 1}, {"7_auto", 0}} {
		applied.recordApplied("migrations", step.apply)
		if err := l.BuildGraph(); err != nil {
			t.Fatalf("after applying %s: %v", step.apply, err)
		}
		if got := numNodes(); got != step.want {
			t.Fatalf("after applying %s: nodes = %d, want %d", step.apply, got, step.want)
		}
	}
}

// django: tests/migrations/test_loader.py LoaderTests.test_check_consistent_history
func TestLoader_CheckConsistentHistory(t *testing.T) {
	applied := &appliedSet{}
	// Django builds the loader with connection=None, i.e. without the
	// applied migrations, and passes the connection to check_consistent_history.
	cfg := testMigrations().config(nil)
	l := newLoader(t, cfg)

	if err := l.CheckConsistentHistory(applied, "default"); err != nil {
		t.Fatalf("consistent history should pass on an empty database: %v", err)
	}

	applied.recordApplied("migrations", "0002_second")
	err := l.CheckConsistentHistory(applied, "default")
	var inconsistent *m.InconsistentMigrationHistory
	if !errors.As(err, &inconsistent) {
		t.Fatalf("expected *m.InconsistentMigrationHistory, got %v (%T)", err, err)
	}
	want := "migration migrations.0002_second is applied before its dependency " +
		"migrations.0001_initial on database 'default'"
	if inconsistent.Msg != want {
		t.Fatalf("message = %q, want %q", inconsistent.Msg, want)
	}
}

// django: tests/migrations/test_loader.py LoaderTests.test_check_consistent_history_squashed
func TestLoader_CheckConsistentHistorySquashed(t *testing.T) {
	applied := &appliedSet{}
	l := newLoader(t, squashedExtraMigrations().config(applied))

	applied.recordApplied("migrations", "0001_initial")
	applied.recordApplied("migrations", "0002_second")
	if err := l.BuildGraph(); err != nil {
		t.Fatal(err)
	}
	if err := l.CheckConsistentHistory(applied, "default"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 0003_third depends on 0002_second, which the squash replaced; the
	// squash counts as applied because all of its `replaces` are.
	applied.recordApplied("migrations", "0003_third")
	if err := l.BuildGraph(); err != nil {
		t.Fatal(err)
	}
	if err := l.CheckConsistentHistory(applied, "default"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// django: tests/migrations/test_loader.py LoaderTests.test_loading_squashed_ref_squashed
func TestLoader_LoadingSquashedRefSquashed(t *testing.T) {
	applied := &appliedSet{}
	l := newLoader(t, squashedRefSquashed().config(applied))

	// Load with nothing applied: both migrations squashed.
	assertPlanSet(t, unapplied(l, mustForwardsPlan(t, l, k("app1", "4_auto"))),
		k("app1", "1_auto"),
		k("app2", "1_squashed_2"),
		k("app1", "2_squashed_3"),
		k("app1", "4_auto"))

	// Migrating to a replaced migration is impossible while replacements are
	// enabled.
	_, err := l.Graph.ForwardsPlan(k("app1", "3_auto"))
	var notFound *m.NodeNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("expected *m.NodeNotFoundError, got %v (%T)", err, err)
	}
	if want := "Node ('app1', '3_auto') not a valid node"; notFound.Message != want {
		t.Fatalf("message = %q, want %q", notFound.Message, want)
	}

	// ... and possible with replacements disabled.
	l.cfg.ReplaceMigrations = false
	if err := l.BuildGraph(); err != nil {
		t.Fatal(err)
	}
	assertPlanSet(t, unapplied(l, mustForwardsPlan(t, l, k("app1", "3_auto"))),
		k("app1", "1_auto"),
		k("app2", "1_auto"),
		k("app2", "2_auto"),
		k("app1", "2_auto"),
		k("app1", "3_auto"))
	l.cfg.ReplaceMigrations = true

	// Fake-apply a few from app1: unsquashes the migration in app1.
	applied.recordApplied("app1", "1_auto")
	applied.recordApplied("app1", "2_auto")
	if err := l.BuildGraph(); err != nil {
		t.Fatal(err)
	}
	assertPlanSet(t, unapplied(l, mustForwardsPlan(t, l, k("app1", "4_auto"))),
		k("app2", "1_squashed_2"),
		k("app1", "3_auto"),
		k("app1", "4_auto"))

	// Fake-apply one from app2: unsquashes the migration in app2 too.
	applied.recordApplied("app2", "1_auto")
	if err := l.BuildGraph(); err != nil {
		t.Fatal(err)
	}
	assertPlanSet(t, unapplied(l, mustForwardsPlan(t, l, k("app1", "4_auto"))),
		k("app2", "2_auto"),
		k("app1", "3_auto"),
		k("app1", "4_auto"))
}

// django: db/migrations/loader.py MigrationLoader._resolve_replaced_migration_keys
//
// A squash of a squash resolves transitively to the leaf migrations, so the
// applied status of the originals decides whether the outer squash can be
// used. Django covers this through the squashmigrations command tests.
func TestLoader_SquashOfSquash(t *testing.T) {
	set := func() migset {
		return migset{"migrations": {
			{App: "migrations", Name: "0001_initial", Operations: []m.Operation{noop()}},
			{App: "migrations", Name: "0002_second",
				Dependencies: []m.Key{k("migrations", "0001_initial")}, Operations: []m.Operation{noop()}},
			{App: "migrations", Name: "0003_third",
				Dependencies: []m.Key{k("migrations", "0002_second")}, Operations: []m.Operation{noop()}},
			{App: "migrations", Name: "0001_squashed_0002",
				Replaces:   []m.Key{k("migrations", "0001_initial"), k("migrations", "0002_second")},
				Operations: []m.Operation{noop()}},
			{App: "migrations", Name: "0001_squashed_0003",
				Replaces:   []m.Key{k("migrations", "0001_squashed_0002"), k("migrations", "0003_third")},
				Operations: []m.Operation{noop()}},
		}}
	}

	t.Run("nothing_applied", func(t *testing.T) {
		l := newLoader(t, set().config(&appliedSet{}))
		if got := appNodes(l, "migrations"); got != 1 {
			t.Fatalf("nodes = %d, want 1 (only the outer squash)", got)
		}
		if !l.Graph.Has(k("migrations", "0001_squashed_0003")) {
			t.Fatal("expected the outer squash to remain")
		}
	})

	t.Run("all_replaced_applied", func(t *testing.T) {
		applied := &appliedSet{}
		for _, n := range []string{"0001_initial", "0002_second", "0003_third"} {
			applied.recordApplied("migrations", n)
		}
		l := newLoader(t, set().config(applied))
		if got := appNodes(l, "migrations"); got != 1 {
			t.Fatalf("nodes = %d, want 1", got)
		}
		// Both squashes count as applied: every migration they resolve to is.
		for _, n := range []string{"0001_squashed_0002", "0001_squashed_0003"} {
			if !l.Applied[k("migrations", n)] {
				t.Fatalf("%s should be marked applied", n)
			}
		}
	})

	t.Run("partially_applied", func(t *testing.T) {
		applied := &appliedSet{}
		applied.recordApplied("migrations", "0001_initial")
		l := newLoader(t, set().config(applied))
		// The inner squash is partially applied, so it is dropped; the outer
		// one resolves to {0001, 0002, 0003} and is partially applied too.
		if l.Graph.Has(k("migrations", "0001_squashed_0002")) {
			t.Fatal("partially applied inner squash should have been removed")
		}
		if l.Graph.Has(k("migrations", "0001_squashed_0003")) {
			t.Fatal("partially applied outer squash should have been removed")
		}
		assertPlan(t, mustForwardsPlan(t, l, k("migrations", "0003_third")),
			k("migrations", "0001_initial"),
			k("migrations", "0002_second"),
			k("migrations", "0003_third"))
	})
}

// django: db/migrations/loader.py MigrationLoader.replace_migration
// (the "Cyclical squash replacement found" CommandError).
func TestLoader_CyclicalSquashReplacement(t *testing.T) {
	set := migset{"migrations": {
		{App: "migrations", Name: "0001_a",
			Replaces: []m.Key{k("migrations", "0002_b")}, Operations: []m.Operation{noop()}},
		{App: "migrations", Name: "0002_b",
			Replaces: []m.Key{k("migrations", "0001_a")}, Operations: []m.Operation{noop()}},
	}}
	_, err := New(set.config(&appliedSet{}))
	var cmdErr *CommandError
	if !errors.As(err, &cmdErr) {
		t.Fatalf("expected *CommandError, got %v (%T)", err, err)
	}
	if want := "cyclical squash replacement found, starting at ('migrations', '0001_a')"; cmdErr.Msg != want {
		t.Fatalf("message = %q, want %q", cmdErr.Msg, want)
	}
}

// django: db/migrations/loader.py MigrationLoader.detect_conflicts
// (Django exercises it through makemigrations in tests/migrations/test_commands.py).
func TestLoader_DetectConflicts(t *testing.T) {
	t.Run("no_conflicts", func(t *testing.T) {
		l := newLoader(t, testMigrations().config(&appliedSet{}))
		if got := l.DetectConflicts(); len(got) != 0 {
			t.Fatalf("conflicts = %v, want none", got)
		}
	})

	t.Run("two_leaves", func(t *testing.T) {
		set := testMigrations()
		set["migrations"] = append(set["migrations"], &m.Migration{
			App: "migrations", Name: "0002_conflicting",
			Dependencies: []m.Key{k("migrations", "0001_initial")},
			Operations:   []m.Operation{noop()},
		})
		l := newLoader(t, set.config(&appliedSet{}))
		want := map[string][]string{"migrations": {"0002_conflicting", "0002_second"}}
		if got := l.DetectConflicts(); !reflect.DeepEqual(got, want) {
			t.Fatalf("conflicts = %v, want %v", got, want)
		}
	})

	t.Run("conflicts_in_one_app_only", func(t *testing.T) {
		set := testMigrations()
		set["migrations"] = append(set["migrations"], &m.Migration{
			App: "migrations", Name: "0002_conflicting",
			Dependencies: []m.Key{k("migrations", "0001_initial")},
			Operations:   []m.Operation{noop()},
		})
		set["other"] = []*m.Migration{
			{App: "other", Name: "0001_initial", Operations: []m.Operation{noop()}},
		}
		l := newLoader(t, set.config(&appliedSet{}))
		got := l.DetectConflicts()
		if _, ok := got["other"]; ok {
			t.Fatalf("app 'other' has a single leaf and must not conflict: %v", got)
		}
		if len(got) != 1 {
			t.Fatalf("conflicts = %v, want only 'migrations'", got)
		}
	})
}

// django: db/migrations/loader.py MigrationLoader.__init__ (replace_migrations=False)
func TestLoader_ReplaceMigrationsDisabled(t *testing.T) {
	cfg := squashedMigrations().config(&appliedSet{})
	cfg.ReplaceMigrations = false
	l := newLoader(t, cfg)
	// All three nodes are kept, and the squash is a leaf of its own.
	if got := appNodes(l, "migrations"); got != 3 {
		t.Fatalf("nodes = %d, want 3", got)
	}
	for _, name := range []string{"0001_initial", "0002_second", "0001_squashed_0002"} {
		if !l.Graph.Has(k("migrations", name)) {
			t.Fatalf("%s should still be a node", name)
		}
	}
}

// django: db/migrations/loader.py MigrationLoader.load_disk (the
// BadMigrationError raised for a migration declaring the wrong app).
func TestLoader_BadMigrationWrongApp(t *testing.T) {
	set := migset{"migrations": {
		{App: "other", Name: "0001_initial", Operations: []m.Operation{noop()}},
	}}
	_, err := New(set.config(&appliedSet{}))
	var bad *m.BadMigrationError
	if !errors.As(err, &bad) {
		t.Fatalf("expected *m.BadMigrationError, got %v (%T)", err, err)
	}
	if !strings.Contains(bad.Msg, "declares app 'other'") {
		t.Fatalf("message = %q", bad.Msg)
	}
}

// django: db/migrations/graph.py MigrationGraph.ensure_not_cyclic, raised
// from MigrationLoader.build_graph.
func TestLoader_CircularDependency(t *testing.T) {
	set := migset{"migrations": {
		{App: "migrations", Name: "0001_initial",
			Dependencies: []m.Key{k("migrations", "0002_second")}, Operations: []m.Operation{noop()}},
		{App: "migrations", Name: "0002_second",
			Dependencies: []m.Key{k("migrations", "0001_initial")}, Operations: []m.Operation{noop()}},
	}}
	_, err := New(set.config(&appliedSet{}))
	var circular *m.CircularDependencyError
	if !errors.As(err, &circular) {
		t.Fatalf("expected *m.CircularDependencyError, got %v (%T)", err, err)
	}
}

// django: db/migrations/loader.py MigrationLoader.build_graph (the plain
// NodeNotFoundError for a dependency that simply does not exist).
func TestLoader_MissingDependency(t *testing.T) {
	set := migset{"migrations": {
		{App: "migrations", Name: "0002_second",
			Dependencies: []m.Key{k("migrations", "0001_initial")}, Operations: []m.Operation{noop()}},
	}}
	_, err := New(set.config(&appliedSet{}))
	var notFound *m.NodeNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("expected *m.NodeNotFoundError, got %v (%T)", err, err)
	}
	want := "Migration migrations.0002_second dependencies reference nonexistent " +
		"parent node ('migrations', '0001_initial')"
	if notFound.Message != want {
		t.Fatalf("message = %q, want %q", notFound.Message, want)
	}
}

// django: db/migrations/loader.py MigrationLoader.get_migration /
// MigrationLoader.project_state
func TestLoader_GetMigrationAndProjectState(t *testing.T) {
	l := newLoader(t, testMigrations().config(&appliedSet{}))

	mig, ok := l.GetMigration("migrations", "0001_initial")
	if !ok || mig.Name != "0001_initial" {
		t.Fatalf("GetMigration = %v, %v", mig, ok)
	}
	if _, ok := l.GetMigration("migrations", "nope"); ok {
		t.Fatal("GetMigration should report a missing migration")
	}

	// project_state() with nil nodes uses the leaf nodes.
	state, err := l.ProjectState(nil, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Models) != 2 {
		t.Fatalf("len(models) = %d, want 2", len(state.Models))
	}

	// ... and at_end=false gives the state before the target.
	before, err := l.ProjectState([]m.Key{k("migrations", "0002_second")}, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := before.Models[m.ModelKey{App: "migrations", Model: "book"}]; ok {
		t.Fatal("Book must not exist before 0002_second")
	}
	if _, ok := before.Models[m.ModelKey{App: "migrations", Model: "tribble"}]; !ok {
		t.Fatal("Tribble must still exist before 0002_second")
	}
}

// ---------------------------------------------------------------------------
// On-disk consistency check (gormgate-specific: it has no Django counterpart,
// because Django imports the migration files it finds on disk while gormgate
// compiles them in and only compares the two).
// ---------------------------------------------------------------------------

func writeMigrationFiles(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("package migrations\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// django: tests/migrations/test_loader.py LoaderTests.test_ignore_files
//
// Files prefixed with an underscore, a tilde or a dot aren't migrations; in
// gormgate the rule is the ^\d{4}_\w*\.go$ file-name pattern.
func TestLoader_IgnoreFiles(t *testing.T) {
	dir := writeMigrationFiles(t, "0001_initial.go", "_util.go", "~util.go", ".util.go", "migrations.go")
	files, exists, err := migrationFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatal("directory should exist")
	}
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	if got, want := names, []string{"0001_initial"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("migrations = %v, want %v", got, want)
	}

	// A name the go tool would otherwise treat as a build constraint is
	// written with a trailing underscore and maps back to the plain name.
	dir2 := writeMigrationFiles(t, "0002_add_linux_.go")
	files2, _, err := migrationFiles(dir2)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := files2["0002_add_linux"]; !ok {
		t.Fatalf("migrations = %v, want 0002_add_linux", files2)
	}
}

// django: tests/migrations/test_loader.py LoaderTests.test_load_empty_dir
//
// An app whose migrations directory holds no migrations is unmigrated.
func TestLoader_LoadEmptyDir(t *testing.T) {
	dir := writeMigrationFiles(t, "migrations.go")
	cfg := Config{
		Apps:              []AppSpec{{Label: "migrations", Dir: dir}},
		Registered:        func(string) []*m.Migration { return nil },
		Compiled:          func(string) bool { return false },
		ReplaceMigrations: true,
	}
	l := newLoader(t, cfg)
	if !l.UnmigratedApps["migrations"] {
		t.Fatalf("unmigrated apps = %v, want it to contain \"migrations\"", l.UnmigratedApps)
	}
}

// django: tests/migrations/test_loader.py LoaderTests.test_load_import_error
//
// Django reraises the ImportError of a broken migrations module; gormgate's
// equivalent failure is a migrations directory whose package was never
// compiled into the running command.
func TestLoader_LoadNotCompiled(t *testing.T) {
	dir := writeMigrationFiles(t, "0001_initial.go")
	cfg := Config{
		Apps:              []AppSpec{{Label: "migrations", Dir: dir}},
		Registered:        func(string) []*m.Migration { return nil },
		Compiled:          func(string) bool { return false },
		ReplaceMigrations: true,
	}
	_, err := New(cfg)
	var cmdErr *CommandError
	if !errors.As(err, &cmdErr) {
		t.Fatalf("expected *CommandError, got %v (%T)", err, err)
	}
	if !strings.Contains(cmdErr.Msg, "are not compiled into this command") {
		t.Fatalf("message = %q", cmdErr.Msg)
	}

	// SkipDiskCheck turns the check off again.
	cfg.SkipDiskCheck = true
	l := newLoader(t, cfg)
	if !l.UnmigratedApps["migrations"] {
		t.Fatalf("unmigrated apps = %v", l.UnmigratedApps)
	}
}

// django: db/migrations/loader.py MigrationLoader.load_disk (gormgate's
// compiled-vs-disk consistency check has no Django counterpart).
func TestLoader_DiskConsistency(t *testing.T) {
	mig := func(name, file string) *m.Migration {
		return &m.Migration{App: "migrations", Name: name, File: file, Operations: []m.Operation{noop()}}
	}

	t.Run("in_sync", func(t *testing.T) {
		dir := writeMigrationFiles(t, "0001_initial.go", "0002_second.go")
		cfg := Config{
			Apps: []AppSpec{{Label: "migrations", Dir: dir}},
			Registered: func(string) []*m.Migration {
				return []*m.Migration{
					mig("0001_initial", filepath.Join(dir, "0001_initial.go")),
					{App: "migrations", Name: "0002_second",
						Dependencies: []m.Key{k("migrations", "0001_initial")},
						File:         filepath.Join(dir, "0002_second.go"),
						Operations:   []m.Operation{noop()}},
				}
			},
			Compiled:          func(string) bool { return true },
			ReplaceMigrations: true,
		}
		l := newLoader(t, cfg)
		if len(l.DiskMigrations) != 2 {
			t.Fatalf("disk migrations = %v, want 2", l.DiskMigrations)
		}
	})

	t.Run("file_on_disk_not_compiled", func(t *testing.T) {
		dir := writeMigrationFiles(t, "0001_initial.go", "0002_second.go")
		cfg := Config{
			Apps: []AppSpec{{Label: "migrations", Dir: dir}},
			Registered: func(string) []*m.Migration {
				return []*m.Migration{mig("0001_initial", filepath.Join(dir, "0001_initial.go"))}
			},
			Compiled:          func(string) bool { return true },
			ReplaceMigrations: true,
		}
		_, err := New(cfg)
		var cmdErr *CommandError
		if !errors.As(err, &cmdErr) {
			t.Fatalf("expected *CommandError, got %v (%T)", err, err)
		}
		want := "migration file(s) " + filepath.Join(dir, "0002_second.go") +
			" exist on disk but are not compiled into this command; rebuild it"
		if cmdErr.Msg != want {
			t.Fatalf("message = %q, want %q", cmdErr.Msg, want)
		}
	})

	t.Run("compiled_but_file_missing", func(t *testing.T) {
		dir := writeMigrationFiles(t, "0001_initial.go")
		cfg := Config{
			Apps: []AppSpec{{Label: "migrations", Dir: dir}},
			Registered: func(string) []*m.Migration {
				return []*m.Migration{
					mig("0001_initial", filepath.Join(dir, "0001_initial.go")),
					mig("0002_second", filepath.Join(dir, "0002_second.go")),
				}
			},
			Compiled:          func(string) bool { return true },
			ReplaceMigrations: true,
		}
		_, err := New(cfg)
		var cmdErr *CommandError
		if !errors.As(err, &cmdErr) {
			t.Fatalf("expected *CommandError, got %v (%T)", err, err)
		}
		want := "migration(s) 0002_second of app 'migrations' are compiled into this " +
			"command but their files are missing from " + dir + "; rebuild it"
		if cmdErr.Msg != want {
			t.Fatalf("message = %q, want %q", cmdErr.Msg, want)
		}
	})

	t.Run("registered_from_another_file", func(t *testing.T) {
		dir := writeMigrationFiles(t, "0001_initial.go")
		cfg := Config{
			Apps: []AppSpec{{Label: "migrations", Dir: dir}},
			Registered: func(string) []*m.Migration {
				return []*m.Migration{mig("0001_initial", filepath.Join(dir, "somewhere_else.go"))}
			},
			Compiled:          func(string) bool { return true },
			ReplaceMigrations: true,
		}
		_, err := New(cfg)
		var bad *m.BadMigrationError
		if !errors.As(err, &bad) {
			t.Fatalf("expected *m.BadMigrationError, got %v (%T)", err, err)
		}
		if !strings.Contains(bad.Msg, "is registered from") {
			t.Fatalf("message = %q", bad.Msg)
		}
	})

	t.Run("no_directory_on_disk", func(t *testing.T) {
		// A deployed binary has no sources; the check is simply skipped.
		cfg := Config{
			Apps: []AppSpec{{Label: "migrations"}},
			Registered: func(string) []*m.Migration {
				return []*m.Migration{mig("0001_initial", "")}
			},
			Compiled:          func(string) bool { return true },
			ReplaceMigrations: true,
		}
		l := newLoader(t, cfg)
		if len(l.DiskMigrations) != 1 {
			t.Fatalf("disk migrations = %v, want 1", l.DiskMigrations)
		}
	})
}
