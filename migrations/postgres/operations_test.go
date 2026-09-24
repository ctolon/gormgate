package postgres

import (
	"strings"
	"testing"

	"github.com/ctolon/gormgate/backends/cockroachdb"
	"github.com/ctolon/gormgate/backends/gaussdb"
	"github.com/ctolon/gormgate/backends/postgresql"
	m "github.com/ctolon/gormgate/migrations"
)

// The schema editors of the PostgreSQL family supply the steps these
// operations need. CockroachDB's and openGauss's editors embed
// PostgreSQL's, so they inherit them.
var (
	_ Editor = (*postgresql.Editor)(nil)
	_ Editor = (*cockroachdb.Editor)(nil)
	_ Editor = (*gaussdb.Editor)(nil)
)

// weightIndex is the index the index operations work on.
func weightIndex() m.Index {
	return m.Index{Name: "pony_weight_idx", Fields: []m.IndexField{
		{Column: "weight", Sort: m.SortDesc}, {Column: "id"},
	}}
}

func check() *m.CheckConstraint {
	return &m.CheckConstraint{Name: "pony_weight_gt0", Check: "weight > 0"}
}

// TestDescribe checks the text makemigrations, migrate --plan and
// sqlmigrate print, which must be Django's word for word.
//
// django: tests/postgres_tests/test_operations.py
func TestDescribe(t *testing.T) {
	for _, tc := range []struct {
		op   m.Operation
		want string
	}{
		{&CreateExtension{Name: "tablefunc"}, "Creates extension tablefunc"},
		{HStoreExtension(), "Creates extension hstore"},
		{BloomExtension(), "Creates extension bloom"},
		{BtreeGinExtension(), "Creates extension btree_gin"},
		{BtreeGistExtension(), "Creates extension btree_gist"},
		{CITextExtension(), "Creates extension citext"},
		{CryptoExtension(), "Creates extension pgcrypto"},
		{TrigramExtension(), "Creates extension pg_trgm"},
		{UnaccentExtension(), "Creates extension unaccent"},
		{&CreateCollation{Name: "C_test", Locale: "C"}, "Create collation C_test"},
		{&RemoveCollation{Name: "C_test", Locale: "C"}, "Remove collation C_test"},
		{&AddIndexConcurrently{ModelName: "Pony", Index: weightIndex()},
			"Concurrently create index pony_weight_idx on field(s) -weight, id of model Pony"},
		{&RemoveIndexConcurrently{ModelName: "Pony", Name: "pony_weight_idx"},
			"Concurrently remove index pony_weight_idx from Pony"},
		{&AddConstraintNotValid{ModelName: "Pony", Constraint: check()},
			"Create not valid constraint pony_weight_gt0 on model Pony"},
		{&ValidateConstraint{ModelName: "Pony", Name: "pony_weight_gt0"},
			"Validate constraint pony_weight_gt0 on model Pony"},
	} {
		if got := tc.op.Describe(); got != tc.want {
			t.Errorf("%s.Describe() = %q, want %q", m.OpName(tc.op), got, tc.want)
		}
	}
}

// TestCategory checks the symbol makemigrations prints in front of each
// operation.
func TestCategory(t *testing.T) {
	for _, tc := range []struct {
		op   m.Operation
		want m.Category
	}{
		{&CreateExtension{Name: "hstore"}, m.CategoryAddition},
		{&CreateCollation{Name: "c"}, m.CategoryAddition},
		{&RemoveCollation{Name: "c"}, m.CategoryRemoval},
		{&AddIndexConcurrently{ModelName: "Pony", Index: weightIndex()}, m.CategoryAddition},
		{&RemoveIndexConcurrently{ModelName: "Pony", Name: "i"}, m.CategoryRemoval},
		{&AddConstraintNotValid{ModelName: "Pony", Constraint: check()}, m.CategoryAddition},
		{&ValidateConstraint{ModelName: "Pony", Name: "c"}, m.CategoryAlteration},
	} {
		if got := tc.op.Category(); got != tc.want {
			t.Errorf("%s.Category() = %q, want %q", m.OpName(tc.op), got, tc.want)
		}
	}
}

// TestMigrationNameFragment checks the fragment each operation contributes
// to a suggested migration name.
func TestMigrationNameFragment(t *testing.T) {
	for _, tc := range []struct {
		op   m.Operation
		want string
	}{
		{&CreateExtension{Name: "tablefunc"}, "create_extension_tablefunc"},
		{HStoreExtension(), "create_extension_hstore"},
		{&CreateCollation{Name: "C_test", Locale: "C"}, "create_collation_c_test"},
		{&RemoveCollation{Name: "C_test", Locale: "C"}, "remove_collation_c_test"},
		{&AddIndexConcurrently{ModelName: "Pony", Index: weightIndex()}, "pony_pony_weight_idx"},
		{&RemoveIndexConcurrently{ModelName: "Pony", Name: "Pony_Weight_Idx"}, "remove_pony_pony_weight_idx"},
		{&AddConstraintNotValid{ModelName: "Pony", Constraint: check()}, "pony_pony_weight_gt0_not_valid"},
		{&ValidateConstraint{ModelName: "Pony", Name: "Pony_Weight_Gt0"}, "pony_validate_pony_weight_gt0"},
	} {
		if got := tc.op.MigrationNameFragment(); got != tc.want {
			t.Errorf("%s.MigrationNameFragment() = %q, want %q", m.OpName(tc.op), got, tc.want)
		}
	}
}

// TestSuggestName checks that a migration holding these operations gets the
// name Django's suggest_name builds out of the fragments above.
func TestSuggestName(t *testing.T) {
	mig := &m.Migration{App: "a", Operations: []m.Operation{HStoreExtension()}}
	if got, want := mig.SuggestName(), "create_extension_hstore"; got != want {
		t.Errorf("SuggestName() = %q, want %q", got, want)
	}
}

// TestAtomic checks that the concurrent operations refuse a transaction of
// their own, which is Django's atomic = False.
func TestAtomic(t *testing.T) {
	for _, op := range []m.Operation{
		&AddIndexConcurrently{ModelName: "Pony", Index: weightIndex()},
		&RemoveIndexConcurrently{ModelName: "Pony", Name: "i"},
	} {
		a := op.Atomic()
		if a == nil || *a {
			t.Errorf("%s.Atomic() = %v, want a non-nil false", m.OpName(op), a)
		}
	}
}

// TestReduce checks the optimizer's view of these operations.
func TestReduce(t *testing.T) {
	ix := weightIndex()
	for _, tc := range []struct {
		name string
		op   m.Operation
		with m.Operation
		want []string // the descriptions of the replacement, nil for no replacement
		kind m.ReduceKind
	}{
		{"collation round trip cancels out",
			&CreateCollation{Name: "C_test", Locale: "C"},
			&RemoveCollation{Name: "C_test", Locale: "C"},
			[]string{}, m.ReduceReplace},
		{"a different collation does not",
			&CreateCollation{Name: "C_test", Locale: "C"},
			&RemoveCollation{Name: "other", Locale: "C"},
			nil, m.ReduceBlock},
		{"concurrent index round trip cancels out",
			&AddIndexConcurrently{ModelName: "Pony", Index: ix},
			&RemoveIndexConcurrently{ModelName: "Pony", Name: ix.Name},
			[]string{}, m.ReduceReplace},
		{"an ordinary RemoveIndex cancels it too",
			&AddIndexConcurrently{ModelName: "Pony", Index: ix},
			&m.RemoveIndex{ModelName: "Pony", Name: ix.Name},
			[]string{}, m.ReduceReplace},
		{"a rename is folded into the creation",
			&AddIndexConcurrently{ModelName: "Pony", Index: ix},
			&m.RenameIndex{ModelName: "Pony", OldName: ix.Name, NewName: "pony_w_idx"},
			[]string{"Concurrently create index pony_w_idx on field(s) -weight, id of model Pony"},
			m.ReduceReplace},
		{"a not valid constraint is cancelled by its removal",
			&AddConstraintNotValid{ModelName: "Pony", Constraint: check()},
			&m.RemoveConstraint{ModelName: "pony", Name: "pony_weight_gt0"},
			[]string{}, m.ReduceReplace},
		{"a validation blocks",
			&ValidateConstraint{ModelName: "Pony", Name: "pony_weight_gt0"},
			&m.RemoveConstraint{ModelName: "pony", Name: "pony_weight_gt0"},
			nil, m.ReduceBlock},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ops, kind := tc.op.Reduce(tc.with, "app")
			if kind != tc.kind {
				t.Fatalf("kind = %v, want %v", kind, tc.kind)
			}
			if tc.want == nil {
				if ops != nil {
					t.Fatalf("operations = %v, want none", ops)
				}
				return
			}
			if len(ops) != len(tc.want) {
				t.Fatalf("got %d operations, want %d", len(ops), len(tc.want))
			}
			for i, w := range tc.want {
				if got := ops[i].Describe(); got != w {
					t.Errorf("operations[%d].Describe() = %q, want %q", i, got, w)
				}
			}
		})
	}
}

// TestStateForwards checks the state each operation leaves behind: none for
// the extension and collation operations, and the same as their core
// counterparts for the index and constraint ones.
func TestStateForwards(t *testing.T) {
	ix := weightIndex()

	t.Run("extensions and collations change nothing", func(t *testing.T) {
		for _, op := range []m.Operation{
			HStoreExtension(),
			&CreateCollation{Name: "C_test", Locale: "C"},
			&RemoveCollation{Name: "C_test", Locale: "C"},
			&ValidateConstraint{ModelName: "Pony", Name: "c"},
		} {
			s := stateWithPony("app")
			if err := op.StateForwards("app", s); err != nil {
				t.Fatalf("%s: %v", m.OpName(op), err)
			}
			if got := s.Models[m.ModelKey{App: "app", Model: "pony"}]; len(got.Options.Indexes) != 0 || len(got.Options.Constraints) != 0 {
				t.Errorf("%s changed the model state", m.OpName(op))
			}
		}
	})

	t.Run("AddIndexConcurrently adds the index", func(t *testing.T) {
		s := stateWithPony("app")
		op := &AddIndexConcurrently{ModelName: "Pony", Index: ix}
		if err := op.StateForwards("app", s); err != nil {
			t.Fatal(err)
		}
		ms := s.Models[m.ModelKey{App: "app", Model: "pony"}]
		if len(ms.Options.Indexes) != 1 || ms.Options.Indexes[0].Name != ix.Name {
			t.Fatalf("indexes = %v", ms.Options.Indexes)
		}
	})

	t.Run("AddIndexConcurrently needs a named index", func(t *testing.T) {
		s := stateWithPony("app")
		op := &AddIndexConcurrently{ModelName: "Pony"}
		err := op.StateForwards("app", s)
		if err == nil || !strings.Contains(err.Error(), "require a name argument") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("RemoveIndexConcurrently removes the index", func(t *testing.T) {
		s := stateWithPony("app", ix)
		op := &RemoveIndexConcurrently{ModelName: "Pony", Name: ix.Name}
		if err := op.StateForwards("app", s); err != nil {
			t.Fatal(err)
		}
		if ms := s.Models[m.ModelKey{App: "app", Model: "pony"}]; len(ms.Options.Indexes) != 0 {
			t.Fatalf("indexes = %v", ms.Options.Indexes)
		}
	})

	t.Run("AddConstraintNotValid adds the constraint", func(t *testing.T) {
		s := stateWithPony("app")
		op := &AddConstraintNotValid{ModelName: "Pony", Constraint: check()}
		if err := op.StateForwards("app", s); err != nil {
			t.Fatal(err)
		}
		ms := s.Models[m.ModelKey{App: "app", Model: "pony"}]
		if len(ms.Options.Constraints) != 1 {
			t.Fatalf("constraints = %v", ms.Options.Constraints)
		}
	})

	t.Run("AddConstraintNotValid refuses anything but a check constraint", func(t *testing.T) {
		s := stateWithPony("app")
		op := &AddConstraintNotValid{ModelName: "Pony",
			Constraint: &m.UniqueConstraint{Name: "u", Fields: []string{"weight"}}}
		err := op.StateForwards("app", s)
		if err == nil || err.Error() != "AddConstraintNotValid.Constraint must be a check constraint" {
			t.Fatalf("err = %v", err)
		}
	})
}

// TestCoreAddIndexFoldsIntoConcurrentRemoval pins the symmetry Django gets
// from RemoveIndexConcurrently subclassing RemoveIndex: an AddIndex
// followed by a concurrent removal of the same index cancels out, exactly
// as it does with a core RemoveIndex.
func TestCoreAddIndexFoldsIntoConcurrentRemoval(t *testing.T) {
	add := &m.AddIndex{ModelName: "pony", Index: m.Index{Name: "idx_pony_name", Fields: m.Columns("name")}}

	for _, tc := range []struct {
		name   string
		remove m.Operation
	}{
		{"core removal", &m.RemoveIndex{ModelName: "pony", Name: "idx_pony_name"}},
		{"concurrent removal", &RemoveIndexConcurrently{ModelName: "pony", Name: "idx_pony_name"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ops, kind := add.Reduce(tc.remove, "app")
			if kind != m.ReduceReplace || len(ops) != 0 {
				t.Errorf("Reduce = %v, %v; want an empty replacement", ops, kind)
			}
		})
	}

	// A different index must not cancel.
	other := &RemoveIndexConcurrently{ModelName: "pony", Name: "idx_pony_other"}
	if ops, kind := add.Reduce(other, "app"); kind == m.ReduceReplace && len(ops) == 0 {
		t.Error("an unrelated index removal cancelled the AddIndex")
	}
}
