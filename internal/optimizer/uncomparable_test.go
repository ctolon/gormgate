package optimizer

import (
	"testing"

	m "github.com/ctolon/gormgate/migrations"
)

// uncomparableOp is an operation of the kind a third party can write: a
// struct with value receivers and a slice field, so the value inside the
// Operation interface cannot be compared with ==.
type uncomparableOp struct {
	m.BaseOperation
	Names []string
}

func (o uncomparableOp) StateForwards(string, *m.ProjectState) error { return nil }
func (o uncomparableOp) DatabaseForwards(string, m.SchemaEditor, *m.ProjectState, *m.ProjectState) error {
	return nil
}
func (o uncomparableOp) DatabaseBackwards(string, m.SchemaEditor, *m.ProjectState, *m.ProjectState) error {
	return nil
}
func (o uncomparableOp) Describe() string                            { return "Uncomparable" }
func (o uncomparableOp) Category() m.Category                        { return m.CategoryMixed }
func (o uncomparableOp) ReferencesField(string, string, string) bool { return true }
func (o uncomparableOp) Reduce(other m.Operation, app string) ([]m.Operation, m.ReduceKind) {
	return nil, m.ReduceBlock
}

var _ m.Operation = uncomparableOp{}

// TestOptimizeWithUncomparableOperations checks that the fixed-point loop
// does not compare the operations themselves: an operation whose value
// cannot be compared with == would panic with "comparing uncomparable type".
func TestOptimizeWithUncomparableOperations(t *testing.T) {
	in := []m.Operation{
		uncomparableOp{Names: []string{"a"}},
		&m.CreateModel{Name: "Thing", Table: "app_thing"},
		uncomparableOp{Names: []string{"b"}},
	}
	got := Optimize(in, "app")
	if len(got) != len(in) {
		t.Fatalf("Optimize returned %d operations, want %d", len(got), len(in))
	}
}

// TestOptimizeReducesAroundUncomparableOperations checks that a reduction
// still happens when an uncomparable operation sits in the list, so the test
// above is not merely exercising a no-op pass.
func TestOptimizeReducesAroundUncomparableOperations(t *testing.T) {
	in := []m.Operation{
		&m.CreateModel{Name: "Thing", Table: "app_thing"},
		&m.AddField{ModelName: "Thing", Name: "n", Field: m.Field{Type: m.Int, Size: 64}},
		uncomparableOp{Names: []string{"a"}},
	}
	got := Optimize(in, "app")
	if len(got) != 2 {
		t.Fatalf("Optimize returned %d operations, want 2: %v", len(got), got)
	}
	cm, ok := got[0].(*m.CreateModel)
	if !ok || len(cm.Fields) != 1 {
		t.Fatalf("expected the AddField folded into the CreateModel, got %v", got[0])
	}
}
