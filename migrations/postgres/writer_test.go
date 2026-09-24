package postgres

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/ctolon/gormgate/internal/writer"
	m "github.com/ctolon/gormgate/migrations"
)

// spaces collapses the alignment gofmt puts into a composite literal.
var spaces = regexp.MustCompile(`[ \t]+`)

// TestWriterRoundTrip renders a migration holding one of every operation of
// this package into the Go source of a migration file, checks that each
// operation is written with its package qualifier and its arguments, and
// compiles the result. A migration a user hand-writes has to survive
// squashmigrations, which rewrites it through this writer.
//
// django: tests/migrations/test_writer.py WriterTests
func TestWriterRoundTrip(t *testing.T) {
	mig := &m.Migration{
		App:    "blog",
		Name:   "0002_postgres",
		Atomic: m.Ptr(false),
		Operations: []m.Operation{
			&CreateExtension{Name: "tablefunc", Hints: map[string]any{"target_db": "replica"}},
			HStoreExtension(),
			&CreateCollation{Name: "case_insensitive", Locale: "und-u-ks-level2",
				Provider: "icu", Deterministic: m.Ptr(false)},
			&RemoveCollation{Name: "C_test", Locale: "C"},
			&AddIndexConcurrently{ModelName: "Pony", Index: weightIndex()},
			&RemoveIndexConcurrently{ModelName: "Pony", Name: "pony_old_idx"},
			&AddConstraintNotValid{ModelName: "Pony", Constraint: check()},
			&ValidateConstraint{ModelName: "Pony", Name: "pony_weight_gt0"},
		},
	}
	w := &writer.Writer{Migration: mig, PackageName: "migrations",
		PackagePath: "example.com/blog/migrations"}
	out, err := w.AsString()
	if err != nil {
		t.Fatalf("rendering the migration: %v", err)
	}
	// Field alignment depends on the longest name in each literal, so the
	// expectations below are matched against the source with runs of
	// spaces collapsed.
	src := string(out)
	flat := spaces.ReplaceAllString(src, " ")

	for _, want := range []string{
		`"github.com/ctolon/gormgate/migrations/postgres"`,
		`&postgres.CreateExtension{`,
		`Name: "tablefunc",`,
		`Hints: map[string]any{`,
		`"target_db": "replica",`,
		`Name: "hstore",`,
		`&postgres.CreateCollation{`,
		`Locale: "und-u-ks-level2",`,
		`Provider: "icu",`,
		`Deterministic: m.Ptr(false),`,
		`&postgres.RemoveCollation{`,
		`&postgres.AddIndexConcurrently{`,
		`Name: "pony_weight_idx",`,
		`Sort: "DESC",`,
		`&postgres.RemoveIndexConcurrently{`,
		`&postgres.AddConstraintNotValid{`,
		`Constraint: &m.CheckConstraint{`,
		`&postgres.ValidateConstraint{`,
		`Atomic: m.Ptr(false),`,
	} {
		if !strings.Contains(flat, want) {
			t.Errorf("the generated migration does not contain %q:\n%s", want, src)
		}
	}
	// The extension shortcuts are written as the CreateExtension they are,
	// which is the same operation read back.
	if strings.Contains(src, "postgres.HStoreExtension") {
		t.Errorf("an extension shortcut was written as a function call:\n%s", src)
	}
	goBuildMigration(t, "0002_postgres.go", src)
}

// goBuildMigration compiles src as a package in a throwaway module that
// points at this repository, proving the generated migration is valid Go
// that links against the operations of this package.
//
// django: tests/migrations/test_writer.py safe_exec
func goBuildMigration(t *testing.T, fileName, src string) {
	t.Helper()
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("go toolchain not available: %v", err)
	}
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	gomod := fmt.Sprintf("module buildtest\n\ngo 1.26\n\nrequire github.com/ctolon/gormgate v0.0.0\n\nreplace github.com/ctolon/gormgate => %s\n", repo)
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(gomod), 0o644); err != nil {
		t.Fatal(err)
	}
	sum, err := os.ReadFile(filepath.Join(repo, "go.sum"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.sum"), sum, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, fileName), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(goBin, "build", "./...")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod", "GOPROXY=off")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build of generated migration failed: %v\n%s\n--- source ---\n%s", err, out, src)
	}
}
