package postgresql

import (
	"testing"

	"github.com/ctolon/gormgate/backends/base"
	m "github.com/ctolon/gormgate/migrations"
)

// collectingEditor is a PostgreSQL editor in collect mode, which renders
// the DDL without a server behind it.
func collectingEditor(t *testing.T) *Editor {
	t.Helper()
	return NewEditor(&base.Conn{Backend: Backend}, true, false)
}

func ponyModel() *m.Model {
	s := m.NewProjectState()
	s.AddModel(&m.ModelState{App: "app", Name: "Pony", Table: "app_pony", Fields: m.Fields{
		{Name: "id", Field: m.Field{Type: m.Int, Size: 64, PrimaryKey: true, AutoIncrement: true}},
		{Name: "weight", Field: m.Field{Type: m.Int, Size: 64}},
	}})
	return s.MustApps().MustModel("app", "Pony")
}

func weightIndex() m.Index {
	return m.Index{Name: "idx_pony_weight", Fields: m.Columns("weight")}
}

// TestConcurrentIndexSQL checks the statements the concurrent steps emit,
// and that the ordinary ones are unchanged: the SQL of every operation that
// existed before them is frozen.
//
// django: postgresql/schema.py sql_create_index_concurrently, sql_delete_index_concurrently
func TestConcurrentIndexSQL(t *testing.T) {
	model, ix := ponyModel(), weightIndex()
	for _, tc := range []struct {
		name string
		run  func(*Editor) error
		want string
	}{
		{"add", func(e *Editor) error { return e.AddIndexConcurrently(model, ix) },
			`CREATE INDEX CONCURRENTLY "idx_pony_weight" ON "app_pony" ("weight");`},
		{"remove", func(e *Editor) error { return e.RemoveIndexConcurrently(model, ix) },
			`DROP INDEX CONCURRENTLY IF EXISTS "idx_pony_weight";`},
		{"add, not concurrently", func(e *Editor) error { return e.AddIndex(model, ix) },
			`CREATE INDEX "idx_pony_weight" ON "app_pony" ("weight");`},
		{"remove, not concurrently", func(e *Editor) error { return e.RemoveIndex(model, ix) },
			`DROP INDEX IF EXISTS "idx_pony_weight";`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := collectingEditor(t)
			if err := tc.run(e); err != nil {
				t.Fatal(err)
			}
			got := e.CollectedSQL()
			if len(got) != 1 || got[0] != tc.want {
				t.Fatalf("collected %q, want [%q]", got, tc.want)
			}
		})
	}
}

// TestConcurrentFlagIsRestored checks that an ordinary index built right
// after a concurrent one does not carry the keyword over.
func TestConcurrentFlagIsRestored(t *testing.T) {
	model, ix := ponyModel(), weightIndex()
	e := collectingEditor(t)
	if err := e.AddIndexConcurrently(model, ix); err != nil {
		t.Fatal(err)
	}
	if err := e.AddIndex(model, ix); err != nil {
		t.Fatal(err)
	}
	got := e.CollectedSQL()
	want := []string{
		`CREATE INDEX CONCURRENTLY "idx_pony_weight" ON "app_pony" ("weight");`,
		`CREATE INDEX "idx_pony_weight" ON "app_pony" ("weight");`,
	}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("collected %q, want %q", got, want)
	}
}

// TestNotValidAndValidateSQL checks the two constraint statements.
//
// django: contrib/postgres/operations.py AddConstraintNotValid, ValidateConstraint
func TestNotValidAndValidateSQL(t *testing.T) {
	model := ponyModel()
	e := collectingEditor(t)
	c := &m.CheckConstraint{Name: "chk_pony_weight", Check: `"weight" > 0`}
	if err := e.AddConstraintNotValid(model, c); err != nil {
		t.Fatal(err)
	}
	if err := e.ValidateConstraint(model, c.Name); err != nil {
		t.Fatal(err)
	}
	want := []string{
		`ALTER TABLE "app_pony" ADD CONSTRAINT "chk_pony_weight" CHECK ("weight" > 0) NOT VALID;`,
		`ALTER TABLE "app_pony" VALIDATE CONSTRAINT "chk_pony_weight";`,
	}
	got := e.CollectedSQL()
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("collected %q, want %q", got, want)
	}
}
