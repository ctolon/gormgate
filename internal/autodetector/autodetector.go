// Package autodetector compares two project states and produces the
// migrations that turn the first into the second.
//
// django: db/migrations/autodetector.py
package autodetector

import (
	"cmp"
	"fmt"
	"maps"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/ctolon/gormgate/internal/graph"
	"github.com/ctolon/gormgate/internal/optimizer"
	"github.com/ctolon/gormgate/internal/questioner"
	m "github.com/ctolon/gormgate/migrations"
)

// opDep is one thing a generated operation has to wait for. A dependency is
// either on a model or on a field of one, and those are separate types: a
// dependency cannot name a field where only a model makes sense, and every
// dependency can say which operation satisfies it.
//
// django: autodetector.py OperationDependency
type opDep interface {
	// app is the label of the app the dependency is on.
	app() string
	// satisfiedBy reports whether op is the operation waited for.
	//
	// django: autodetector.py MigrationAutodetector.check_dependency
	satisfiedBy(op m.Operation) bool
}

// modelDepType says what a modelDep waits for.
type modelDepType int

const (
	modelCreated modelDepType = iota
	modelDeleted
)

// modelDep waits for a model to be created or deleted.
type modelDep struct {
	App   string
	Model string
	Type  modelDepType
}

func (d modelDep) app() string { return d.App }

func (d modelDep) satisfiedBy(op m.Operation) bool {
	switch d.Type {
	case modelCreated:
		x, ok := op.(*m.CreateModel)
		return ok && strings.EqualFold(x.Name, d.Model)
	case modelDeleted:
		x, ok := op.(*m.DeleteModel)
		return ok && strings.EqualFold(x.Name, d.Model)
	}
	return false
}

// fieldDepType says what a fieldDep waits for.
type fieldDepType int

const (
	fieldCreated fieldDepType = iota
	fieldRemoved
	fieldAltered
	uniqueTogetherAltered
	indexOrConstraintRemoved
)

// fieldDep waits for an operation on one field of a model.
type fieldDep struct {
	App   string
	Model string
	Field string
	Type  fieldDepType
}

func (d fieldDep) app() string { return d.App }

func (d fieldDep) satisfiedBy(op m.Operation) bool {
	switch d.Type {
	case fieldCreated:
		if x, ok := op.(*m.CreateModel); ok && strings.EqualFold(x.Name, d.Model) {
			return x.Fields.Index(d.Field) >= 0
		}
		x, ok := op.(*m.AddField)
		return ok && strings.EqualFold(x.ModelName, d.Model) && strings.EqualFold(x.Name, d.Field)
	case fieldRemoved:
		x, ok := op.(*m.RemoveField)
		return ok && strings.EqualFold(x.ModelName, d.Model) && strings.EqualFold(x.Name, d.Field)
	case fieldAltered:
		x, ok := op.(*m.AlterField)
		return ok && strings.EqualFold(x.ModelName, d.Model) && strings.EqualFold(x.Name, d.Field)
	case uniqueTogetherAltered:
		x, ok := op.(*m.AlterUniqueTogether)
		return ok && strings.EqualFold(x.Name, d.Model)
	case indexOrConstraintRemoved:
		switch x := op.(type) {
		case *m.RemoveIndex:
			return strings.EqualFold(x.ModelName, d.Model)
		case *m.RemoveConstraint:
			return strings.EqualFold(x.ModelName, d.Model)
		}
	}
	return false
}

// genOp is an operation the autodetector has generated, together with the
// operations it must follow.
type genOp struct {
	op   m.Operation
	deps []opDep
}

type fieldKey struct{ App, Model, Field string }

func sortFieldKeys(keys []fieldKey) {
	slices.SortFunc(keys, func(a, b fieldKey) int {
		return cmp.Or(
			cmp.Compare(a.App, b.App),
			cmp.Compare(a.Model, b.Model),
			cmp.Compare(a.Field, b.Field),
		)
	})
}

// keySet is a set of model keys.
type keySet map[m.ModelKey]bool

// sorted returns the set's keys in app, then model order.
func (s keySet) sorted() []m.ModelKey {
	return slices.SortedFunc(maps.Keys(s), func(a, b m.ModelKey) int {
		return cmp.Or(cmp.Compare(a.App, b.App), cmp.Compare(a.Model, b.Model))
	})
}

// difference returns the keys in a that are not in b.
func difference(a, b keySet) keySet {
	out := keySet{}
	for k := range a {
		if !b[k] {
			out[k] = true
		}
	}
	return out
}

// intersect returns the keys in both a and b.
func intersect(a, b keySet) keySet {
	out := keySet{}
	for k := range a {
		if b[k] {
			out[k] = true
		}
	}
	return out
}

// union returns the keys in any of sets.
func union(sets ...keySet) keySet {
	out := keySet{}
	for _, s := range sets {
		for k := range s {
			out[k] = true
		}
	}
	return out
}

type renamedOp struct {
	remApp, remModel, remField string
	app, model                 string
	field                      m.Field
	fieldName                  string
}

// renamedIndex records an index that kept its definition but changed name.
type renamedIndex struct{ old, new string }

// alteredIndexes collects the index changes of one model.
type alteredIndexes struct {
	key     m.ModelKey
	added   []m.Index
	removed []m.Index
	renamed []renamedIndex
}

type alteredConstraints struct {
	key     m.ModelKey
	added   []m.Constraint
	removed []m.Constraint
	altered []m.Constraint
}

// Autodetector compares two project states and produces the migrations
// that turn the first into the second.
//
// django: autodetector.py MigrationAutodetector
type Autodetector struct {
	from, to     *m.ProjectState
	q            questioner.Questioner
	existingApps map[string]bool

	generated     map[string][]*genOp
	generatedApps []string

	oldModelKeys, oldUnmanagedKeys keySet
	newModelKeys, newUnmanagedKeys keySet
	keptModelKeys, keptUnmanaged   keySet

	oldFieldKeys, newFieldKeys map[fieldKey]bool

	renamedModels    map[m.ModelKey]string // new key -> old model name (lower)
	renamedModelsRel map[string]string     // "app.old" -> "app.new"
	renamedFields    map[fieldKey]string   // new field key -> old field name
	renamedOps       []renamedOp

	altIndexes     []alteredIndexes
	altConstraints []alteredConstraints

	migrations    map[string][]*m.Migration
	migrationApps []string
}

// New builds an autodetector; q may be nil for the default questioner.
func New(from, to *m.ProjectState, q questioner.Questioner) *Autodetector {
	if q == nil {
		q = &questioner.Base{}
	}
	ex := map[string]bool{}
	for k := range from.Models {
		ex[k.App] = true
	}
	return &Autodetector{from: from, to: to, q: q, existingApps: ex}
}

// Order returns the app labels of changes in the order the apps first
// received a migration, with any app the autodetector did not generate
// appended in alphabetical order. The commands report apps in this order.
func (a *Autodetector) Order(changes map[string][]*m.Migration) []string {
	var out []string
	seen := map[string]bool{}
	for _, app := range a.migrationApps {
		if _, ok := changes[app]; ok && !seen[app] {
			out = append(out, app)
			seen[app] = true
		}
	}
	var rest []string
	for app := range changes {
		if !seen[app] {
			rest = append(rest, app)
		}
	}
	slices.Sort(rest)
	return append(out, rest...)
}

// ChangesOptions are the optional arguments of ChangesFor. The zero value
// detects the changes of every app and names the migrations after what they
// do.
type ChangesOptions struct {
	// TrimToApps keeps only these apps and the apps whose migrations they
	// depend on; empty keeps every app.
	TrimToApps []string
	// ConvertApps are the apps whose models are being turned from
	// unmigrated into migrated ones.
	ConvertApps []string
	// MigrationName is the name to give each generated migration, in
	// place of the one suggested from its operations.
	MigrationName string
}

// ChangesFor returns the migrations to write, keyed by app label. It detects
// the changes, groups them into migrations placed after g's leaves, and,
// with opts.TrimToApps, keeps only those apps and what they depend on.
//
// django: autodetector.py MigrationAutodetector.changes
func (a *Autodetector) ChangesFor(g *graph.Graph, opts ChangesOptions) (map[string][]*m.Migration, error) {
	changes, err := a.detectChanges(opts.ConvertApps, g)
	if err != nil {
		return nil, err
	}
	changes, err = a.ArrangeForGraph(changes, g, opts.MigrationName)
	if err != nil {
		return nil, err
	}
	if len(opts.TrimToApps) > 0 {
		changes = trimChangesToApps(changes, opts.TrimToApps)
	}
	return changes, nil
}

// qualifyRelationTarget rewrites a bare "model" foreign-key target in a
// deconstruction to the qualified "app.model" form. A deconstruction
// lower-cases the target but cannot resolve it, because a Field does not
// know its app; both sides of a comparison have to spell it the same way, or
// a field that did not change looks altered and a rename is never offered.
//
// django: migrations/utils.py resolve_relation
func qualifyRelationTarget(dec []m.KV, app string) []m.KV {
	for i, kv := range dec {
		fk, ok := kv.Value.(*m.ForeignKey)
		if !ok || strings.Contains(fk.To, ".") {
			continue
		}
		cp := *fk
		cp.To = app + "." + cp.To
		dec[i].Value = &cp
	}
	return dec
}

// deconstructWithoutTo returns f's deconstruction with the foreign-key
// target cleared, so that two fields can be compared ignoring what they
// point at.
//
// django: autodetector.py MigrationAutodetector.only_relation_agnostic_fields
func deconstructWithoutTo(f m.Field) []m.KV {
	var out []m.KV
	for _, kv := range f.Deconstruct() {
		if kv.Key == m.AttrForeignKey {
			fk := *kv.Value.(*m.ForeignKey)
			fk.To = ""
			kv.Value = &fk
		}
		out = append(out, kv)
	}
	return out
}

// onlyRelationAgnosticFields returns field definitions ignoring names and
// what related fields relate to, ordered by field name.
//
// django: autodetector.py MigrationAutodetector.only_relation_agnostic_fields
func onlyRelationAgnosticFields(fs m.Fields) [][]m.KV {
	sorted := slices.Clone(fs)
	slices.SortStableFunc(sorted, func(a, b m.NamedField) int { return cmp.Compare(a.Name, b.Name) })
	out := make([][]m.KV, len(sorted))
	for i, f := range sorted {
		out[i] = deconstructWithoutTo(f.Field)
	}
	return out
}

func kvEqual(a, b []m.KV) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Key != b[i].Key {
			return false
		}
		ra, oka := a[i].Value.(*m.TypeRef)
		rb, okb := b[i].Value.(*m.TypeRef)
		if oka && okb {
			if ra.String() != rb.String() {
				return false
			}
			continue
		}
		if !reflect.DeepEqual(a[i].Value, b[i].Value) {
			return false
		}
	}
	return true
}

func fieldDefsEqual(a, b [][]m.KV) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !kvEqual(a[i], b[i]) {
			return false
		}
	}
	return true
}

// django: autodetector.py MigrationAutodetector._detect_changes
func (a *Autodetector) detectChanges(convertApps []string, g *graph.Graph) (map[string][]*m.Migration, error) {
	a.generated = map[string][]*genOp{}
	a.renamedFields = map[fieldKey]string{}
	a.oldModelKeys, a.oldUnmanagedKeys = keySet{}, keySet{}
	a.newModelKeys, a.newUnmanagedKeys = keySet{}, keySet{}
	convert := map[string]bool{}
	for _, c := range convertApps {
		convert[c] = true
	}
	for k, ms := range a.from.Models {
		if !ms.Options.IsManaged() {
			a.oldUnmanagedKeys[k] = true
		} else if !a.from.RealApps[k.App] {
			a.oldModelKeys[k] = true
		}
	}
	for k, ms := range a.to.Models {
		if !ms.Options.IsManaged() {
			a.newUnmanagedKeys[k] = true
		} else if !a.from.RealApps[k.App] || convert[k.App] {
			a.newModelKeys[k] = true
		}
	}

	steps := []func() error{
		a.generateRenamedModels,
		func() error { a.prepareFieldLists(); return nil },
		a.generateDeletedModels,
		a.generateCreatedModels,
		a.generateAlteredOptions,
		a.generateAlteredDBTableComment,
		a.createRenamedFields,
		a.createAlteredIndexes,
		a.createAlteredConstraints,
		a.generateRemovedConstraints,
		a.generateRemovedIndexes,
		a.generateRenamedFields,
		a.generateRenamedIndexes,
		a.generateRemovedAlteredUniqueTogether,
		a.generateRemovedFields,
		a.generateAddedFields,
		a.generateAlteredFields,
		a.generateAlteredUniqueTogether,
		a.generateAddedIndexes,
		a.generateAddedConstraints,
		a.generateAlteredConstraints,
		a.generateAlteredDBTable,
		a.sortMigrations,
		func() error { return a.buildMigrationList(g) },
		func() error { a.optimizeMigrations(); return nil },
	}
	for _, step := range steps {
		if err := step(); err != nil {
			return nil, err
		}
	}
	return a.migrations, nil
}

func (a *Autodetector) oldName(k m.ModelKey) string {
	if n, ok := a.renamedModels[k]; ok {
		return n
	}
	return k.Model
}

func (a *Autodetector) oldState(k m.ModelKey) *m.ModelState {
	return a.from.Models[m.ModelKey{App: k.App, Model: a.oldName(k)}]
}

// django: autodetector.py MigrationAutodetector._prepare_field_lists
func (a *Autodetector) prepareFieldLists() {
	a.keptModelKeys = intersect(a.oldModelKeys, a.newModelKeys)
	a.keptUnmanaged = intersect(a.oldUnmanagedKeys, a.newUnmanagedKeys)
	a.oldFieldKeys = map[fieldKey]bool{}
	a.newFieldKeys = map[fieldKey]bool{}
	for k := range a.keptModelKeys {
		for _, f := range a.oldState(k).Fields {
			a.oldFieldKeys[fieldKey{k.App, k.Model, f.Name}] = true
		}
		for _, f := range a.to.Models[k].Fields {
			a.newFieldKeys[fieldKey{k.App, k.Model, f.Name}] = true
		}
	}
}

func fieldKeysMinus(a, b map[fieldKey]bool) []fieldKey {
	var out []fieldKey
	for k := range a {
		if !b[k] {
			out = append(out, k)
		}
	}
	sortFieldKeys(out)
	return out
}

func fieldKeysAnd(a, b map[fieldKey]bool) []fieldKey {
	var out []fieldKey
	for k := range a {
		if b[k] {
			out = append(out, k)
		}
	}
	sortFieldKeys(out)
	return out
}

// django: autodetector.py MigrationAutodetector.add_operation
func (a *Autodetector) addOperation(app string, op m.Operation, deps []opDep, beginning bool) {
	if _, ok := a.generated[app]; !ok {
		a.generatedApps = append(a.generatedApps, app)
	}
	g := &genOp{op: op, deps: deps}
	if beginning {
		a.generated[app] = append([]*genOp{g}, a.generated[app]...)
	} else {
		a.generated[app] = append(a.generated[app], g)
	}
}

// referencingFields returns the fields of other models that point at key.
//
// django: state.py ProjectState.relations[model]: fields of any model
// that reference key, keyed by the referencing model.
func referencingFields(s *m.ProjectState, key m.ModelKey) []m.FieldReference {
	return m.GetReferences(s, key, "")
}

// django: autodetector.py MigrationAutodetector._get_dependencies_for_foreign_key
func dependenciesForForeignKey(app string, f m.Field) []opDep {
	target := f.ForeignKey.Target(app)
	return []opDep{modelDep{App: target.App, Model: target.Model, Type: modelCreated}}
}

// dependenciesForConstraint returns the dependencies of adding c to the
// model of app: a table-level foreign key needs the model it points at.
// Django has no such constraint, so it has no counterpart for this.
func dependenciesForConstraint(app string, c m.Constraint) []opDep {
	fk, ok := c.(*m.ForeignKeyConstraint)
	if !ok {
		return nil
	}
	target := fk.Target(app)
	return []opDep{modelDep{App: target.App, Model: target.Model, Type: modelCreated}}
}

// django: autodetector.py MigrationAutodetector._get_dependencies_for_model
func (a *Autodetector) dependenciesForModel(k m.ModelKey) []opDep {
	var deps []opDep
	for _, f := range a.to.Models[k].Fields {
		if f.Field.IsRelation() {
			deps = append(deps, dependenciesForForeignKey(k.App, f.Field)...)
		}
	}
	return deps
}

// django: autodetector.py MigrationAutodetector.generate_renamed_models
func (a *Autodetector) generateRenamedModels() error {
	a.renamedModels = map[m.ModelKey]string{}
	a.renamedModelsRel = map[string]string{}
	for _, k := range difference(a.newModelKeys, a.oldModelKeys).sorted() {
		ms := a.to.Models[k]
		def := onlyRelationAgnosticFields(ms.Fields)
		for _, rk := range difference(a.oldModelKeys, a.newModelKeys).sorted() {
			if rk.App != k.App {
				continue
			}
			rms := a.from.Models[rk]
			if !fieldDefsEqual(def, onlyRelationAgnosticFields(rms.Fields)) {
				continue
			}
			ok, err := a.q.AskRenameModel(rms, ms)
			if err != nil {
				return err
			}
			if !ok {
				continue
			}
			var deps []opDep
			for _, f := range ms.Fields {
				if f.Field.IsRelation() {
					deps = append(deps, dependenciesForForeignKey(k.App, f.Field)...)
				}
			}
			for _, ref := range referencingFields(a.to, k) {
				deps = append(deps, modelDep{App: ref.Model.App, Model: ref.Model.Model, Type: modelCreated})
			}
			a.addOperation(k.App, &m.RenameModel{OldName: rms.Name, NewName: ms.Name}, deps, false)
			a.renamedModels[k] = rk.Model
			a.renamedModelsRel[rms.App+"."+rms.NameLower()] = ms.App + "." + ms.NameLower()
			delete(a.oldModelKeys, rk)
			a.oldModelKeys[k] = true
			break
		}
	}
	return nil
}

// django: autodetector.py MigrationAutodetector.generate_created_models
func (a *Autodetector) generateCreatedModels() error {
	oldKeys := union(a.oldModelKeys, a.oldUnmanagedKeys)
	added := difference(a.newModelKeys, oldKeys).sorted()
	addedUnmanaged := difference(a.newUnmanagedKeys, oldKeys).sorted()
	slices.Reverse(added)
	slices.Reverse(addedUnmanaged)
	all := append(added, addedUnmanaged...)
	for _, k := range all {
		ms := a.to.Models[k]
		related := map[string]m.Field{}
		var pkRels []m.ModelKey
		for _, f := range ms.Fields {
			if !f.Field.IsRelation() {
				continue
			}
			if f.Field.PrimaryKey {
				// A foreign key that is part of the primary key stays in
				// CreateModel. Django tracks one such field; gorm's join
				// tables have a composite primary key of two.
				pkRels = append(pkRels, f.Field.ForeignKey.Target(k.App))
			} else {
				related[f.Name] = f.Field
			}
		}
		deps := []opDep{modelDep{App: k.App, Model: k.Model, Type: modelDeleted}}
		for _, t := range pkRels {
			deps = append(deps, modelDep{App: t.App, Model: t.Model, Type: modelCreated})
		}
		opts := ms.Options.Clone()
		indexes, constraints, uniqueTogether := opts.Indexes, opts.Constraints, opts.UniqueTogether
		opts.Indexes, opts.Constraints, opts.UniqueTogether = nil, nil, nil
		var fields m.Fields
		for _, f := range ms.Fields {
			if _, rel := related[f.Name]; !rel {
				fields = append(fields, m.NamedField{Name: f.Name, Field: f.Field.Clone()})
			}
		}
		a.addOperation(k.App, &m.CreateModel{Name: ms.Name, Table: ms.Table, Fields: fields, Options: opts}, deps, true)
		if !ms.Options.IsManaged() {
			continue
		}
		relatedNames := slices.Sorted(maps.Keys(related))
		for _, name := range relatedNames {
			f := related[name]
			d := dependenciesForForeignKey(k.App, f)
			d = append(d, modelDep{App: k.App, Model: k.Model, Type: modelCreated})
			a.addOperation(k.App, &m.AddField{ModelName: k.Model, Name: name, Field: f.Clone()}, d, false)
		}
		var relatedDeps []opDep
		for _, name := range relatedNames {
			relatedDeps = append(relatedDeps, fieldDep{App: k.App, Model: k.Model, Field: name, Type: fieldCreated})
		}
		relatedDeps = append(relatedDeps, modelDep{App: k.App, Model: k.Model, Type: modelCreated})
		for _, ix := range indexes {
			a.addOperation(k.App, &m.AddIndex{ModelName: k.Model, Index: ix}, relatedDeps, false)
		}
		for _, c := range constraints {
			deps := append(slices.Clone(relatedDeps), dependenciesForConstraint(k.App, c)...)
			a.addOperation(k.App, &m.AddConstraint{ModelName: k.Model, Constraint: c}, deps, false)
		}
		if len(uniqueTogether) > 0 {
			a.addOperation(k.App, &m.AlterUniqueTogether{Name: k.Model, UniqueTogether: uniqueTogether}, relatedDeps, false)
		}
	}
	return nil
}

// django: autodetector.py MigrationAutodetector.generate_deleted_models
func (a *Autodetector) generateDeletedModels() error {
	newKeys := union(a.newModelKeys, a.newUnmanagedKeys)
	all := append(difference(a.oldModelKeys, newKeys).sorted(), difference(a.oldUnmanagedKeys, newKeys).sorted()...)
	for _, k := range all {
		ms := a.from.Models[k]
		var related []string
		for _, f := range ms.Fields {
			if f.Field.IsRelation() {
				related = append(related, f.Name)
			}
		}
		slices.Sort(related)
		if len(ms.Options.UniqueTogether) > 0 {
			a.addOperation(k.App, &m.AlterUniqueTogether{Name: k.Model}, nil, false)
		}
		for _, ix := range ms.Options.Indexes {
			a.addOperation(k.App, &m.RemoveIndex{ModelName: k.Model, Name: ix.Name}, nil, false)
		}
		for _, c := range ms.Options.Constraints {
			a.addOperation(k.App, &m.RemoveConstraint{ModelName: k.Model, Name: c.ConstraintName()}, nil, false)
		}
		for _, name := range related {
			a.addOperation(k.App, &m.RemoveField{ModelName: k.Model, Name: name},
				[]opDep{fieldDep{App: k.App, Model: k.Model, Field: name, Type: indexOrConstraintRemoved}}, false)
		}
		var deps []opDep
		for _, ref := range referencingFields(a.from, k) {
			deps = append(deps,
				fieldDep{App: ref.Model.App, Model: ref.Model.Model, Field: ref.Name, Type: fieldRemoved},
				fieldDep{App: ref.Model.App, Model: ref.Model.Model, Field: ref.Name, Type: fieldAltered})
		}
		for _, name := range related {
			deps = append(deps, fieldDep{App: k.App, Model: k.Model, Field: name, Type: fieldRemoved})
		}
		a.addOperation(k.App, &m.DeleteModel{Name: ms.Name}, dedupDeps(deps), false)
	}
	return nil
}

func dedupDeps(deps []opDep) []opDep {
	seen := map[opDep]bool{}
	var out []opDep
	for _, d := range deps {
		if !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	return out
}

// django: autodetector.py MigrationAutodetector.create_renamed_fields
func (a *Autodetector) createRenamedFields() error {
	a.renamedOps = nil
	oldFieldKeys := map[fieldKey]bool{}
	for k := range a.oldFieldKeys {
		oldFieldKeys[k] = true
	}
	for _, nk := range fieldKeysMinus(a.newFieldKeys, oldFieldKeys) {
		mk := m.ModelKey{App: nk.App, Model: nk.Model}
		oldMS := a.oldState(mk)
		newMS := a.to.Models[mk]
		field, _ := newMS.Fields.Get(nk.Field)
		fieldDec := qualifyRelationTarget(field.Deconstruct(), nk.App)
		for _, rk := range fieldKeysMinus(oldFieldKeys, a.newFieldKeys) {
			if rk.App != nk.App || rk.Model != nk.Model {
				continue
			}
			oldField, _ := oldMS.Fields.Get(rk.Field)
			oldDec := qualifyRelationTarget(oldField.Deconstruct(), rk.App)
			if field.IsRelation() {
				for i, kv := range oldDec {
					if kv.Key != m.AttrForeignKey {
						continue
					}
					fk := *kv.Value.(*m.ForeignKey)
					// renamedModelsRel is keyed by the
					// qualified, lower-cased name, which is
					// how a deconstruction spells it.
					nt, ok := a.renamedModelsRel[fk.To]
					if !ok {
						continue
					}
					fk.To = nt
					oldDec[i].Value = &fk
				}
			}
			if !kvEqual(oldDec, fieldDec) {
				continue
			}
			ok, err := a.q.AskRename(nk.Model, rk.Field, nk.Field, field)
			if err != nil {
				return err
			}
			if !ok {
				continue
			}
			a.renamedOps = append(a.renamedOps, renamedOp{
				remApp: rk.App, remModel: rk.Model, remField: rk.Field,
				app: nk.App, model: nk.Model, field: field, fieldName: nk.Field,
			})
			delete(oldFieldKeys, rk)
			oldFieldKeys[nk] = true
			a.renamedFields[nk] = rk.Field
			break
		}
	}
	return nil
}

// django: autodetector.py MigrationAutodetector.generate_renamed_fields
func (a *Autodetector) generateRenamedFields() error {
	for _, r := range a.renamedOps {
		// gormgate has no db_column, so the AlterField Django emits for a
		// changed db_column never applies.
		a.addOperation(r.app, &m.RenameField{ModelName: r.model, OldName: r.remField, NewName: r.fieldName}, nil, false)
		delete(a.oldFieldKeys, fieldKey{r.remApp, r.remModel, r.remField})
		a.oldFieldKeys[fieldKey{r.app, r.model, r.fieldName}] = true
	}
	return nil
}

// django: autodetector.py MigrationAutodetector.generate_added_fields
func (a *Autodetector) generateAddedFields() error {
	for _, k := range fieldKeysMinus(a.newFieldKeys, a.oldFieldKeys) {
		if err := a.generateAddedField(k.App, k.Model, k.Field); err != nil {
			return err
		}
	}
	return nil
}

// django: autodetector.py MigrationAutodetector._generate_added_field
func (a *Autodetector) generateAddedField(app, model, name string) error {
	field, _ := a.to.Models[m.ModelKey{App: app, Model: model}].Fields.Get(name)
	deps := []opDep{fieldDep{App: app, Model: model, Field: name, Type: fieldRemoved}}
	if field.IsRelation() {
		deps = append(deps, dependenciesForForeignKey(app, field)...)
	}
	preserveDefault := field.Null || field.HasDefault() || field.DBDefault != nil ||
		(field.Type == m.Time && field.AutoNow) || field.AutoIncrement
	if !preserveDefault {
		field = field.Clone()
		var def any
		var err error
		if field.Type == m.Time && field.AutoNowAdd {
			def, err = a.q.AskAutoNowAddAddition(name, model)
		} else {
			def, err = a.q.AskNotNullAddition(name, model)
		}
		if err != nil {
			return err
		}
		if def == nil {
			def = &m.GoExpr{Source: "nil"}
		}
		field.Default = def
	}
	if field.Unique && field.HasDefault() && isCallableDefault(field.Default) {
		if err := a.q.AskUniqueCallableDefaultAddition(name, model); err != nil {
			return err
		}
	}
	op := &m.AddField{ModelName: model, Name: name, Field: field}
	if !preserveDefault {
		op.PreserveDefault = m.Ptr(false)
	}
	a.addOperation(app, op, deps, false)
	return nil
}

var identPath = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)*$`)

// isCallableDefault reports whether a default is a function, including a
// prompt-entered expression that names a function (e.g. time.Now).
func isCallableDefault(v any) bool {
	if v == nil {
		return false
	}
	if e, ok := v.(*m.GoExpr); ok {
		return identPath.MatchString(e.Source) && e.Source != "nil" && e.Source != "true" && e.Source != "false"
	}
	return reflect.TypeOf(v).Kind() == reflect.Func
}

// django: autodetector.py MigrationAutodetector.generate_removed_fields
func (a *Autodetector) generateRemovedFields() error {
	for _, k := range fieldKeysMinus(a.oldFieldKeys, a.newFieldKeys) {
		a.generateRemovedField(k.App, k.Model, k.Field)
	}
	return nil
}

func (a *Autodetector) generateRemovedField(app, model, name string) {
	a.addOperation(app, &m.RemoveField{ModelName: model, Name: name}, []opDep{
		fieldDep{App: app, Model: model, Field: name, Type: uniqueTogetherAltered},
		fieldDep{App: app, Model: model, Field: name, Type: indexOrConstraintRemoved},
	}, false)
}

// django: autodetector.py MigrationAutodetector.generate_altered_fields
func (a *Autodetector) generateAlteredFields() error {
	for _, k := range fieldKeysAnd(a.oldFieldKeys, a.newFieldKeys) {
		mk := m.ModelKey{App: k.App, Model: k.Model}
		oldFieldName := k.Field
		if n, ok := a.renamedFields[k]; ok {
			oldFieldName = n
		}
		oldField, _ := a.oldState(mk).Fields.Get(oldFieldName)
		newField, _ := a.to.Models[mk].Fields.Get(k.Field)
		newField = newField.Clone()
		var deps []opDep
		if newField.IsRelation() {
			renameKey := newField.ForeignKey.Target(k.App)
			if _, ok := a.renamedModels[renameKey]; ok && oldField.IsRelation() {
				newField.ForeignKey.To = oldField.ForeignKey.To
			}
			if tf := newField.ForeignKey.ToField; tf != "" {
				if _, ok := a.renamedFields[fieldKey{renameKey.App, renameKey.Model, tf}]; ok && oldField.IsRelation() {
					newField.ForeignKey.To = oldField.ForeignKey.To
					newField.ForeignKey.ToField = oldField.ForeignKey.ToField
				}
			}
			deps = append(deps, dependenciesForForeignKey(k.App, newField)...)
		}
		oldDec := qualifyRelationTarget(oldField.Deconstruct(), k.App)
		newDec := qualifyRelationTarget(newField.Deconstruct(), k.App)
		if kvEqual(oldDec, newDec) || oldFieldName != k.Field {
			continue
		}
		preserveDefault := true
		field := newField
		if oldField.Null && !newField.Null && !newField.HasDefault() && newField.DBDefault == nil {
			field = newField.Clone()
			def, err := a.q.AskNotNullAlteration(k.Field, k.Model)
			if err != nil {
				return err
			}
			if def == nil {
				def = &m.GoExpr{Source: "nil"}
			}
			if def != questioner.NotProvided {
				field.Default = def
				preserveDefault = false
			}
		}
		op := &m.AlterField{ModelName: k.Model, Name: k.Field, Field: field}
		if !preserveDefault {
			op.PreserveDefault = m.Ptr(false)
		}
		a.addOperation(k.App, op, deps, false)
	}
	return nil
}

// indexIgnoringName returns ix with its name cleared, so that two indexes
// can be compared on their definition alone.
func indexIgnoringName(ix m.Index) m.Index {
	ix = ix.Clone()
	ix.Name = ""
	return ix
}

func containsIndex(list []m.Index, ix m.Index) bool {
	return slices.ContainsFunc(list, func(x m.Index) bool { return x.Equal(ix) })
}

// django: autodetector.py MigrationAutodetector.create_altered_indexes
func (a *Autodetector) createAlteredIndexes() error {
	for _, k := range a.keptModelKeys.sorted() {
		oldIx := a.oldState(k).Options.Indexes
		newIx := a.to.Models[k].Options.Indexes
		var added, removed []m.Index
		for _, ix := range newIx {
			if !containsIndex(oldIx, ix) {
				added = append(added, ix)
			}
		}
		for _, ix := range oldIx {
			if !containsIndex(newIx, ix) {
				removed = append(removed, ix)
			}
		}
		var renamed []renamedIndex
		var removeFromAdded, removeFromRemoved []m.Index
		for _, nix := range added {
			for _, oix := range removed {
				if indexIgnoringName(nix).Equal(indexIgnoringName(oix)) && nix.Name != oix.Name {
					renamed = append(renamed, renamedIndex{old: oix.Name, new: nix.Name})
					removeFromAdded = append(removeFromAdded, nix)
					removeFromRemoved = append(removeFromRemoved, oix)
				}
			}
		}
		var finalAdded, finalRemoved []m.Index
		for _, ix := range added {
			if !containsIndex(removeFromAdded, ix) {
				finalAdded = append(finalAdded, ix)
			}
		}
		for _, ix := range removed {
			if !containsIndex(removeFromRemoved, ix) {
				finalRemoved = append(finalRemoved, ix)
			}
		}
		a.altIndexes = append(a.altIndexes, alteredIndexes{key: k, added: finalAdded, removed: finalRemoved, renamed: renamed})
	}
	return nil
}

func (a *Autodetector) generateAddedIndexes() error {
	for _, ai := range a.altIndexes {
		deps := a.dependenciesForModel(ai.key)
		for _, ix := range ai.added {
			a.addOperation(ai.key.App, &m.AddIndex{ModelName: ai.key.Model, Index: ix}, deps, false)
		}
	}
	return nil
}

func (a *Autodetector) generateRemovedIndexes() error {
	for _, ai := range a.altIndexes {
		for _, ix := range ai.removed {
			a.addOperation(ai.key.App, &m.RemoveIndex{ModelName: ai.key.Model, Name: ix.Name}, nil, false)
		}
	}
	return nil
}

func (a *Autodetector) generateRenamedIndexes() error {
	for _, ai := range a.altIndexes {
		for _, r := range ai.renamed {
			a.addOperation(ai.key.App, &m.RenameIndex{ModelName: ai.key.Model, NewName: r.new, OldName: r.old}, nil, false)
		}
	}
	return nil
}

func containsConstraint(list []m.Constraint, c m.Constraint) bool {
	return slices.ContainsFunc(list, func(x m.Constraint) bool { return m.ConstraintEqual(x, c) })
}

// django: autodetector.py MigrationAutodetector.create_altered_constraints
func (a *Autodetector) createAlteredConstraints() error {
	for _, k := range a.keptModelKeys.sorted() {
		oldC := a.oldState(k).Options.Constraints
		newC := a.to.Models[k].Options.Constraints
		var altered []m.Constraint
		alteredNames := map[string]bool{}
		for _, oc := range oldC {
			for _, nc := range newC {
				if !m.ConstraintEqual(oc, nc) && oc.ConstraintName() == nc.ConstraintName() && m.ConstraintEqualIgnoringNonDB(oc, nc) {
					altered = append(altered, nc)
					alteredNames[nc.ConstraintName()] = true
				}
			}
		}
		var added, removed []m.Constraint
		for _, c := range newC {
			if !containsConstraint(oldC, c) && !alteredNames[c.ConstraintName()] {
				added = append(added, c)
			}
		}
		for _, c := range oldC {
			if !containsConstraint(newC, c) && !alteredNames[c.ConstraintName()] {
				removed = append(removed, c)
			}
		}
		a.altConstraints = append(a.altConstraints, alteredConstraints{key: k, added: added, removed: removed, altered: altered})
	}
	return nil
}

func (a *Autodetector) generateAddedConstraints() error {
	for _, ac := range a.altConstraints {
		deps := a.dependenciesForModel(ac.key)
		for _, c := range ac.added {
			a.addOperation(ac.key.App, &m.AddConstraint{ModelName: ac.key.Model, Constraint: c},
				append(slices.Clone(deps), dependenciesForConstraint(ac.key.App, c)...), false)
		}
	}
	return nil
}

func (a *Autodetector) generateRemovedConstraints() error {
	for _, ac := range a.altConstraints {
		for _, c := range ac.removed {
			a.addOperation(ac.key.App, &m.RemoveConstraint{ModelName: ac.key.Model, Name: c.ConstraintName()}, nil, false)
		}
	}
	return nil
}

func (a *Autodetector) generateAlteredConstraints() error {
	for _, ac := range a.altConstraints {
		deps := a.dependenciesForModel(ac.key)
		for _, c := range ac.altered {
			a.addOperation(ac.key.App, &m.AlterConstraint{ModelName: ac.key.Model, Name: c.ConstraintName(), Constraint: c}, deps, false)
		}
	}
	return nil
}

// togetherChange is the before and after of one model's unique_together,
// each as a set of comparable keys.
type togetherChange struct {
	old, want map[string][]string
	key       m.ModelKey
	deps      []opDep
}

func togetherToSet(t [][]string) map[string][]string {
	s := map[string][]string{}
	for _, x := range t {
		s[strings.Join(x, "\x00")] = x
	}
	return s
}

func setIntersect(a, b map[string][]string) map[string][]string {
	out := map[string][]string{}
	for k, v := range a {
		if _, ok := b[k]; ok {
			out[k] = v
		}
	}
	return out
}

func setEqual(a, b map[string][]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if _, ok := b[k]; !ok {
			return false
		}
	}
	return true
}

// setToTogether renders a set as a sorted list for a deterministic
// operation argument.
func setToTogether(s map[string][]string) [][]string {
	keys := slices.Sorted(maps.Keys(s))
	out := make([][]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, s[k])
	}
	return out
}

// django: autodetector.py MigrationAutodetector._get_altered_foo_together_operations
func (a *Autodetector) alteredUniqueTogether() []togetherChange {
	var out []togetherChange
	for _, k := range a.keptModelKeys.sorted() {
		oldMS := a.oldState(k)
		newMS := a.to.Models[k]
		oldVal := map[string][]string{}
		for _, t := range oldMS.Options.UniqueTogether {
			renamed := make([]string, len(t))
			for i, n := range t {
				if rn, ok := a.renamedFieldByOld(k, n); ok {
					renamed[i] = rn
				} else {
					renamed[i] = n
				}
			}
			oldVal[strings.Join(renamed, "\x00")] = renamed
		}
		newVal := togetherToSet(newMS.Options.UniqueTogether)
		if setEqual(oldVal, newVal) {
			continue
		}
		var deps []opDep
		for _, key := range slices.Sorted(maps.Keys(newVal)) {
			for _, fname := range newVal[key] {
				if f, ok := newMS.Fields.Get(fname); ok && f.IsRelation() {
					deps = append(deps, dependenciesForForeignKey(k.App, f)...)
				}
			}
		}
		out = append(out, togetherChange{old: oldVal, want: newVal, key: k, deps: deps})
	}
	return out
}

// renamedFieldByOld looks n up in the renamed-field map, which is keyed by
// the new field name. The lookup therefore only hits when a field was
// renamed to a name some other field used before; Django keys it the same
// way, and the detected changes depend on that.
func (a *Autodetector) renamedFieldByOld(k m.ModelKey, n string) (string, bool) {
	v, ok := a.renamedFields[fieldKey{k.App, k.Model, n}]
	return v, ok
}

// django: autodetector.py MigrationAutodetector._generate_removed_altered_foo_together
func (a *Autodetector) generateRemovedAlteredUniqueTogether() error {
	for _, c := range a.alteredUniqueTogether() {
		removal := setIntersect(c.want, c.old)
		if len(removal) > 0 || len(c.old) > 0 {
			a.addOperation(c.key.App, &m.AlterUniqueTogether{Name: c.key.Model, UniqueTogether: setToTogether(removal)}, c.deps, false)
		}
	}
	return nil
}

// django: autodetector.py MigrationAutodetector._generate_altered_foo_together
func (a *Autodetector) generateAlteredUniqueTogether() error {
	for _, c := range a.alteredUniqueTogether() {
		removal := setIntersect(c.want, c.old)
		if !setEqual(c.want, removal) {
			a.addOperation(c.key.App, &m.AlterUniqueTogether{Name: c.key.Model, UniqueTogether: setToTogether(c.want)}, c.deps, false)
		}
	}
	return nil
}

// django: autodetector.py MigrationAutodetector.generate_altered_db_table
func (a *Autodetector) generateAlteredDBTable() error {
	for _, k := range union(a.keptModelKeys, a.keptUnmanaged).sorted() {
		oldMS, newMS := a.oldState(k), a.to.Models[k]
		if oldMS.Table != newMS.Table {
			a.addOperation(k.App, &m.AlterModelTable{Name: k.Model, Table: newMS.Table}, nil, false)
		}
	}
	return nil
}

// django: autodetector.py MigrationAutodetector.generate_altered_db_table_comment
func (a *Autodetector) generateAlteredDBTableComment() error {
	for _, k := range union(a.keptModelKeys, a.keptUnmanaged).sorted() {
		oldMS, newMS := a.oldState(k), a.to.Models[k]
		if oldMS.Options.DBTableComment != newMS.Options.DBTableComment {
			a.addOperation(k.App, &m.AlterModelTableComment{Name: k.Model, TableComment: newMS.Options.DBTableComment}, nil, false)
		}
	}
	return nil
}

func boolPtrEqual(a, b *bool) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// django: autodetector.py MigrationAutodetector.generate_altered_options
func (a *Autodetector) generateAlteredOptions() error {
	check := union(a.keptModelKeys, a.keptUnmanaged, intersect(a.oldUnmanagedKeys, a.newModelKeys), intersect(a.oldModelKeys, a.newUnmanagedKeys))
	for _, k := range check.sorted() {
		oldMS, newMS := a.oldState(k), a.to.Models[k]
		if !boolPtrEqual(oldMS.Options.Managed, newMS.Options.Managed) {
			var managed *bool
			if newMS.Options.Managed != nil {
				managed = m.Ptr(*newMS.Options.Managed)
			}
			a.addOperation(k.App, &m.AlterModelOptions{Name: k.Model, Managed: managed}, nil, false)
		}
	}
	return nil
}

// topoSorter orders generated operations so that each one follows the
// operations it depends on. It emits whole ready sets at a time, in the
// order the nodes were added, which is the order the autodetector's output
// depends on.
//
// django: Python's graphlib.TopologicalSorter
type topoSorter struct {
	// added lists the nodes in the order they were added.
	added []*genOp
	info  map[*genOp]*topoInfo
}

type topoInfo struct {
	npred      int
	successors []*genOp
}

func (t *topoSorter) get(n *genOp) *topoInfo {
	if i, ok := t.info[n]; ok {
		return i
	}
	i := &topoInfo{}
	t.info[n] = i
	t.added = append(t.added, n)
	return i
}

func (t *topoSorter) add(n *genOp, preds ...*genOp) {
	ni := t.get(n)
	ni.npred += len(preds)
	for _, p := range preds {
		pi := t.get(p)
		pi.successors = append(pi.successors, n)
	}
}

// order returns the nodes in dependency order, or an error if they form a
// cycle.
func (t *topoSorter) order() ([]*genOp, error) {
	var ready []*genOp
	for _, n := range t.added {
		if t.info[n].npred == 0 {
			ready = append(ready, n)
		}
	}
	var out []*genOp
	for len(ready) > 0 {
		group := ready
		ready = nil
		out = append(out, group...)
		for _, n := range group {
			for _, s := range t.info[n].successors {
				si := t.info[s]
				si.npred--
				if si.npred == 0 {
					ready = append(ready, s)
				}
			}
		}
	}
	if len(out) != len(t.added) {
		return nil, fmt.Errorf("nodes are in a cycle")
	}
	return out, nil
}

// django: autodetector.py MigrationAutodetector._sort_migrations
func (a *Autodetector) sortMigrations() error {
	apps := slices.Sorted(slices.Values(a.generatedApps))
	for _, app := range apps {
		ops := a.generated[app]
		ts := &topoSorter{info: map[*genOp]*topoInfo{}}
		for _, op := range ops {
			ts.add(op)
			for _, d := range op.deps {
				if d.app() != app {
					continue
				}
				var preds []*genOp
				for _, x := range ops {
					if d.satisfiedBy(x.op) {
						preds = append(preds, x)
					}
				}
				ts.add(op, preds...)
			}
		}
		sorted, err := ts.order()
		if err != nil {
			return err
		}
		a.generated[app] = sorted
	}
	return nil
}

// django: autodetector.py MigrationAutodetector._build_migration_list
func (a *Autodetector) buildMigrationList(g *graph.Graph) error {
	a.migrations = map[string][]*m.Migration{}
	count := func() int {
		n := 0
		for _, ops := range a.generated {
			n += len(ops)
		}
		return n
	}
	numOps := count()
	chopMode := false
	for numOps > 0 {
		apps := slices.Sorted(slices.Values(a.generatedApps))
		for _, app := range apps {
			var chopped []*genOp
			deps := map[m.Key]bool{}
			var depOrder []m.Key
			addDep := func(k m.Key) {
				if !deps[k] {
					deps[k] = true
					depOrder = append(depOrder, k)
				}
			}
			for len(a.generated[app]) > 0 {
				op := a.generated[app][0]
				satisfied := true
				var opDeps []m.Key
				for _, d := range op.deps {
					depApp := d.app()
					if depApp == app {
						continue
					}
					for _, other := range a.generated[depApp] {
						if d.satisfiedBy(other.op) {
							satisfied = false
							break
						}
					}
					if !satisfied {
						break
					}
					if migs, ok := a.migrations[depApp]; ok {
						opDeps = append(opDeps, m.Key{App: depApp, Name: migs[len(migs)-1].Name})
					} else if chopMode {
						if g != nil && len(g.LeafNodes(depApp)) > 0 {
							opDeps = append(opDeps, g.LeafNodes(depApp)[0])
						} else {
							opDeps = append(opDeps, m.Key{App: depApp, Name: "__first__"})
						}
					} else {
						satisfied = false
					}
				}
				if !satisfied {
					break
				}
				chopped = append(chopped, op)
				for _, k := range opDeps {
					addDep(k)
				}
				a.generated[app] = a.generated[app][1:]
			}
			if len(depOrder) == 0 && len(chopped) == 0 {
				continue
			}
			if len(a.generated[app]) == 0 || chopMode {
				mig := &m.Migration{
					App:          app,
					Name:         "auto_" + strconv.Itoa(len(a.migrations[app])+1),
					Dependencies: depOrder,
					Initial:      m.Ptr(!a.existingApps[app]),
				}
				for _, c := range chopped {
					mig.Operations = append(mig.Operations, c.op)
				}
				if _, ok := a.migrations[app]; !ok {
					a.migrationApps = append(a.migrationApps, app)
				}
				a.migrations[app] = append(a.migrations[app], mig)
				chopMode = false
			} else {
				a.generated[app] = append(chopped, a.generated[app]...)
			}
		}
		newNum := count()
		if newNum == numOps {
			if !chopMode {
				chopMode = true
			} else {
				return fmt.Errorf("cannot resolve operation dependencies: %s", formatGenerated(a.generated))
			}
		}
		numOps = newNum
	}
	return nil
}

// django: autodetector.py MigrationAutodetector._optimize_migrations
func (a *Autodetector) optimizeMigrations() {
	for _, app := range a.migrationApps {
		migs := a.migrations[app]
		for i := 1; i < len(migs); i++ {
			migs[i].Dependencies = append(migs[i].Dependencies, m.Key{App: app, Name: migs[i-1].Name})
		}
	}
	for _, app := range a.migrationApps {
		for _, mig := range a.migrations[app] {
			seen := map[m.Key]bool{}
			var deps []m.Key
			for _, d := range mig.Dependencies {
				if !seen[d] {
					seen[d] = true
					deps = append(deps, d)
				}
			}
			mig.Dependencies = deps
			mig.Operations = optimizer.Optimize(mig.Operations, app)
		}
	}
}

var (
	squashedNumber = regexp.MustCompile(`.*_squashed_(\d+)`)
	leadingNumber  = regexp.MustCompile(`^\d+`)
)

// ParseNumber extracts the number from a migration name; for squashed
// migrations the second number. ok is false when there is none.
//
// django: autodetector.py MigrationAutodetector.parse_number
func ParseNumber(name string) (int, bool) {
	digits := ""
	if mm := squashedNumber.FindStringSubmatch(name); mm != nil {
		digits = mm[1]
	} else {
		digits = leadingNumber.FindString(name)
	}
	if digits == "" {
		return 0, false
	}
	// A number too long for an int is not a migration number: reporting
	// it as one would make the next migration's number overflow.
	n, err := strconv.Atoi(digits)
	if err != nil {
		return 0, false
	}
	return n, true
}

// ArrangeForGraph names the migrations and makes them extend the graph's
// leaf nodes.
//
// django: autodetector.py MigrationAutodetector.arrange_for_graph
func (a *Autodetector) ArrangeForGraph(changes map[string][]*m.Migration, g *graph.Graph, migrationName string) (map[string][]*m.Migration, error) {
	leaves := g.LeafNodes("")
	nameMap := map[m.Key]m.Key{}
	for _, app := range slices.Sorted(maps.Keys(changes)) {
		migs := changes[app]
		if len(migs) == 0 {
			continue
		}
		var appLeaf *m.Key
		for i := range leaves {
			if leaves[i].App == app {
				appLeaf = &leaves[i]
				break
			}
		}
		if appLeaf == nil && !a.q.AskInitial(app) {
			for _, mig := range migs {
				nameMap[m.Key{App: app, Name: mig.Name}] = m.Key{App: app, Name: "__first__"}
			}
			delete(changes, app)
			continue
		}
		next := 1
		if appLeaf != nil {
			n, _ := ParseNumber(appLeaf.Name)
			next = n + 1
		}
		for i, mig := range migs {
			if i == 0 && appLeaf != nil {
				mig.Dependencies = append(mig.Dependencies, *appLeaf)
			}
			parts := []string{fmt.Sprintf("%04d", next)}
			switch {
			case migrationName != "":
				parts = append(parts, migrationName)
			case i == 0 && appLeaf == nil:
				parts = append(parts, "initial")
			default:
				s := mig.SuggestName()
				parts = append(parts, s[:min(len(s), 100)])
			}
			newName := strings.Join(parts, "_")
			nameMap[m.Key{App: app, Name: mig.Name}] = m.Key{App: app, Name: newName}
			next++
			mig.Name = newName
		}
	}
	for _, migs := range changes {
		for _, mig := range migs {
			for i, d := range mig.Dependencies {
				if nk, ok := nameMap[d]; ok {
					mig.Dependencies[i] = nk
				}
			}
		}
	}
	return changes, nil
}

// trimChangesToApps keeps only the migrations of appLabels and of the apps
// depend on.
//
// django: autodetector.py MigrationAutodetector._trim_to_apps
func trimChangesToApps(changes map[string][]*m.Migration, appLabels []string) map[string][]*m.Migration {
	appDeps := map[string]map[string]bool{}
	for app, migs := range changes {
		for _, mig := range migs {
			for _, d := range mig.Dependencies {
				if appDeps[app] == nil {
					appDeps[app] = map[string]bool{}
				}
				appDeps[app][d.App] = true
			}
		}
	}
	required := map[string]bool{}
	for _, a := range appLabels {
		required[a] = true
	}
	for {
		before := len(required)
		for app := range required {
			for d := range appDeps[app] {
				required[d] = true
			}
		}
		if len(required) == before {
			break
		}
	}
	for app := range changes {
		if !required[app] {
			delete(changes, app)
		}
	}
	return changes
}

// formatGenerated renders the operations still waiting for a dependency,
// the way Django's %r of the generated_operations dict does: the operations
// themselves, not their addresses.
//
// django: autodetector.py MigrationAutodetector._build_migration_list
func formatGenerated(generated map[string][]*genOp) string {
	apps := slices.Sorted(maps.Keys(generated))
	var b strings.Builder
	b.WriteByte('{')
	for i, app := range apps {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%q: [", app)
		for j, g := range generated[app] {
			if j > 0 {
				b.WriteString(", ")
			}
			b.WriteString(m.FormatOperation(g.op))
		}
		b.WriteString("]")
	}
	b.WriteByte('}')
	return b.String()
}
