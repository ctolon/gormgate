package autodetector

// Helpers mirroring the fixtures and assertions of Django's
// tests/migrations/test_autodetector.py (BaseAutodetectorTests and the
// class-level ModelState attributes of AutodetectorTests).

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/ctolon/gormgate/internal/graph"
	"github.com/ctolon/gormgate/internal/questioner"
	m "github.com/ctolon/gormgate/migrations"
)

// ---------------------------------------------------------------------------
// model/field fixtures
// ---------------------------------------------------------------------------

// tableFor is the table name a gorm naming strategy derives from an app and a
// model name. gormgate always stores the table explicitly in the model state
// (documented deviation), so the fixtures have to pick one.
func tableFor(app, name string) string { return strings.ToLower(app + "_" + name) }

// model builds a ModelState whose table follows the model name.
func model(app, name string, fields ...m.NamedField) *m.ModelState {
	return &m.ModelState{
		App:    app,
		Name:   name,
		Table:  tableFor(app, name),
		Fields: append(m.Fields(nil), fields...),
	}
}

// withTable returns a copy of ms with an explicit table name.
func withTable(ms *m.ModelState, table string) *m.ModelState {
	c := ms.Clone()
	c.Table = table
	return c
}

// withOptions returns a copy of ms with the given options.
func withOptions(ms *m.ModelState, o m.Options) *m.ModelState {
	c := ms.Clone()
	c.Options = o
	return c
}

// renamedTo returns a copy of ms under a new model name (and the table that
// follows from it).
func renamedTo(ms *m.ModelState, name string) *m.ModelState {
	c := ms.Clone()
	c.Name = name
	c.Table = tableFor(c.App, name)
	return c
}

func field(name string, f m.Field) m.NamedField { return m.NamedField{Name: name, Field: f} }

// autoField is Django's models.AutoField(primary_key=True).
func autoField() m.Field {
	return m.Field{Type: m.Int, Size: 32, PrimaryKey: true, AutoIncrement: true}
}

// bigAutoField is Django's models.BigAutoField(primary_key=True).
func bigAutoField() m.Field {
	return m.Field{Type: m.Int, Size: 64, PrimaryKey: true, AutoIncrement: true}
}

// smallAutoField is Django's models.SmallAutoField(primary_key=True).
func smallAutoField() m.Field {
	return m.Field{Type: m.Int, Size: 16, PrimaryKey: true, AutoIncrement: true}
}

// charField is Django's models.CharField(max_length=n).
func charField(n int) m.Field { return m.Field{Type: m.String, Size: n} }

// textField is Django's models.TextField().
func textField() m.Field { return m.Field{Type: m.String} }

// intField is Django's models.IntegerField().
func intField() m.Field { return m.Field{Type: m.Int, Size: 32} }

// dateTimeField is Django's models.DateTimeField().
func dateTimeField() m.Field { return m.Field{Type: m.Time} }

// fk is Django's models.ForeignKey(to, models.CASCADE).
func fk(to string) m.Field {
	return m.Field{Type: m.Int, Size: 64, ForeignKey: &m.ForeignKey{To: to, OnDelete: m.Cascade}}
}

// fkToField is Django's models.ForeignKey(to, models.CASCADE, to_field=...).
func fkToField(to, toField string) m.Field {
	f := fk(to)
	f.ForeignKey.ToField = toField
	return f
}

func null(f m.Field) m.Field      { f.Null = true; return f }
func unique(f m.Field) m.Field    { f.Unique = true; return f }
func pkOf(f m.Field) m.Field      { f.PrimaryKey = true; return f }
func autoNow(f m.Field) m.Field   { f.AutoNow = true; return f }
func autoNowAd(f m.Field) m.Field { f.AutoNowAdd = true; return f }

func withDefault(f m.Field, v any) m.Field   { f.Default = v; return f }
func withDBDefault(f m.Field, v any) m.Field { f.DBDefault = m.DBValue(v); return f }

func checkConstraint(name, check string) *m.CheckConstraint {
	return &m.CheckConstraint{Name: name, Check: check}
}

func uniqueConstraint(name string, fields ...string) *m.UniqueConstraint {
	return &m.UniqueConstraint{Name: name, Fields: fields}
}

func index(name string, columns ...string) m.Index {
	ix := m.Index{Name: name}
	for _, c := range columns {
		ix.Fields = append(ix.Fields, m.IndexField{Column: c})
	}
	return ix
}

func managed(v bool) *bool { return m.Ptr(v) }

// ---------------------------------------------------------------------------
// project state / change detection
// ---------------------------------------------------------------------------

// projectState is BaseAutodetectorTests.make_project_state.
func projectState(models ...*m.ModelState) *m.ProjectState {
	st := m.NewProjectState()
	for _, ms := range models {
		st.AddModel(ms.Clone())
	}
	return st
}

// getChanges is BaseAutodetectorTests.get_changes: it runs _detect_changes()
// only, so migrations keep their "auto_N" names.
func getChanges(t *testing.T, before, after []*m.ModelState, q questioner.Questioner) map[string][]*m.Migration {
	t.Helper()
	return getChangesState(t, projectState(before...), projectState(after...), q)
}

func getChangesState(t *testing.T, before, after *m.ProjectState, q questioner.Questioner) map[string][]*m.Migration {
	t.Helper()
	ch, err := detect(before, after, q, nil)
	if err != nil {
		t.Fatalf("detectChanges: %v", err)
	}
	return ch
}

func detect(before, after *m.ProjectState, q questioner.Questioner, g *graph.Graph) (map[string][]*m.Migration, error) {
	if q == nil {
		q = &questioner.Base{}
	}
	return New(before, after, q).detectChanges(nil, g)
}

// defaults builds the non-interactive questioner Django spells
// MigrationQuestioner({...}).
func defaults(d questioner.Defaults) *questioner.Base {
	return &questioner.Base{Defaults: d}
}

// ---------------------------------------------------------------------------
// recording questioner
// ---------------------------------------------------------------------------

// recordingQuestioner counts the questions the autodetector asks and can fail
// the test when a question must not be asked at all. It stands in for Django's
// mock.patch of MigrationQuestioner methods.
type recordingQuestioner struct {
	questioner.Base
	t *testing.T

	// forbidNotNullAddition etc. are Django's
	// side_effect=AssertionError("Should not have prompted ...").
	forbidNotNullAddition   bool
	forbidNotNullAlteration bool

	notNullAdditionCalls   int
	notNullAlterationCalls int
	autoNowAddCalls        int
	uniqueCallableCalls    int
	renameCalls            int

	// notNullAdditionValue / notNullAlterationValue are the mocked return
	// values; nil means "no default" (the base questioner's answer).
	notNullAdditionValue   *m.GoExpr
	notNullAlterationValue *m.GoExpr
}

func (q *recordingQuestioner) AskNotNullAddition(field, model string) (*m.GoExpr, error) {
	q.t.Helper()
	if q.forbidNotNullAddition {
		q.t.Fatalf("Should not have prompted for not null addition (%s.%s)", model, field)
	}
	q.notNullAdditionCalls++
	return q.notNullAdditionValue, nil
}

func (q *recordingQuestioner) AskNotNullAlteration(field, model string) (*m.GoExpr, error) {
	q.t.Helper()
	if q.forbidNotNullAlteration {
		q.t.Fatalf("Should not have prompted for not null alteration (%s.%s)", model, field)
	}
	q.notNullAlterationCalls++
	return q.notNullAlterationValue, nil
}

func (q *recordingQuestioner) AskAutoNowAddAddition(field, model string) (*m.GoExpr, error) {
	q.autoNowAddCalls++
	return nil, nil
}

func (q *recordingQuestioner) AskUniqueCallableDefaultAddition(field, model string) error {
	q.uniqueCallableCalls++
	return nil
}

func (q *recordingQuestioner) AskRename(model, oldName, newName string, f m.Field) (bool, error) {
	q.renameCalls++
	return q.Defaults.Rename, nil
}

// ---------------------------------------------------------------------------
// assertions (django: BaseAutodetectorTests.assert*)
// ---------------------------------------------------------------------------

func reprChanges(changes map[string][]*m.Migration, includeDependencies bool) string {
	var b strings.Builder
	apps := make([]string, 0, len(changes))
	for app := range changes {
		apps = append(apps, app)
	}
	sort.Strings(apps)
	for _, app := range apps {
		fmt.Fprintf(&b, "  %s:\n", app)
		for _, mig := range changes[app] {
			fmt.Fprintf(&b, "    %s\n", mig.Name)
			for _, op := range mig.Operations {
				fmt.Fprintf(&b, "      %s\n", m.FormatOperation(op))
			}
			if includeDependencies {
				b.WriteString("      Dependencies:\n")
				if len(mig.Dependencies) == 0 {
					b.WriteString("        None\n")
				}
				for _, d := range mig.Dependencies {
					fmt.Fprintf(&b, "        ('%s', '%s')\n", d.App, d.Name)
				}
			}
		}
	}
	return b.String()
}

func assertNumberMigrations(t *testing.T, changes map[string][]*m.Migration, app string, n int) {
	t.Helper()
	if len(changes[app]) != n {
		t.Fatalf("Incorrect number of migrations (%d) for %s (expected %d)\n%s",
			len(changes[app]), app, n, reprChanges(changes, false))
	}
}

func opTypes(ops []m.Operation) []string {
	out := make([]string, len(ops))
	for i, op := range ops {
		out[i] = m.OpName(op)
	}
	return out
}

func migrationAt(t *testing.T, changes map[string][]*m.Migration, app string, pos int) *m.Migration {
	t.Helper()
	if len(changes[app]) == 0 {
		t.Fatalf("No migrations found for %s\n%s", app, reprChanges(changes, false))
	}
	if len(changes[app]) < pos+1 {
		t.Fatalf("No migration at index %d for %s\n%s", pos, app, reprChanges(changes, false))
	}
	return changes[app][pos]
}

func assertOperationTypes(t *testing.T, changes map[string][]*m.Migration, app string, pos int, types ...string) {
	t.Helper()
	mig := migrationAt(t, changes, app, pos)
	got := opTypes(mig.Operations)
	if !equalStrings(got, types) {
		t.Fatalf("Operation type mismatch for %s.%s (expected %v, got %v):\n%s",
			app, mig.Name, types, got, reprChanges(changes, false))
	}
}

func assertMigrationDependencies(t *testing.T, changes map[string][]*m.Migration, app string, pos int, deps []m.Key) {
	t.Helper()
	mig := migrationAt(t, changes, app, pos)
	if !sameKeySet(mig.Dependencies, deps) {
		t.Fatalf("Migration dependencies mismatch for %s.%s (expected %v, got %v):\n%s",
			app, mig.Name, deps, mig.Dependencies, reprChanges(changes, true))
	}
}

// operationAt returns the operation at the given position, asserting its type.
// It replaces Django's assertOperationAttributes, whose keyword arguments
// become plain field accesses on the returned typed operation.
func operationAt[T m.Operation](t *testing.T, changes map[string][]*m.Migration, app string, pos, opPos int) T {
	t.Helper()
	var zero T
	mig := migrationAt(t, changes, app, pos)
	if len(mig.Operations) < opPos+1 {
		t.Fatalf("No operation at index %d for %s.%s\n%s", opPos, app, mig.Name, reprChanges(changes, false))
	}
	op, ok := any(mig.Operations[opPos]).(T)
	if !ok {
		t.Fatalf("Operation type mismatch for %s.%s op #%d (expected %T, got %s):\n%s",
			app, mig.Name, opPos, zero, m.OpName(mig.Operations[opPos]), reprChanges(changes, false))
	}
	return op
}

func assertEqual[T comparable](t *testing.T, what string, got, want T) {
	t.Helper()
	if got != want {
		t.Fatalf("%s: got %v, want %v", what, got, want)
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

func sameKeySet(a, b []m.Key) bool {
	if len(a) != len(b) {
		return false
	}
	sa := map[m.Key]bool{}
	for _, k := range a {
		sa[k] = true
	}
	for _, k := range b {
		if !sa[k] {
			return false
		}
	}
	return len(sa) == len(b)
}

// assertUniqueTogether compares an AlterUniqueTogether value as a set of
// tuples, the way Django stores unique_together.
func assertUniqueTogether(t *testing.T, got [][]string, want [][]string) {
	t.Helper()
	norm := func(v [][]string) []string {
		out := make([]string, 0, len(v))
		for _, x := range v {
			out = append(out, strings.Join(x, ","))
		}
		sort.Strings(out)
		return out
	}
	g, w := norm(got), norm(want)
	if !equalStrings(g, w) {
		t.Fatalf("unique_together mismatch: got %v, want %v", got, want)
	}
}

func deps(keys ...m.Key) []m.Key { return keys }

func key(app, name string) m.Key { return m.Key{App: app, Name: name} }
