package conformance

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"gorm.io/gorm"

	"github.com/ctolon/gormgate/backends/base"
	"github.com/ctolon/gormgate/itest/internal/harness"
	m "github.com/ctolon/gormgate/migrations"
)

// Case is one conformance scenario. Setup becomes migration 0001 and Ops
// migration 0002 of the case's app.
type Case struct {
	Name  string
	Setup []m.Operation
	Ops   []m.Operation
	// Atomic false makes 0002 non-atomic.
	NonAtomic bool
	// Before runs after 0001 (to insert rows); After runs after each
	// forward application of 0002; AfterBackwards after unapplying it.
	Before         func(t testing.TB, db *gorm.DB)
	After          func(t testing.TB, db *gorm.DB)
	AfterBackwards func(t testing.TB, db *gorm.DB)
	// Skip returns a reason to skip the case on a backend.
	Skip func(b *base.Backend) string
	// NotSQL marks cases whose 0002 cannot be written as SQL (RunGo).
	NotSQL bool
	// Irreversible cases only run forwards.
	Irreversible bool
	// ExpectError is a substring of the error 0002 must fail with; the
	// database must then be unchanged (on backends that can roll back DDL).
	ExpectError string
}

// Env is one vendor under test: two isolated databases.
type Env struct {
	// A migrates; B builds fresh schemas and replays collected SQL.
	A, B func(testing.TB) *gorm.DB
	Norm *Normalizer
}

// Run executes cases as subtests.
func Run(t *testing.T, env Env, builders []Builder) {
	dbA := env.A(t)
	dbB := env.B(t)
	quote := func(s string) string { return dbA.Statement.Quote(s) }
	for i, b := range builders {
		app := fmt.Sprintf("c%03d", i)
		c := b(app+"_", quote)
		t.Run(c.Name, func(t *testing.T) {
			runCase(t, env, dbA, dbB, app, c)
		})
	}
}

func migration(app, name string, deps []m.Key, ops []m.Operation, atomic bool) *m.Migration {
	mig := &m.Migration{App: app, Name: name, Dependencies: deps, Operations: ops}
	if !atomic {
		mig.Atomic = m.Ptr(false)
	}
	return mig
}

// tablesOf lists the tables of every model in the given states.
func tablesOf(states ...*m.ProjectState) []string {
	seen := map[string]bool{}
	for _, s := range states {
		for _, ms := range s.Models {
			seen[ms.Table] = true
		}
	}
	var out []string
	for t := range seen {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

func statesOf(t testing.TB, app string, migs ...*m.Migration) []*m.ProjectState {
	st := m.NewProjectState()
	out := []*m.ProjectState{st}
	for _, mig := range migs {
		var err error
		st, err = mig.MutateState(st, true)
		if err != nil {
			t.Fatalf("state of %s: %v", mig.Name, err)
		}
		out = append(out, st)
	}
	return out
}

func snap(t testing.TB, p *harness.Project, tables []string, norm *Normalizer) Snapshot {
	t.Helper()
	s, err := Take(p.Conn.Introspection(), tables, norm)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	return s
}

func expectSame(t testing.TB, what string, want, got Snapshot) {
	t.Helper()
	if d := Diff(want, got); d != "" {
		t.Fatalf("%s:\n%s", what, d)
	}
}

func runCase(t *testing.T, env Env, dbA, dbB *gorm.DB, app string, c Case) {
	pa := harness.New(t, dbA, app)
	if c.Skip != nil {
		if reason := c.Skip(pa.Conn.Backend); reason != "" {
			t.Skip(reason)
		}
	}
	var migs []*m.Migration
	var deps []m.Key
	if len(c.Setup) > 0 {
		migs = append(migs, migration(app, "0001_setup", nil, c.Setup, true))
		deps = []m.Key{{App: app, Name: "0001_setup"}}
	}
	target := migration(app, "0002_change", deps, c.Ops, !c.NonAtomic)
	migs = append(migs, target)
	pa.Register(migs...)
	states := statesOf(t, app, migs...)
	tables := tablesOf(states...)
	before := m.Key{App: app, Name: ""}
	if len(c.Setup) > 0 {
		before = m.Key{App: app, Name: "0001_setup"}
		pa.MustMigrate(before)
	}
	s0 := snap(t, pa, tables, env.Norm)
	if c.Before != nil {
		c.Before(t, dbA)
	}
	defer func() {
		if err := pa.Migrate(m.Key{App: app, Name: ""}); err != nil {
			t.Errorf("migrate %s zero: %v", app, err)
			return
		}
		if left := snap(t, pa, tables, env.Norm); len(left) > 0 {
			var names []string
			for n := range left {
				names = append(names, n)
			}
			t.Errorf("tables left after migrate zero: %s", strings.Join(names, ", "))
		}
	}()

	err := pa.Migrate(target.Key())
	if c.ExpectError != "" {
		if err == nil || !strings.Contains(err.Error(), c.ExpectError) {
			t.Fatalf("migrate: error %v, want one containing %q", err, c.ExpectError)
		}
		if pa.Conn.Backend.Features.CanRollbackDDL {
			expectSame(t, "schema after failed atomic migration", s0, snap(t, pa, tables, env.Norm))
		}
		applied, _ := pa.Executor().Recorder.AppliedMigrations()
		for _, k := range applied {
			if k == target.Key() {
				t.Fatalf("failed migration %s was recorded as applied", k)
			}
		}
		return
	}
	if err != nil {
		t.Fatalf("migrate %s: %v", target.Key(), err)
	}
	s1 := snap(t, pa, tables, env.Norm)
	if c.After != nil {
		c.After(t, dbA)
	}

	// The migrated schema must equal the schema created from scratch from
	// the final state.
	pb := harness.New(t, dbB, app+"_fresh")
	fresh := freshMigration(t, app+"_fresh", states[len(states)-1])
	pb.Register(fresh)
	pb.MustMigrate(fresh.Key())
	sf := snap(t, pb, tables, env.Norm)
	pb.MustMigrate(m.Key{App: app + "_fresh", Name: ""})
	expectSame(t, "migrated schema vs schema created from the final state", sf, s1)

	if c.Irreversible {
		return
	}
	// Backwards restores the initial schema; forwards again reproduces the
	// migrated one.
	pa.MustMigrate(before)
	expectSame(t, "schema after unapplying", s0, snap(t, pa, tables, env.Norm))
	if c.AfterBackwards != nil {
		c.AfterBackwards(t, dbA)
	}
	pa.MustMigrate(target.Key())
	expectSame(t, "schema after re-applying", s1, snap(t, pa, tables, env.Norm))

	if c.NotSQL {
		return
	}
	// The collected SQL (sqlmigrate) must produce the same schema.
	pc := harness.New(t, dbB, app)
	pc.Register(migs...)
	if len(c.Setup) > 0 {
		pc.MustMigrate(before)
	}
	sqls := pc.SQL(app, target.Name, false)
	for _, s := range executable(sqls) {
		if err := pc.Conn.Exec(s); err != nil {
			t.Fatalf("collected SQL failed: %v\n%s\nfull script:\n%s", err, s, strings.Join(sqls, "\n"))
		}
	}
	if err := pc.Executor().Recorder.RecordApplied(app, target.Name); err != nil {
		t.Fatal(err)
	}
	expectSame(t, "schema from collected SQL", s1, snap(t, pc, tables, env.Norm))
	pc.MustMigrate(m.Key{App: app, Name: ""})
}

// executable drops comment lines and trailing semicolons.
func executable(lines []string) []string {
	var out []string
	for _, l := range lines {
		s := strings.TrimSpace(l)
		if s == "" || strings.HasPrefix(s, "--") {
			continue
		}
		out = append(out, strings.TrimSuffix(s, ";"))
	}
	return out
}

// freshMigration creates every model of state in FK dependency order.
func freshMigration(_ testing.TB, app string, st *m.ProjectState) *m.Migration {
	keys := st.SortedKeys()
	done := map[m.ModelKey]bool{}
	var ops []m.Operation
	var visit func(k m.ModelKey)
	visiting := map[m.ModelKey]bool{}
	visit = func(k m.ModelKey) {
		if done[k] || visiting[k] {
			return
		}
		visiting[k] = true
		ms := st.Models[k]
		for _, f := range ms.Fields {
			if fk := f.Field.ForeignKey; fk != nil {
				if tk := fk.Target(k.App); tk != k {
					visit(tk)
				}
			}
		}
		for _, c := range ms.Options.Constraints {
			if fk, ok := c.(*m.ForeignKeyConstraint); ok {
				if tk := fk.Target(k.App); tk != k {
					visit(tk)
				}
			}
		}
		done[k] = true
		ops = append(ops, createOf(ms, app, st))
	}
	for _, k := range keys {
		visit(k)
	}
	return &m.Migration{App: app, Name: "0001_fresh", Operations: ops}
}

// createOf is a CreateModel for ms, moved to app (foreign keys to the
// original app are rewritten).
func createOf(ms *m.ModelState, app string, st *m.ProjectState) m.Operation {
	c := ms.Clone()
	for i, f := range c.Fields {
		if f.Field.ForeignKey != nil {
			tk := f.Field.ForeignKey.Target(ms.App)
			c.Fields[i].Field.ForeignKey.To = app + "." + st.Models[tk].Name
		}
	}
	for _, x := range c.Options.Constraints {
		if fk, ok := x.(*m.ForeignKeyConstraint); ok {
			fk.To = app + "." + st.Models[fk.Target(ms.App)].Name
		}
	}
	return &m.CreateModel{Name: c.Name, Table: c.Table, Fields: c.Fields, Options: c.Options}
}

// FreshMigration builds a migration that creates every model of a state
// from scratch, in foreign-key dependency order.
func FreshMigration(app string, st *m.ProjectState) *m.Migration {
	return freshMigration(nil, app, st)
}
