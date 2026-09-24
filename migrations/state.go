package migrations

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// ClickHouseTable holds the table engine settings used by the ClickHouse
// backend.
type ClickHouseTable struct {
	Engine      string // default "MergeTree()"
	OrderBy     string // default: the primary key columns
	PartitionBy string
	PrimaryKey  string
	Settings    string
}

// Options are the model Meta options a migration tracks.
type Options struct {
	// Managed set to false keeps gormgate from creating or altering the
	// model's table; nil means true.
	Managed *bool
	// DBTableComment is the comment on the model's table.
	DBTableComment string
	UniqueTogether [][]string
	Indexes        []Index
	Constraints    []Constraint
	// AutoCreated marks a many2many join table created implicitly by gorm.
	AutoCreated bool
	ClickHouse  *ClickHouseTable
}

// IsManaged reports whether gormgate manages the table.
func (o Options) IsManaged() bool { return o.Managed == nil || *o.Managed }

// Clone returns a deep copy of the options.
func (o Options) Clone() Options {
	if o.Managed != nil {
		v := *o.Managed
		o.Managed = &v
	}
	if o.UniqueTogether != nil {
		ut := make([][]string, len(o.UniqueTogether))
		for i, t := range o.UniqueTogether {
			ut[i] = slices.Clone(t)
		}
		o.UniqueTogether = ut
	}
	if o.Indexes != nil {
		ix := make([]Index, len(o.Indexes))
		for i, x := range o.Indexes {
			ix[i] = x.Clone()
		}
		o.Indexes = ix
	}
	if o.Constraints != nil {
		cs := make([]Constraint, len(o.Constraints))
		for i, c := range o.Constraints {
			cs[i] = c.cloneConstraint()
		}
		o.Constraints = cs
	}
	if o.ClickHouse != nil {
		ch := *o.ClickHouse
		o.ClickHouse = &ch
	}
	return o
}

// ModelState represents a model at a point in migration history.
//
// django: migrations/state.py ModelState
type ModelState struct {
	App     string
	Name    string
	Table   string
	Fields  Fields
	Options Options
}

// NameLower is the model name key.
func (m *ModelState) NameLower() string { return strings.ToLower(m.Name) }

// Key returns (app_label, name_lower).
func (m *ModelState) Key() ModelKey { return ModelKey{m.App, m.NameLower()} }

// GetField returns the named field or an error.
func (m *ModelState) GetField(name string) (Field, error) {
	f, ok := m.Fields.Get(name)
	if !ok {
		return Field{}, &FieldDoesNotExist{Msg: fmt.Sprintf("%s.%s has no field named '%s'", m.App, m.NameLower(), name)}
	}
	return f, nil
}

// GetIndexByName returns the named index.
//
// django: state.py ModelState.get_index_by_name
func (m *ModelState) GetIndexByName(name string) (Index, error) {
	for _, ix := range m.Options.Indexes {
		if ix.Name == name {
			return ix, nil
		}
	}
	return Index{}, fmt.Errorf("no index named %s on model %s", name, m.Name)
}

// GetConstraintByName returns the named constraint.
//
// django: state.py ModelState.get_constraint_by_name
func (m *ModelState) GetConstraintByName(name string) (Constraint, error) {
	for _, c := range m.Options.Constraints {
		if c.ConstraintName() == name {
			return c, nil
		}
	}
	return nil, fmt.Errorf("no constraint named %s on model %s", name, m.Name)
}

// Clone returns a deep copy.
func (m *ModelState) Clone() *ModelState {
	cp := &ModelState{App: m.App, Name: m.Name, Table: m.Table, Options: m.Options.Clone()}
	cp.Fields = make(Fields, len(m.Fields))
	for i, f := range m.Fields {
		cp.Fields[i] = NamedField{Name: f.Name, Field: f.Field.Clone()}
	}
	return cp
}

// Validate reports duplicate field names and unnamed indexes.
func (m *ModelState) Validate() error {
	seen := map[string]bool{}
	for _, f := range m.Fields {
		if seen[f.Name] {
			return fmt.Errorf("found duplicate value %s in CreateModel fields argument", f.Name)
		}
		seen[f.Name] = true
	}
	for _, ix := range m.Options.Indexes {
		if ix.Name == "" {
			return fmt.Errorf("indexes passed to ModelState require a name attribute. %#v doesn't have one", ix)
		}
	}
	for _, c := range m.Options.Constraints {
		fk, ok := c.(*ForeignKeyConstraint)
		if !ok {
			continue
		}
		if len(fk.Fields) == 0 {
			return fmt.Errorf("foreign key constraint %s on model %s has no fields", fk.Name, m.Name)
		}
		if len(fk.Fields) != len(fk.ToFields) {
			return fmt.Errorf("foreign key constraint %s on model %s constrains %d field(s) but references %d", fk.Name, m.Name, len(fk.Fields), len(fk.ToFields))
		}
		if fk.To == "" {
			return fmt.Errorf("foreign key constraint %s on model %s has no target model", fk.Name, m.Name)
		}
	}
	return nil
}

// Equal compares two model states. Fields are compared sorted by name, so
// two states declaring the same fields in a different order are equal.
//
// django: state.py ModelState.__eq__
func (m *ModelState) Equal(o *ModelState) bool {
	if m.App != o.App || m.Name != o.Name || m.Table != o.Table || len(m.Fields) != len(o.Fields) {
		return false
	}
	byName := func(x, y NamedField) int { return cmp.Compare(x.Name, y.Name) }
	a := slices.Clone(m.Fields)
	b := slices.Clone(o.Fields)
	slices.SortFunc(a, byName)
	slices.SortFunc(b, byName)
	for i := range a {
		if a[i].Name != b[i].Name || !a[i].Field.Equal(b[i].Field) {
			return false
		}
	}
	return optionsEqual(m.Options, o.Options)
}

func optionsEqual(a, b Options) bool {
	if a.IsManaged() != b.IsManaged() || a.DBTableComment != b.DBTableComment || a.AutoCreated != b.AutoCreated {
		return false
	}
	if !togetherEqual(a.UniqueTogether, b.UniqueTogether) {
		return false
	}
	if len(a.Indexes) != len(b.Indexes) || len(a.Constraints) != len(b.Constraints) {
		return false
	}
	for i := range a.Indexes {
		if !a.Indexes[i].Equal(b.Indexes[i]) {
			return false
		}
	}
	for i := range a.Constraints {
		if !ConstraintEqual(a.Constraints[i], b.Constraints[i]) {
			return false
		}
	}
	return deconstructEqual(deconstructStruct(a.ClickHouse), deconstructStruct(b.ClickHouse))
}

// containsTogether reports whether t holds the entry x.
func containsTogether(t [][]string, x []string) bool {
	return slices.ContainsFunc(t, func(y []string) bool { return slices.Equal(x, y) })
}

// dedupTogether drops repeated entries of a unique_together declaration, so
// that two declarations compare as the sets Django holds them in.
func dedupTogether(t [][]string) [][]string {
	var out [][]string
	for _, x := range t {
		if !containsTogether(out, x) {
			out = append(out, x)
		}
	}
	return out
}

// togetherEqual compares two unique_together declarations as sets: the order
// of the entries does not matter, the order of the columns inside one does.
func togetherEqual(a, b [][]string) bool {
	sa, sb := dedupTogether(a), dedupTogether(b)
	if len(sa) != len(sb) {
		return false
	}
	for _, x := range sa {
		if !containsTogether(sb, x) {
			return false
		}
	}
	return true
}

// ProjectState is the whole project's model state at one point in history.
//
// django: migrations/state.py ProjectState
type ProjectState struct {
	Models map[ModelKey]*ModelState
	// RealApps are apps without migrations; their models are provided by
	// RealModels (the current model definitions).
	RealApps   map[string]bool
	RealModels []*ModelState

	apps *Apps
}

// NewProjectState returns an empty state.
func NewProjectState() *ProjectState {
	return &ProjectState{Models: map[ModelKey]*ModelState{}, RealApps: map[string]bool{}}
}

// Clone returns a deep copy.
//
// django: state.py ProjectState.clone
func (s *ProjectState) Clone() *ProjectState {
	ns := &ProjectState{Models: make(map[ModelKey]*ModelState, len(s.Models)), RealApps: s.RealApps, RealModels: s.RealModels}
	for k, m := range s.Models {
		ns.Models[k] = m.Clone()
	}
	if s.apps != nil {
		// A state that has already been rendered hands its clone a
		// rendered registry too, so the clone need not render again.
		// An error here is not fatal: the clone is simply left
		// unrendered and reports it when something asks for Apps.
		ns.apps, _ = renderApps(ns)
	}
	return ns
}

// SortedKeys returns the model keys in (app, model) order.
func (s *ProjectState) SortedKeys() []ModelKey {
	return slices.SortedFunc(maps.Keys(s.Models), compareModelKeys)
}

func compareModelKeys(a, b ModelKey) int {
	return cmp.Or(cmp.Compare(a.App, b.App), cmp.Compare(a.Model, b.Model))
}

func sortKeys(keys []ModelKey) { slices.SortFunc(keys, compareModelKeys) }

func (s *ProjectState) invalidate() { s.apps = nil }

// Rendered reports whether the state's models have already been rendered
// into a model registry; if not, the next call that needs one builds it.
func (s *ProjectState) Rendered() bool { return s.apps != nil }

// Model returns the model state for key.
func (s *ProjectState) Model(app, name string) (*ModelState, error) {
	m, ok := s.Models[ModelKey{app, strings.ToLower(name)}]
	if !ok {
		return nil, &ModelNotFoundError{Msg: fmt.Sprintf("app '%s' doesn't have a '%s' model", app, name)}
	}
	return m, nil
}

// mustModel is Model for a caller that has already checked the model is
// there. It panics with a *ModelNotFoundError otherwise, which is a bug in
// gormgate; the command boundary reports it as an internal error.
func (s *ProjectState) mustModel(app, name string) *ModelState {
	m, err := s.Model(app, name)
	if err != nil {
		panic(err)
	}
	return m
}

// AddModel adds or replaces a model. Foreign keys naming a bare
// "ModelName" are rewritten to the canonical "app.ModelName" form.
func (s *ProjectState) AddModel(m *ModelState) {
	for i := range m.Fields {
		m.Fields[i].Field = qualifyForeignKey(m.Fields[i].Field, m.App)
	}
	qualifyConstraints(m.Options.Constraints, m.App)
	s.Models[m.Key()] = m
	s.invalidate()
}

// qualifyConstraints rewrites a bare "ModelName" target of a
// ForeignKeyConstraint to "app.ModelName", for the same reason
// qualifyForeignKey does it for a field.
func qualifyConstraints(cs []Constraint, app string) {
	for i, c := range cs {
		fk, ok := c.(*ForeignKeyConstraint)
		if !ok || fk.To == "" || strings.Contains(fk.To, ".") {
			continue
		}
		cp := fk.cloneConstraint().(*ForeignKeyConstraint)
		cp.To = app + "." + cp.To
		cs[i] = cp
	}
}

// qualifyForeignKey rewrites a bare "ModelName" foreign-key target to
// "app.ModelName". A ProjectState only ever holds the qualified form, so
// that a hand-written migration using the short spelling and a model
// definition using the long one deconstruct equal.
//
// django: migrations/utils.py resolve_relation
func qualifyForeignKey(f Field, app string) Field {
	if f.ForeignKey == nil || strings.Contains(f.ForeignKey.To, ".") {
		return f
	}
	fk := *f.ForeignKey
	fk.To = app + "." + fk.To
	f.ForeignKey = &fk
	return f
}

// RemoveModel deletes a model.
func (s *ProjectState) RemoveModel(app, name string) {
	delete(s.Models, ModelKey{app, strings.ToLower(name)})
	s.invalidate()
}

// RenameModel renames a model and repoints references to it.
//
// django: state.py ProjectState.rename_model
func (s *ProjectState) RenameModel(app, oldName, newName string) {
	oldKey := ModelKey{app, strings.ToLower(oldName)}
	renamed := s.Models[oldKey].Clone()
	renamed.Name = newName
	// The renamed model is added before the references are repointed so that
	// a self-referential foreign key on the renamed model is repointed too;
	// the old model is only removed afterwards.
	s.Models[renamed.Key()] = renamed
	newRef := app + "." + newName
	for _, k := range s.SortedKeys() {
		ms := s.Models[k]
		for i, c := range ms.Options.Constraints {
			fk, ok := c.(*ForeignKeyConstraint)
			if !ok || fk.Target(k.App) != oldKey {
				continue
			}
			cp := fk.cloneConstraint().(*ForeignKeyConstraint)
			cp.To = newRef
			ms.Options.Constraints[i] = cp
		}
	}
	for _, ref := range GetReferences(s, oldKey, "") {
		ms := s.Models[ref.Model]
		i := ms.Fields.Index(ref.Name)
		f := ms.Fields[i].Field.Clone()
		f.ForeignKey.To = newRef
		ms.Fields[i].Field = f
	}
	if renamed.Key() != oldKey {
		delete(s.Models, oldKey)
	}
	s.invalidate()
}

// AlterModelOptions sets the model's managed flag when set is true, and
// otherwise leaves it alone. Django's alter_model_options resets exactly the
// option keys the operation names; managed is the only one gormgate has.
//
// django: state.py ProjectState.alter_model_options
func (s *ProjectState) AlterModelOptions(app, name string, managed *bool, set bool) {
	m := s.mustModel(app, name)
	if set {
		m.Options.Managed = managed
	}
	s.invalidate()
}

// AlterTable sets the model's table.
func (s *ProjectState) AlterTable(app, name, table string) {
	s.mustModel(app, name).Table = table
	s.invalidate()
}

// AlterTableComment sets the table comment.
func (s *ProjectState) AlterTableComment(app, name, comment string) {
	s.mustModel(app, name).Options.DBTableComment = comment
	s.invalidate()
}

// AlterUniqueTogether replaces unique_together.
func (s *ProjectState) AlterUniqueTogether(app, name string, ut [][]string) {
	m := s.mustModel(app, name)
	m.Options.UniqueTogether = normalizeTogether(ut)
	s.invalidate()
}

// RemoveUniqueTogether drops one unique_together entry.
//
// django: state.py ProjectState.remove_model_options
func (s *ProjectState) RemoveUniqueTogether(app, name string, value []string) {
	m := s.mustModel(app, name)
	var out [][]string
	for _, t := range m.Options.UniqueTogether {
		if !slices.Equal(t, value) {
			out = append(out, t)
		}
	}
	m.Options.UniqueTogether = out
	s.invalidate()
}

// AddIndex appends an index.
func (s *ProjectState) AddIndex(app, name string, ix Index) {
	m := s.mustModel(app, name)
	m.Options.Indexes = append(slices.Clone(m.Options.Indexes), ix.Clone())
	s.invalidate()
}

// RemoveIndex removes the named index.
func (s *ProjectState) RemoveIndex(app, name, indexName string) {
	m := s.mustModel(app, name)
	var out []Index
	for _, ix := range m.Options.Indexes {
		if ix.Name != indexName {
			out = append(out, ix)
		}
	}
	m.Options.Indexes = out
	s.invalidate()
}

// RenameIndex renames an index.
func (s *ProjectState) RenameIndex(app, name, oldName, newName string) {
	m := s.mustModel(app, name)
	out := make([]Index, len(m.Options.Indexes))
	for i, ix := range m.Options.Indexes {
		ix = ix.Clone()
		if ix.Name == oldName {
			ix.Name = newName
		}
		out[i] = ix
	}
	m.Options.Indexes = out
	s.invalidate()
}

// AddConstraint appends a constraint.
func (s *ProjectState) AddConstraint(app, name string, c Constraint) {
	m := s.mustModel(app, name)
	m.Options.Constraints = append(slices.Clone(m.Options.Constraints), c.cloneConstraint())
	qualifyConstraints(m.Options.Constraints[len(m.Options.Constraints)-1:], app)
	s.invalidate()
}

// RemoveConstraint removes the named constraint.
func (s *ProjectState) RemoveConstraint(app, name, constraintName string) {
	m := s.mustModel(app, name)
	var out []Constraint
	for _, c := range m.Options.Constraints {
		if c.ConstraintName() != constraintName {
			out = append(out, c)
		}
	}
	m.Options.Constraints = out
	s.invalidate()
}

// AlterConstraint replaces the named constraint.
func (s *ProjectState) AlterConstraint(app, name, constraintName string, c Constraint) {
	m := s.mustModel(app, name)
	out := make([]Constraint, len(m.Options.Constraints))
	for i, x := range m.Options.Constraints {
		if x.ConstraintName() == constraintName {
			out[i] = c.cloneConstraint()
			qualifyConstraints(out[i:i+1], app)
		} else {
			out[i] = x
		}
	}
	m.Options.Constraints = out
	s.invalidate()
}

// AddField appends a field. A bare "ModelName" foreign-key target is
// rewritten to the canonical "app.ModelName" form.
//
// It panics with a *ModelNotFoundError when the app has no such model; the
// commands recover that at their boundary.
//
// django: state.py ProjectState.add_field
func (s *ProjectState) AddField(app, model, name string, f Field, preserveDefault bool) {
	f = qualifyForeignKey(f.Clone(), app)
	if !preserveDefault {
		f.Default = nil
	}
	m := s.mustModel(app, model)
	m.Fields = append(m.Fields, NamedField{Name: name, Field: f})
	s.invalidate()
}

// RemoveField removes a field.
//
// It panics with a *ModelNotFoundError when the app has no such model, and
// with a *FieldDoesNotExist when the model has no such field. RenameField
// returns that error instead; both are recovered at the command boundary and
// printed the way Django prints an unhandled exception, so the difference is
// not visible to the user.
//
// django: state.py ProjectState.remove_field
func (s *ProjectState) RemoveField(app, model, name string) {
	m := s.mustModel(app, model)
	i := m.Fields.Index(name)
	if i < 0 {
		panic(&FieldDoesNotExist{Msg: fmt.Sprintf("%s.%s has no field named '%s'", app, strings.ToLower(model), name)})
	}
	m.Fields = slices.Delete(slices.Clone(m.Fields), i, i+1)
	s.invalidate()
}

// AlterField replaces a field definition in place. A bare "ModelName"
// foreign-key target is rewritten to the canonical "app.ModelName" form.
//
// It panics with a *ModelNotFoundError when the app has no such model, and
// with a *FieldDoesNotExist when the model has no such field, the way
// RemoveField does.
//
// django: state.py ProjectState.alter_field
func (s *ProjectState) AlterField(app, model, name string, f Field, preserveDefault bool) {
	f = qualifyForeignKey(f.Clone(), app)
	if !preserveDefault {
		f.Default = nil
	}
	m := s.mustModel(app, model)
	i := m.Fields.Index(name)
	if i < 0 {
		panic(&FieldDoesNotExist{Msg: fmt.Sprintf("%s.%s has no field named '%s'", app, strings.ToLower(model), name)})
	}
	m.Fields = slices.Clone(m.Fields)
	m.Fields[i] = NamedField{Name: name, Field: f}
	s.invalidate()
}

// RenameField renames a field and every reference to it: unique_together,
// indexes, constraints and foreign keys targeting it.
//
// It returns a *FieldDoesNotExist when the model has no such field;
// RemoveField and AlterField panic with that error instead.
//
// django: state.py ProjectState.rename_field
func (s *ProjectState) RenameField(app, model, oldName, newName string) error {
	key := ModelKey{app, strings.ToLower(model)}
	m := s.Models[key]
	i := m.Fields.Index(oldName)
	if i < 0 {
		return &FieldDoesNotExist{Msg: fmt.Sprintf("%s.%s has no field named '%s'", app, key.Model, oldName)}
	}
	m.Fields = slices.Clone(m.Fields)
	m.Fields[i].Name = newName
	for j, t := range m.Options.UniqueTogether {
		m.Options.UniqueTogether[j] = renameIn(t, oldName, newName)
	}
	for j, ix := range m.Options.Indexes {
		ix = ix.Clone()
		for k := range ix.Fields {
			if ix.Fields[k].Column == oldName {
				ix.Fields[k].Column = newName
			}
		}
		ix.Include = renameIn(ix.Include, oldName, newName)
		m.Options.Indexes[j] = ix
	}
	for j, c := range m.Options.Constraints {
		m.Options.Constraints[j] = c.renameColumn(oldName, newName)
	}
	for _, ck := range s.SortedKeys() {
		cms := s.Models[ck]
		for i, c := range cms.Options.Constraints {
			fk, ok := c.(*ForeignKeyConstraint)
			if !ok || fk.Target(ck.App) != key {
				continue
			}
			cms.Options.Constraints[i] = fk.renameTargetColumn(oldName, newName)
		}
	}
	for _, ref := range GetReferences(s, key, oldName) {
		ms := s.Models[ref.Model]
		k := ms.Fields.Index(ref.Name)
		f := ms.Fields[k].Field.Clone()
		if f.ForeignKey.ToField == oldName {
			f.ForeignKey.ToField = newName
		}
		ms.Fields[k].Field = f
	}
	s.invalidate()
	return nil
}

func renameIn(xs []string, old, new string) []string {
	if xs == nil {
		return nil
	}
	out := make([]string, len(xs))
	for i, x := range xs {
		if x == old {
			x = new
		}
		out[i] = x
	}
	return out
}

func normalizeTogether(t [][]string) [][]string {
	if len(t) == 0 {
		return nil
	}
	out := make([][]string, len(t))
	for i, x := range t {
		out[i] = slices.Clone(x)
	}
	return out
}

// Equal compares two project states.
func (s *ProjectState) Equal(o *ProjectState) bool {
	return maps.EqualFunc(s.Models, o.Models, (*ModelState).Equal) && maps.Equal(s.RealApps, o.RealApps)
}

// FieldReference is one field that references a model (and optionally a
// specific field of it).
type FieldReference struct {
	Model ModelKey
	Name  string
	Field Field
}

// FieldReferences reports whether field (on model modelKey) references
// refModel, and refField when given.
//
// django: migrations/utils.py field_references
func FieldReferences(modelKey ModelKey, f Field, refModel ModelKey, refField string) bool {
	if f.ForeignKey == nil {
		return false
	}
	if f.ForeignKey.Target(modelKey.App) != refModel {
		return false
	}
	return refField == "" || f.ForeignKey.ToField == "" || f.ForeignKey.ToField == refField
}

// GetReferences lists fields in state referencing model (and field, when
// non-empty), in deterministic order.
//
// django: migrations/utils.py get_references
func GetReferences(s *ProjectState, model ModelKey, field string) []FieldReference {
	var out []FieldReference
	for _, k := range s.SortedKeys() {
		for _, f := range s.Models[k].Fields {
			if FieldReferences(k, f.Field, model, field) {
				out = append(out, FieldReference{Model: k, Name: f.Name, Field: f.Field})
			}
		}
	}
	return out
}

// FieldIsReferenced reports whether any model references the field.
//
// django: migrations/utils.py field_is_referenced
func FieldIsReferenced(s *ProjectState, model ModelKey, field string) bool {
	return len(GetReferences(s, model, field)) > 0
}
