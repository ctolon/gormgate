package migrations

import (
	"errors"
	"strings"
	"testing"
)

// mustPanic runs fn and returns the message it panicked with.
func mustPanic(t *testing.T, fn func()) string {
	t.Helper()
	var got any
	func() {
		defer func() { got = recover() }()
		fn()
	}()
	if got == nil {
		t.Fatal("expected a panic")
	}
	s, ok := got.(string)
	if !ok {
		t.Fatalf("expected a string panic value, got %T", got)
	}
	return s
}

// A RunSQL whose SQL is none of the accepted shapes no longer needs a test:
// SQL is a sealed interface, so anything else fails to compile.

// TestRegisterRejectsMissingSQL checks that a RunSQL with no SQL at all is
// refused; Django's RunSQL takes sql as a required argument.
func TestRegisterRejectsMissingSQL(t *testing.T) {
	msg := mustPanic(t, func() {
		Register(&Migration{App: "validate", Name: "0002_no_sql", Operations: []Operation{&RunSQL{}}})
	})
	if !strings.Contains(msg, "RunSQL requires SQL") {
		t.Fatalf("unexpected panic message: %s", msg)
	}
}

// TestRegisterChecksNestedOperations checks that the operations a
// SeparateDatabaseAndState holds are validated too.
func TestRegisterChecksNestedOperations(t *testing.T) {
	msg := mustPanic(t, func() {
		Register(&Migration{App: "validate", Name: "0003_nested", Operations: []Operation{
			&SeparateDatabaseAndState{DatabaseOperations: []Operation{&RunSQL{}}},
		}})
	})
	if !strings.Contains(msg, "RunSQL requires SQL") {
		t.Fatalf("unexpected panic message: %s", msg)
	}
}

// TestRegisterRejectsWronglyShapedDefault checks that a function default
// that takes arguments, or returns none, is refused at init rather than
// written into DDL with the wrong shape.
func TestRegisterRejectsWronglyShapedDefault(t *testing.T) {
	for _, tc := range []struct {
		name string
		def  any
	}{
		{"takes an argument", func(int) int { return 0 }},
		{"returns nothing", func() {}},
		{"returns two values", func() (int, error) { return 0, nil }},
		{"is variadic", func(...int) int { return 0 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msg := mustPanic(t, func() {
				Register(&Migration{App: "validate", Name: "0004_" + strings.ReplaceAll(tc.name, " ", "_"), Operations: []Operation{
					&AddField{ModelName: "Thing", Name: "n", Field: Field{Type: Int, Default: tc.def}},
				}})
			})
			if !strings.Contains(msg, "Default must be a literal value or a function with no arguments and a single result") {
				t.Fatalf("unexpected panic message: %s", msg)
			}
		})
	}
}

// TestRegisterAcceptsWellShapedOperations checks the shapes that are valid,
// so the validation cannot simply reject everything.
func TestRegisterAcceptsWellShapedOperations(t *testing.T) {
	Register(&Migration{App: "validate", Name: "0005_ok", Operations: []Operation{
		&RunSQL{SQL: Script("SELECT 1"), ReverseSQL: Statements{"SELECT 2"}},
		&RunSQL{SQL: Parameterized{{SQL: "SELECT ?", Params: []any{1}}}},
		&AddField{ModelName: "Thing", Name: "n", Field: Field{Type: Int, Default: 3}},
		&AddField{ModelName: "Thing", Name: "t", Field: Field{Type: Time, Default: func() int { return 1 }}},
		&CreateModel{Name: "Thing", Fields: Fields{{Name: "n", Field: Field{Type: Int, Default: 1}}}},
	}})
}

// TestRunSQLReversible pins what counts as a reverse: one that was given
// at all, however empty.
func TestRunSQLReversible(t *testing.T) {
	if (&RunSQL{SQL: Script("x")}).Reversible() {
		t.Fatal("a missing ReverseSQL must not report reversible")
	}
	if !(&RunSQL{SQL: Script("x"), ReverseSQL: NoSQL}).Reversible() {
		t.Fatal("NoSQL must report reversible")
	}
	if !(&RunSQL{SQL: Script("x"), ReverseSQL: Statements{}}).Reversible() {
		t.Fatal("empty Statements must report reversible")
	}
}

// TestValueErrorUnwrapsItsCause checks that the operations that turn a
// lookup failure into a *ValueError keep the error they were given, without
// changing what is printed.
func TestValueErrorUnwrapsItsCause(t *testing.T) {
	cause := errors.New("no index named ix on model Thing")
	err := valueError(cause)
	if err.Error() != cause.Error() {
		t.Fatalf("message changed: %q", err.Error())
	}
	if !errors.Is(err, cause) {
		t.Fatal("cause is not reachable with errors.Is")
	}
}

// TestColumns checks the helper that spells a plain multi-column index.
func TestColumns(t *testing.T) {
	got := Columns("title", "slug")
	want := []IndexField{{Column: "title"}, {Column: "slug"}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("Columns = %v, want %v", got, want)
	}
	if len(Columns()) != 0 {
		t.Fatalf("Columns() = %v, want empty", Columns())
	}
}
