package migrations

import (
	"fmt"
	"regexp"
	"time"
)

// Key identifies a migration: (app_label, name).
type Key struct {
	App  string
	Name string
}

func (k Key) String() string { return k.App + "." + k.Name }

// Dep builds a dependency on a migration.
func Dep(app, name string) Key { return Key{app, name} }

// First builds a dependency on whichever migration of app comes first.
func First(app string) Key { return Key{app, "__first__"} }

// Latest builds a dependency on whichever migration of app comes last.
func Latest(app string) Key { return Key{app, "__latest__"} }

// Migration is one migration file's content.
//
// django: migrations/migration.py Migration
type Migration struct {
	App          string
	Name         string
	Dependencies []Key
	RunBefore    []Key
	// Replaces lists the migrations (of the same or other apps) this
	// squashed migration replaces.
	Replaces []Key
	// Initial marks the migration as the app's first; nil leaves the
	// loader to work it out from the dependencies.
	Initial *bool
	// Atomic runs the whole migration in one transaction; nil means
	// true.
	Atomic     *bool
	Operations []Operation

	// File is the source file that registered the migration (set by
	// Register).
	File string
}

// Key returns the migration key.
func (m *Migration) Key() Key { return Key{m.App, m.Name} }

func (m *Migration) String() string { return m.App + "." + m.Name }

// IsAtomic reports the migration's atomic flag.
func (m *Migration) IsAtomic() bool { return m.Atomic == nil || *m.Atomic }

// IsInitial reports whether the migration is marked as an app's first.
func (m *Migration) IsInitial() bool { return m.Initial != nil && *m.Initial }

// Clone returns a copy whose dependency, replaces and operation slices can
// be mutated without affecting m. The operations themselves are shared.
func (m *Migration) Clone() *Migration {
	cp := *m
	cp.Dependencies = append([]Key(nil), m.Dependencies...)
	cp.RunBefore = append([]Key(nil), m.RunBefore...)
	cp.Replaces = append([]Key(nil), m.Replaces...)
	cp.Operations = append([]Operation(nil), m.Operations...)
	return &cp
}

// MutateState applies the operations to a copy of state (or state itself
// when preserve is false).
//
// django: migration.py Migration.mutate_state
func (m *Migration) MutateState(state *ProjectState, preserve bool) (*ProjectState, error) {
	ns := state
	if preserve {
		ns = state.Clone()
	}
	for _, op := range m.Operations {
		if err := op.StateForwards(m.App, ns); err != nil {
			return nil, err
		}
	}
	return ns, nil
}

// atomicOperation reports whether op must run inside a transaction: either
// the operation demands one, or the migration is atomic and the operation
// does not refuse one.
func atomicOperation(m *Migration, op Operation) bool {
	a := op.Atomic()
	if a != nil && *a {
		return true
	}
	return m.IsAtomic() && (a == nil || *a)
}

// Apply runs the migration forwards.
//
// django: migration.py Migration.apply
func (m *Migration) Apply(state *ProjectState, ed SchemaEditor) (*ProjectState, error) {
	collect := ed.CollectSQL()
	for _, op := range m.Operations {
		before := 0
		if collect {
			ed.AddCollected("--", "-- "+op.Describe(), "--")
			if !op.ReducesToSQL() {
				ed.AddCollected("-- THIS OPERATION CANNOT BE WRITTEN AS SQL")
				continue
			}
			before = len(ed.CollectedSQL())
		}
		oldState := state.Clone()
		if err := op.StateForwards(m.App, state); err != nil {
			return nil, err
		}
		run := func() error { return op.DatabaseForwards(m.App, ed, oldState, state) }
		var err error
		if !ed.AtomicMigration() && atomicOperation(m, op) {
			err = ed.Atomic(run)
		} else {
			err = run()
		}
		if err != nil {
			return nil, err
		}
		if collect && before == len(ed.CollectedSQL()) {
			ed.AddCollected("-- (no-op)")
		}
	}
	return state, nil
}

// Unapply runs the migration backwards.
//
// django: migration.py Migration.unapply
func (m *Migration) Unapply(state *ProjectState, ed SchemaEditor) (*ProjectState, error) {
	type step struct {
		op       Operation
		to, from *ProjectState
	}
	var toRun []step
	newState := state
	for _, op := range m.Operations {
		if !op.Reversible() {
			return nil, &IrreversibleError{Msg: fmt.Sprintf("operation %s in %s is not reversible", FormatOperation(op), m)}
		}
		newState = newState.Clone()
		oldState := newState.Clone()
		if err := op.StateForwards(m.App, newState); err != nil {
			return nil, err
		}
		toRun = append([]step{{op, oldState, newState}}, toRun...)
	}
	collect := ed.CollectSQL()
	for _, s := range toRun {
		before := 0
		if collect {
			ed.AddCollected("--", "-- "+s.op.Describe(), "--")
			if !s.op.ReducesToSQL() {
				ed.AddCollected("-- THIS OPERATION CANNOT BE WRITTEN AS SQL")
				continue
			}
			before = len(ed.CollectedSQL())
		}
		run := func() error { return s.op.DatabaseBackwards(m.App, ed, s.from, s.to) }
		var err error
		if !ed.AtomicMigration() && atomicOperation(m, s.op) {
			err = ed.Atomic(run)
		} else {
			err = run()
		}
		if err != nil {
			return nil, err
		}
		if collect && before == len(ed.CollectedSQL()) {
			ed.AddCollected("-- (no-op)")
		}
	}
	return state, nil
}

var nonWord = regexp.MustCompile(`\W+`)

// maxSuggestedNameLen is how long a suggested name may grow before the
// remaining fragments are replaced by "_and_more". It is Django's budget for
// the name part of a migration file.
const maxSuggestedNameLen = 52

// MigrationNameTimestamp returns the timestamp SuggestName falls back to
// when the operations suggest no name, formatted as YYYYMMDD_HHMM. It is a
// variable so that tests can pin the clock.
var MigrationNameTimestamp = func() string { return time.Now().Format("20060102_1504") }

// SuggestName suggests a name for the migration's operations.
//
// django: migration.py Migration.suggest_name
func (m *Migration) SuggestName() string {
	if m.IsInitial() {
		return "initial"
	}
	var fragments []string
	for _, op := range m.Operations {
		if f := op.MigrationNameFragment(); f != "" {
			fragments = append(fragments, nonWord.ReplaceAllString(f, "_"))
		}
	}
	if len(fragments) == 0 || len(fragments) != len(m.Operations) {
		return "auto_" + MigrationNameTimestamp()
	}
	name := fragments[0]
	for _, fragment := range fragments[1:] {
		newName := name + "_" + fragment
		if len(newName) > maxSuggestedNameLen {
			name = name + "_and_more"
			break
		}
		name = newName
	}
	return name
}

// PlanStep is one (migration, backwards) entry of an execution plan.
type PlanStep struct {
	Migration *Migration
	Backwards bool
}
