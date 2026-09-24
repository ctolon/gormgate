package autodetector

// Regression tests for the foreign-key target spelling. ForeignKey.To may be
// written as a bare "ModelName" for a model of the same app, while the state
// built from the gorm models always spells it "app.ModelName". The two have
// to compare equal, or an unchanged field looks altered and a renamed field
// is never offered as a rename.

import (
	"testing"

	"github.com/ctolon/gormgate/internal/questioner"
	m "github.com/ctolon/gormgate/migrations"
)

// authorModel is the target of the foreign keys below.
func authorModel() *m.ModelState {
	return model("testapp", "Author", field("id", autoField()), field("name", charField(200)))
}

// TestRelativeForeignKeyIsNotAltered checks that a migration spelling the
// target "Author" and a model state spelling it "testapp.Author" produce no
// operation at all.
func TestRelativeForeignKeyIsNotAltered(t *testing.T) {
	before := []*m.ModelState{
		authorModel(),
		model("testapp", "Book", field("id", autoField()), field("author_id", fk("Author"))),
	}
	after := []*m.ModelState{
		authorModel(),
		model("testapp", "Book", field("id", autoField()), field("author_id", fk("testapp.Author"))),
	}
	changes := getChanges(t, before, after, nil)
	if len(changes) != 0 {
		t.Fatalf("expected no changes, got %v", changes)
	}
}

// TestRelativeForeignKeyRenameIsDetected checks that renaming such a field
// is offered as a rename rather than a remove plus an add, which would drop
// the column and its data.
func TestRelativeForeignKeyRenameIsDetected(t *testing.T) {
	before := []*m.ModelState{
		authorModel(),
		model("testapp", "Book", field("id", autoField()), field("author_id", fk("Author"))),
	}
	after := []*m.ModelState{
		authorModel(),
		model("testapp", "Book", field("id", autoField()), field("writer_id", fk("testapp.Author"))),
	}
	q := &questioner.Base{Defaults: questioner.Defaults{Rename: true}}
	changes := getChanges(t, before, after, q)
	assertNumberMigrations(t, changes, "testapp", 1)
	assertOperationTypes(t, changes, "testapp", 0, "RenameField")
}

// TestRelativeForeignKeyInStateIsQualified checks the canonical form the
// state stores, which is what makes the comparisons above work.
func TestRelativeForeignKeyInStateIsQualified(t *testing.T) {
	st := projectState(model("testapp", "Book", field("author_id", fk("Author"))))
	got := st.Models[m.ModelKey{App: "testapp", Model: "book"}].Fields[0].Field.ForeignKey.To
	assertEqual(t, "ForeignKey.To", got, "testapp.Author")
}

// TestParseNumberRejectsOverlongNumbers checks that a leading run of digits
// too long for an int is not reported as a migration number. strconv.Atoi
// saturates at MaxInt, and ArrangeForGraph would then name the next
// migration with a negative number.
func TestParseNumberRejectsOverlongNumbers(t *testing.T) {
	for _, name := range []string{
		"1234567890123456789012_initial",
		"0001_squashed_99999999999999999999",
	} {
		if n, ok := ParseNumber(name); ok {
			t.Errorf("ParseNumber(%q) = %d, true; want ok=false", name, n)
		}
	}
	if n, ok := ParseNumber("0007_things"); !ok || n != 7 {
		t.Errorf("ParseNumber(\"0007_things\") = %d, %v; want 7, true", n, ok)
	}
	if n, ok := ParseNumber("0001_squashed_0004"); !ok || n != 4 {
		t.Errorf("ParseNumber(\"0001_squashed_0004\") = %d, %v; want 4, true", n, ok)
	}
}
