//go:build integration

package itest

import (
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"testing"

	"strings"

	"gorm.io/gorm"

	"github.com/ctolon/gormgate/internal/autodetector"
	"github.com/ctolon/gormgate/internal/questioner"
	"github.com/ctolon/gormgate/itest/internal/conformance"
	"github.com/ctolon/gormgate/itest/internal/dbtest"
	"github.com/ctolon/gormgate/itest/internal/harness"
	m "github.com/ctolon/gormgate/migrations"
)

// TestPropertyEvolution evolves a random schema step by step: every step
// autodetects the change, applies it, and requires that the database then
// matches a schema built from scratch and that no further change is
// detected. At the end everything is unapplied again.
func TestPropertyEvolution(t *testing.T) {
	seeds := []int64{1, 2, 3}
	if v := os.Getenv("GORMGATE_PROPERTY_SEEDS"); v != "" {
		seeds = nil
		for _, s := range splitComma(v) {
			n, err := strconv.ParseInt(s, 10, 64)
			if err != nil {
				t.Fatalf("GORMGATE_PROPERTY_SEEDS: %v", err)
			}
			seeds = append(seeds, n)
		}
	}
	steps := 12
	if v := os.Getenv("GORMGATE_PROPERTY_STEPS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			t.Fatalf("GORMGATE_PROPERTY_STEPS: %v", err)
		}
		steps = n
	}
	for _, key := range vendorsUnderTest(t) {
		key := key
		t.Run(key, func(t *testing.T) {
			for _, seed := range seeds {
				seed := seed
				t.Run(fmt.Sprintf("seed%d", seed), func(t *testing.T) {
					dbA, _ := dbtest.Open(t, key)
					dbB, _ := dbtest.Open(t, key)
					evolve(t, key, dbA, dbB, seed, steps)
				})
			}
		})
	}
}

func evolve(t *testing.T, key string, dbA, dbB *gorm.DB, seed int64, steps int) {
	rng := rand.New(rand.NewSource(seed))
	app := fmt.Sprintf("p%d", seed)
	prefix := app + "_"
	pa := harness.New(t, dbA, app)
	// The random corpus below uses foreign keys and unique constraints in
	// every schema it generates; a backend that has neither (ClickHouse) is
	// covered by its own test instead.
	if f := pa.Conn.Backend.Features; !f.SupportsForeignKeys || !f.SupportsUniqueConstraints {
		t.Skip("the generated schemas use foreign keys and unique constraints")
	}
	norm := conformance.Normalizers[key]

	state := m.NewProjectState()
	var migs []*m.Migration
	var prev *m.Migration
	for step := 0; step <= steps; step++ {
		next := state.Clone()
		if step == 0 {
			initialState(next, app, prefix, rng)
		} else if !mutate(next, app, prefix, rng) {
			continue
		}
		ad := autodetector.New(state, next, &questioner.Base{SpecifiedApps: map[string]bool{app: true}})
		changes, err := ad.ChangesFor(pa.Executor().Loader.Graph, autodetector.ChangesOptions{ConvertApps: []string{app}})
		if err != nil {
			t.Fatalf("step %d: autodetect: %v", step, err)
		}
		for _, mig := range changes[app] {
			// Chain the generated migrations onto the previous one.
			if prev != nil {
				mig.Dependencies = append(mig.Dependencies, prev.Key())
			}
			pa.Register(mig)
			migs = append(migs, mig)
			prev = mig
		}
		if len(changes[app]) == 0 {
			state = next
			continue
		}
		if err := pa.Migrate(prev.Key()); err != nil {
			t.Fatalf("step %d: migrate %s: %v\noperations: %s", step, prev.Name, err, describe(prev))
		}
		state = next

		// Nothing further must be detected against the same models.
		again, err := autodetector.New(state, state.Clone(), &questioner.Base{SpecifiedApps: map[string]bool{app: true}}).
			ChangesFor(pa.Executor().Loader.Graph, autodetector.ChangesOptions{ConvertApps: []string{app}})
		if err != nil {
			t.Fatalf("step %d: autodetect again: %v", step, err)
		}
		if len(again) != 0 {
			t.Fatalf("step %d: changes detected against an unchanged state: %v", step, again)
		}

		// The migrated schema must equal one built from scratch.
		tables := tablesOfState(state)
		got := snapshotOf(t, pa, tables, norm)
		fresh := freshBuild(t, dbB, app+"_fresh", state)
		want := snapshotOf(t, fresh, tables, norm)
		cleanup(t, fresh, app+"_fresh")
		if d := conformance.Diff(want, got); d != "" {
			t.Fatalf("step %d (%s):\n%s\noperations:\n%s", step, prev.Name, d, describe(prev))
		}
	}
	// Unapply everything.
	if err := pa.Migrate(m.Key{App: app, Name: ""}); err != nil {
		t.Fatalf("migrate zero: %v", err)
	}
	if left := snapshotOf(t, pa, tablesOfState(state), norm); len(left) > 0 {
		t.Fatalf("tables left after migrate zero: %v", left)
	}
	_ = migs
}

func describe(mig *m.Migration) string {
	out := ""
	for _, op := range mig.Operations {
		out += "  " + m.FormattedDescription(op) + "\n"
	}
	return out
}

// initialState builds the starting schema: two models with a foreign key.
func initialState(st *m.ProjectState, app, prefix string, rng *rand.Rand) {
	st.AddModel(&m.ModelState{App: app, Name: "Author", Table: prefix + "author", Fields: m.Fields{
		{Name: "id", Field: m.Field{Type: m.Int, Size: 64, PrimaryKey: true, AutoIncrement: true}},
		{Name: "name", Field: m.Field{Type: m.String, Size: 100, Null: true}},
	}})
	st.AddModel(&m.ModelState{App: app, Name: "Book", Table: prefix + "book", Fields: m.Fields{
		{Name: "id", Field: m.Field{Type: m.Int, Size: 64, PrimaryKey: true, AutoIncrement: true}},
		{Name: "title", Field: m.Field{Type: m.String, Size: 200, Null: true}},
		{Name: "author_id", Field: m.Field{Type: m.Int, Size: 64, Null: true, ForeignKey: &m.ForeignKey{
			To: "Author", ToField: "id", Name: "fk_" + prefix + "book_author",
		}}},
	}})
}

// mutate applies one random change to the state; it returns false when the
// draw produced no change.
func mutate(st *m.ProjectState, app, prefix string, rng *rand.Rand) bool {
	keys := st.SortedKeys()
	if len(keys) == 0 {
		initialState(st, app, prefix, rng)
		return true
	}
	k := keys[rng.Intn(len(keys))]
	ms := st.Models[k]
	switch rng.Intn(10) {
	case 0: // add a nullable field
		name := fmt.Sprintf("f%d", len(ms.Fields)+rng.Intn(1000))
		ms.Fields = append(ms.Fields, m.NamedField{Name: name, Field: randomField(rng, true)})
	case 1: // add a field with a database default
		name := fmt.Sprintf("d%d", len(ms.Fields)+rng.Intn(1000))
		f := randomField(rng, false)
		f.DBDefault = defaultFor(f)
		ms.Fields = append(ms.Fields, m.NamedField{Name: name, Field: f})
	case 2: // remove a field that is neither the primary key nor referenced
		if i := removableField(st, ms); i >= 0 {
			ms.Fields = append(ms.Fields[:i:i], ms.Fields[i+1:]...)
			dropIndexesOf(ms, ms.Fields)
		} else {
			return false
		}
	case 3: // rename a field
		if i := removableField(st, ms); i >= 0 {
			newName := ms.Fields[i].Name + "_r"
			old := ms.Fields[i].Name
			if err := st.RenameField(k.App, k.Model, old, newName); err != nil {
				return false
			}
		} else {
			return false
		}
	case 4: // change nullability of a nullable field
		if i := removableField(st, ms); i >= 0 {
			f := ms.Fields[i].Field
			if f.Null {
				f.Null = false
				f.DBDefault = defaultFor(f)
			} else {
				f.Null = true
				f.DBDefault = nil
			}
			ms.Fields[i].Field = f
		} else {
			return false
		}
	case 5: // change the size or type of a string field
		if i := removableField(st, ms); i >= 0 && ms.Fields[i].Field.Type == m.String {
			f := ms.Fields[i].Field
			if f.Size == 0 {
				f.Size = 50 + rng.Intn(150)
			} else if rng.Intn(2) == 0 {
				f.Size += 50
			} else {
				f.Size = 0
			}
			ms.Fields[i].Field = f
		} else {
			return false
		}
	case 6: // add an index
		if len(ms.Fields) < 2 {
			return false
		}
		col := ms.Fields[1+rng.Intn(len(ms.Fields)-1)].Name
		name := fmt.Sprintf("idx_%s%s_%s_%d", prefix, ms.NameLower(), col, rng.Intn(1000))
		for _, ix := range ms.Options.Indexes {
			if len(ix.Fields) == 1 && ix.Fields[0].Column == col {
				return false
			}
		}
		ms.Options.Indexes = append(ms.Options.Indexes, m.Index{Name: name, Fields: []m.IndexField{{Column: col}}})
	case 7: // remove an index
		if len(ms.Options.Indexes) == 0 {
			return false
		}
		i := rng.Intn(len(ms.Options.Indexes))
		ms.Options.Indexes = append(ms.Options.Indexes[:i:i], ms.Options.Indexes[i+1:]...)
	case 8: // add a model with a foreign key to an existing one
		name := fmt.Sprintf("M%d", len(st.Models)+rng.Intn(1000))
		table := prefix + strings.ToLower(name)
		st.AddModel(&m.ModelState{App: app, Name: name, Table: table, Fields: m.Fields{
			{Name: "id", Field: m.Field{Type: m.Int, Size: 64, PrimaryKey: true, AutoIncrement: true}},
			{Name: "ref_id", Field: m.Field{Type: m.Int, Size: 64, Null: true, ForeignKey: &m.ForeignKey{
				To: ms.Name, ToField: "id", Name: "fk_" + table + "_ref",
			}}},
		}})
	case 9: // drop a model nothing references
		if len(st.Models) <= 2 || len(m.GetReferences(st, k, "")) > 0 {
			return false
		}
		st.RemoveModel(k.App, k.Model)
	}
	return true
}

func randomField(rng *rand.Rand, null bool) m.Field {
	switch rng.Intn(5) {
	case 0:
		return m.Field{Type: m.Int, Size: 32, Null: null}
	case 1:
		return m.Field{Type: m.Int, Size: 64, Null: null}
	case 2:
		return m.Field{Type: m.String, Size: 20 + rng.Intn(200), Null: null}
	case 3:
		return m.Field{Type: m.Bool, Null: null}
	default:
		return m.Field{Type: m.Time, Null: null}
	}
}

func defaultFor(f m.Field) *m.DBDefault {
	switch f.Type {
	case m.Int:
		return m.DBValue(int64(0))
	case m.String:
		return m.DBValue("x")
	case m.Bool:
		return m.DBValue(false)
	}
	return nil
}

// removableField returns the index of a field that may be changed: not the
// primary key, not a foreign key and not referenced by another model.
func removableField(st *m.ProjectState, ms *m.ModelState) int {
	for i, f := range ms.Fields {
		if f.Field.PrimaryKey || f.Field.ForeignKey != nil {
			continue
		}
		if m.FieldIsReferenced(st, ms.Key(), f.Name) {
			continue
		}
		indexed := false
		for _, ix := range ms.Options.Indexes {
			for _, c := range ix.Fields {
				if c.Column == f.Name {
					indexed = true
				}
			}
		}
		if indexed {
			continue
		}
		return i
	}
	return -1
}

// dropIndexesOf removes indexes that reference columns which no longer
// exist.
func dropIndexesOf(ms *m.ModelState, fields m.Fields) {
	var kept []m.Index
	for _, ix := range ms.Options.Indexes {
		ok := true
		for _, c := range ix.Fields {
			if c.Column != "" && fields.Index(c.Column) < 0 {
				ok = false
			}
		}
		if ok {
			kept = append(kept, ix)
		}
	}
	ms.Options.Indexes = kept
}

func tablesOfState(st *m.ProjectState) []string {
	var out []string
	for _, k := range st.SortedKeys() {
		out = append(out, st.Models[k].Table)
	}
	return out
}

func snapshotOf(t *testing.T, p *harness.Project, tables []string, norm *conformance.Normalizer) conformance.Snapshot {
	t.Helper()
	s, err := conformance.Take(p.Conn.Introspection(), tables, norm)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	return s
}

// freshBuild creates every model of the state from scratch in dbB.
func freshBuild(t *testing.T, dbB *gorm.DB, app string, st *m.ProjectState) *harness.Project {
	t.Helper()
	p := harness.New(t, dbB, app)
	p.Register(conformance.FreshMigration(app, st))
	p.MustMigrate(m.Key{App: app, Name: "0001_fresh"})
	return p
}

func cleanup(t *testing.T, p *harness.Project, app string) {
	t.Helper()
	if err := p.Migrate(m.Key{App: app, Name: ""}); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
}
