package migrations

import "testing"

// customOp is an operation written the way one outside this package is: it
// embeds BaseOperation and implements only what differs.
type customOp struct {
	BaseOperation
	Label string
}

func (o *customOp) StateForwards(string, *ProjectState) error { return nil }
func (o *customOp) DatabaseForwards(string, SchemaEditor, *ProjectState, *ProjectState) error {
	return nil
}
func (o *customOp) DatabaseBackwards(string, SchemaEditor, *ProjectState, *ProjectState) error {
	return nil
}
func (o *customOp) Describe() string                            { return "Custom" }
func (o *customOp) Category() Category                          { return CategoryMixed }
func (o *customOp) ReferencesField(string, string, string) bool { return true }
func (o *customOp) Reduce(other Operation, app string) ([]Operation, ReduceKind) {
	return baseReduce(o, other)
}

var _ Operation = (*customOp)(nil)

// TestBaseOperationSuppliesTheDefaults checks that embedding BaseOperation
// is enough to satisfy Operation, and that its defaults are the ones the
// operations of this package rely on.
func TestBaseOperationSuppliesTheDefaults(t *testing.T) {
	var op Operation = &customOp{Label: "x"}
	if op.MigrationNameFragment() != "" {
		t.Error("MigrationNameFragment default changed")
	}
	if !op.Reversible() || !op.ReducesToSQL() || op.Elidable() {
		t.Error("Reversible/ReducesToSQL/Elidable defaults changed")
	}
	if a := op.Atomic(); a == nil || *a {
		t.Error("Atomic default must be a non-nil false")
	}
	if !op.ReferencesModel("anything", "app") {
		t.Error("ReferencesModel default changed")
	}
}

// TestBaseOperationIsNotDeconstructed checks that the embedded field does
// not appear in the deconstruction a migration file is written from, which
// is what keeps generated files unchanged.
func TestBaseOperationIsNotDeconstructed(t *testing.T) {
	kv := deconstructStruct(&customOp{Label: "x"})
	if len(kv) != 1 || kv[0].Key != "Label" {
		t.Fatalf("deconstructStruct = %v, want only Label", kv)
	}
}
