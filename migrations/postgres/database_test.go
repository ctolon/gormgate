package postgres

import (
	"errors"
	"slices"
	"testing"

	m "github.com/ctolon/gormgate/migrations"
)

// allOperations is one of each operation, ready to run against the fake
// editor: enough state exists for every one of them.
func allOperations() []m.Operation {
	return []m.Operation{
		HStoreExtension(),
		&CreateCollation{Name: "C_test", Locale: "C"},
		&RemoveCollation{Name: "C_test", Locale: "C"},
		&AddIndexConcurrently{ModelName: "Pony", Index: weightIndex()},
		&RemoveIndexConcurrently{ModelName: "Pony", Name: weightIndex().Name},
		&AddConstraintNotValid{ModelName: "Pony", Constraint: check()},
		&ValidateConstraint{ModelName: "Pony", Name: check().Name},
	}
}

// TestOutsideTheFamilyIsASilentNoOp checks the guard these operations
// apply: on a schema editor that does not implement the PostgreSQL steps,
// every one of them returns without running any SQL and without an error.
// The fake's embedded migrations.SchemaEditor is nil, so an operation that
// reached past the guard would panic rather than pass unnoticed.
func TestOutsideTheFamilyIsASilentNoOp(t *testing.T) {
	for _, vendor := range []string{"mysql", "sqlite", "mssql", "oracle", "clickhouse"} {
		t.Run(vendor, func(t *testing.T) {
			for _, op := range allOperations() {
				ed := newPlainEditor(vendor)
				// A transaction is open: the concurrent operations must
				// still keep quiet rather than refuse it.
				ed.conn.inBlock = true
				from := stateWithPony("app", weightIndex())
				to := stateWithPony("app", weightIndex())
				if err := op.DatabaseForwards("app", ed, from, to); err != nil {
					t.Errorf("%s forwards: %v", m.OpName(op), err)
				}
				if err := op.DatabaseBackwards("app", ed, from, to); err != nil {
					t.Errorf("%s backwards: %v", m.OpName(op), err)
				}
				if len(ed.sqls) > 0 {
					t.Errorf("%s ran %v on %s", m.OpName(op), ed.sqls, vendor)
				}
			}
		})
	}
}

// TestVendorsThatHaveTheFeature checks which of the three vendors of the
// PostgreSQL family each operation runs on. The sets were verified against
// PostgreSQL 14 and 18, CockroachDB 25.2 and openGauss; see the vendor sets
// in operations.go for what each server refused.
func TestVendorsThatHaveTheFeature(t *testing.T) {
	for _, tc := range []struct {
		op      m.Operation
		vendors []string
	}{
		{HStoreExtension(), []string{vendorPostgreSQL}},
		{&CreateCollation{Name: "C_test", Locale: "C"}, []string{vendorPostgreSQL}},
		{&RemoveCollation{Name: "C_test", Locale: "C"}, []string{vendorPostgreSQL}},
		{&AddIndexConcurrently{ModelName: "Pony", Index: weightIndex()},
			[]string{vendorPostgreSQL, vendorCockroachDB, vendorGaussDB}},
		{&RemoveIndexConcurrently{ModelName: "Pony", Name: weightIndex().Name},
			[]string{vendorPostgreSQL, vendorCockroachDB, vendorGaussDB}},
		{&AddConstraintNotValid{ModelName: "Pony", Constraint: check()},
			[]string{vendorPostgreSQL, vendorCockroachDB, vendorGaussDB}},
		{&ValidateConstraint{ModelName: "Pony", Name: check().Name},
			[]string{vendorPostgreSQL, vendorCockroachDB, vendorGaussDB}},
	} {
		for _, vendor := range []string{vendorPostgreSQL, vendorCockroachDB, vendorGaussDB} {
			ed := newFakeEditor(vendor)
			from := stateWithPony("app", weightIndex())
			// CreateExtension queries pg_extension through the gorm
			// handle, which the fake does not have; the other operations
			// run to completion.
			if _, ok := tc.op.(*CreateExtension); ok {
				if got := hasExtensions(ed); got != slices.Contains(tc.vendors, vendor) {
					t.Errorf("%s on %s: hasExtensions = %v", m.OpName(tc.op), vendor, got)
				}
				continue
			}
			if err := tc.op.DatabaseForwards("app", ed, from, from); err != nil {
				t.Fatalf("%s on %s: %v", m.OpName(tc.op), vendor, err)
			}
			ran := len(ed.sqls) > 0 || len(ed.calls) > 0
			if want := slices.Contains(tc.vendors, vendor); ran != want {
				t.Errorf("%s on %s: ran = %v, want %v", m.OpName(tc.op), vendor, ran, want)
			}
		}
	}
}

// TestRouterGuard checks that every operation asks the routers before doing
// anything, and passes the hints through.
//
// django: db/utils.py ConnectionRouter.allow_migrate
func TestRouterGuard(t *testing.T) {
	no := false
	for _, op := range allOperations() {
		ed := newFakeEditor(vendorPostgreSQL)
		ed.conn.allow = &no
		from := stateWithPony("app", weightIndex())
		if err := op.DatabaseForwards("app", ed, from, from); err != nil {
			t.Fatalf("%s: %v", m.OpName(op), err)
		}
		if len(ed.sqls) > 0 || len(ed.calls) > 0 {
			t.Errorf("%s ran %v %v although the router said no", m.OpName(op), ed.sqls, ed.calls)
		}
	}

	t.Run("hints reach the router", func(t *testing.T) {
		ed := newFakeEditor(vendorPostgreSQL)
		ed.conn.allow = &no
		op := &CreateExtension{Name: "hstore", Hints: map[string]any{"target_db": "replica"}}
		if err := op.DatabaseForwards("app", ed, nil, nil); err != nil {
			t.Fatal(err)
		}
		if ed.conn.app != "app" || ed.conn.hints["target_db"] != "replica" {
			t.Errorf("router saw app %q hints %v", ed.conn.app, ed.conn.hints)
		}
	})
}

// TestNotInTransaction checks the message Django's NotInTransactionMixin
// raises, adapted to a Go error string and to the name of the Go migration
// field the reader has to set.
//
// django: contrib/postgres/operations.py NotInTransactionMixin
func TestNotInTransaction(t *testing.T) {
	for _, tc := range []struct {
		op   m.Operation
		want string
	}{
		{&AddIndexConcurrently{ModelName: "Pony", Index: weightIndex()},
			"the AddIndexConcurrently operation cannot be executed inside a transaction (set Atomic: m.Ptr(false) on the migration)"},
		{&RemoveIndexConcurrently{ModelName: "Pony", Name: weightIndex().Name},
			"the RemoveIndexConcurrently operation cannot be executed inside a transaction (set Atomic: m.Ptr(false) on the migration)"},
	} {
		ed := newFakeEditor(vendorPostgreSQL)
		ed.conn.inBlock = true
		from := stateWithPony("app", weightIndex())
		err := tc.op.DatabaseForwards("app", ed, from, from)
		if err == nil || err.Error() != tc.want {
			t.Fatalf("%s forwards: err = %v, want %q", m.OpName(tc.op), err, tc.want)
		}
		var notSupported *m.NotSupportedError
		if !errors.As(err, &notSupported) {
			t.Errorf("%s: error is %T, want *migrations.NotSupportedError", m.OpName(tc.op), err)
		}
		if err := tc.op.DatabaseBackwards("app", ed, from, from); err == nil {
			t.Errorf("%s backwards: no error inside a transaction", m.OpName(tc.op))
		}
		if len(ed.calls) > 0 {
			t.Errorf("%s ran %v inside a transaction", m.OpName(tc.op), ed.calls)
		}
	}
}

// TestConcurrentIndexSteps checks that the index operations reach the
// concurrent steps of the schema editor in both directions.
func TestConcurrentIndexSteps(t *testing.T) {
	ix := weightIndex()
	for _, tc := range []struct {
		op        m.Operation
		forwards  string
		backwards string
	}{
		{&AddIndexConcurrently{ModelName: "Pony", Index: ix},
			"AddIndexConcurrently app_pony " + ix.Name,
			"RemoveIndexConcurrently app_pony " + ix.Name},
		{&RemoveIndexConcurrently{ModelName: "Pony", Name: ix.Name},
			"RemoveIndexConcurrently app_pony " + ix.Name,
			"AddIndexConcurrently app_pony " + ix.Name},
	} {
		ed := newFakeEditor(vendorPostgreSQL)
		s := stateWithPony("app", ix)
		if err := tc.op.DatabaseForwards("app", ed, s, s); err != nil {
			t.Fatal(err)
		}
		if err := tc.op.DatabaseBackwards("app", ed, s, s); err != nil {
			t.Fatal(err)
		}
		if got := []string{tc.forwards, tc.backwards}; !slices.Equal(ed.calls, got) {
			t.Errorf("%s: calls = %v, want %v", m.OpName(tc.op), ed.calls, got)
		}
	}
}

// TestConstraintSteps checks the two constraint operations in both
// directions.
func TestConstraintSteps(t *testing.T) {
	ed := newFakeEditor(vendorPostgreSQL)
	s := stateWithPony("app")
	add := &AddConstraintNotValid{ModelName: "Pony", Constraint: check()}
	if err := add.DatabaseForwards("app", ed, s, s); err != nil {
		t.Fatal(err)
	}
	if err := add.DatabaseBackwards("app", ed, s, s); err != nil {
		t.Fatal(err)
	}
	validate := &ValidateConstraint{ModelName: "Pony", Name: check().Name}
	if err := validate.DatabaseForwards("app", ed, s, s); err != nil {
		t.Fatal(err)
	}
	if err := validate.DatabaseBackwards("app", ed, s, s); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"AddConstraintNotValid app_pony pony_weight_gt0",
		"RemoveConstraint app_pony pony_weight_gt0",
		"ValidateConstraint app_pony pony_weight_gt0",
	}
	if !slices.Equal(ed.calls, want) {
		t.Errorf("calls = %v, want %v", ed.calls, want)
	}
}

// TestCollationSQL checks the statements Django's CollationOperation
// builds, including the order of the arguments and the quoting of the
// locale and the provider.
//
// django: contrib/postgres/operations.py CollationOperation.create_collation
func TestCollationSQL(t *testing.T) {
	for _, tc := range []struct {
		name string
		op   m.Operation
		want []string
	}{
		{"default provider",
			&CreateCollation{Name: "C_test", Locale: "C"},
			[]string{`CREATE COLLATION "C_test" (locale="C")`}},
		{"an explicit libc provider is left out, as Django leaves it out",
			&CreateCollation{Name: "C_test", Locale: "C", Provider: "libc"},
			[]string{`CREATE COLLATION "C_test" (locale="C")`}},
		{"icu provider",
			&CreateCollation{Name: "case_insensitive", Locale: "und-u-ks-level2", Provider: "icu",
				Deterministic: m.Ptr(false)},
			[]string{`CREATE COLLATION "case_insensitive" (locale="und-u-ks-level2", provider="icu", deterministic=false)`}},
		{"removal",
			&RemoveCollation{Name: "C_test", Locale: "C"},
			[]string{`DROP COLLATION "C_test"`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ed := newFakeEditor(vendorPostgreSQL)
			if err := tc.op.DatabaseForwards("app", ed, nil, nil); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(ed.sqls, tc.want) {
				t.Errorf("sql = %v, want %v", ed.sqls, tc.want)
			}
		})
	}

	t.Run("backwards is the other statement", func(t *testing.T) {
		ed := newFakeEditor(vendorPostgreSQL)
		op := &CreateCollation{Name: "C_test", Locale: "C"}
		if err := op.DatabaseBackwards("app", ed, nil, nil); err != nil {
			t.Fatal(err)
		}
		rm := &RemoveCollation{Name: "C_test", Locale: "C"}
		if err := rm.DatabaseBackwards("app", ed, nil, nil); err != nil {
			t.Fatal(err)
		}
		want := []string{`DROP COLLATION "C_test"`, `CREATE COLLATION "C_test" (locale="C")`}
		if !slices.Equal(ed.sqls, want) {
			t.Errorf("sql = %v, want %v", ed.sqls, want)
		}
	})
}
