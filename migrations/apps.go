package migrations

import (
	"errors"
	"fmt"
	"strings"
)

// Model is a ModelState rendered against a whole ProjectState, with its
// foreign keys resolved to the models and fields they point at. It is what
// operations and RunGo code see in place of the live Go struct.
type Model struct {
	// State is the model state this was rendered from.
	State *ModelState
	// App, Name and Table identify the model and its table.
	App, Name, Table string
	// Fields are the model's fields, in declaration order.
	Fields []*ModelField
	apps   *Apps
}

// ModelField is a field bound to the model it belongs to.
type ModelField struct {
	// Model is the model that declares the field.
	Model *Model
	// Name is the field name.
	Name string
	// Field is the field definition.
	Field Field
	// Column is the database column (identical to Name in gormgate).
	Column string
	// Remote and RemoteField are set for foreign keys.
	Remote      *Model
	RemoteField *ModelField
}

// Apps is the registry of rendered models of one ProjectState.
//
// django: migrations/state.py StateApps
type Apps struct {
	models map[ModelKey]*Model
	order  []ModelKey
}

// Apps renders the state's models into a registry. The result is kept until
// the state changes.
func (s *ProjectState) Apps() (*Apps, error) {
	if s.apps != nil {
		return s.apps, nil
	}
	a, err := renderApps(s)
	if err != nil {
		return nil, err
	}
	s.apps = a
	return a, nil
}

// MustApps is Apps for callers holding a state that is known to render,
// such as one an operation has just produced. It panics otherwise.
func (s *ProjectState) MustApps() *Apps {
	a, err := s.Apps()
	if err != nil {
		panic(err)
	}
	return a
}

// renderApps builds the registry and resolves every foreign key in it. A
// reference that cannot be resolved is not fatal on its own: they are all
// collected so that one render reports every broken relation at once, the way
// Django's system checks do.
//
// django: migrations/state.py StateApps.__init__
func renderApps(s *ProjectState) (*Apps, error) {
	a := &Apps{models: map[ModelKey]*Model{}}
	add := func(ms *ModelState) {
		m := &Model{State: ms, App: ms.App, Name: ms.Name, Table: ms.Table, apps: a}
		for _, f := range ms.Fields {
			m.Fields = append(m.Fields, &ModelField{Model: m, Name: f.Name, Field: f.Field, Column: f.Name})
		}
		a.models[ms.Key()] = m
	}
	for _, k := range s.SortedKeys() {
		add(s.Models[k])
	}
	for _, ms := range s.RealModels {
		if _, ok := a.models[ms.Key()]; !ok && s.RealApps[ms.App] {
			add(ms)
		}
	}
	for k := range a.models {
		a.order = append(a.order, k)
	}
	sortKeys(a.order)
	installed := map[string]bool{}
	for k := range a.models {
		installed[k.App] = true
	}
	for app := range s.RealApps {
		installed[app] = true
	}
	var errs []string
	for _, k := range a.order {
		m := a.models[k]
		for _, c := range m.State.Options.Constraints {
			fk, ok := c.(*ForeignKeyConstraint)
			if !ok {
				continue
			}
			target := fk.Target(m.App)
			remote, ok := a.models[target]
			if !ok {
				if installed[target.App] {
					errs = append(errs, fmt.Sprintf("The constraint %s.%s.%s was declared with a lazy reference to '%s', but app '%s' doesn't provide model '%s'.", m.App, m.Name, fk.Name, target, target.App, target.Model))
				} else {
					errs = append(errs, fmt.Sprintf("The constraint %s.%s.%s was declared with a lazy reference to '%s', but app '%s' isn't installed.", m.App, m.Name, fk.Name, target, target.App))
				}
				continue
			}
			for _, col := range fk.ToFields {
				if remote.Field(col) == nil {
					errs = append(errs, fmt.Sprintf("The constraint %s.%s.%s references '%s.%s', which does not exist.", m.App, m.Name, fk.Name, target, col))
				}
			}
		}
		for _, f := range m.Fields {
			fk := f.Field.ForeignKey
			if fk == nil {
				continue
			}
			target := fk.Target(m.App)
			remote, ok := a.models[target]
			if !ok {
				// django: core/checks/model_checks.py _check_lazy_references
				if installed[target.App] {
					errs = append(errs, fmt.Sprintf("The field %s.%s.%s was declared with a lazy reference to '%s', but app '%s' doesn't provide model '%s'.", m.App, m.Name, f.Name, target, target.App, target.Model))
				} else {
					errs = append(errs, fmt.Sprintf("The field %s.%s.%s was declared with a lazy reference to '%s', but app '%s' isn't installed.", m.App, m.Name, f.Name, target, target.App))
				}
				continue
			}
			f.Remote = remote
			toField := fk.ToField
			if toField == "" {
				pk := remote.PK()
				if len(pk) != 1 {
					errs = append(errs, fmt.Sprintf("The field %s.%s.%s references '%s', which has no single-column primary key.", m.App, m.Name, f.Name, target))
					continue
				}
				f.RemoteField = pk[0]
				continue
			}
			rf := remote.Field(toField)
			if rf == nil {
				errs = append(errs, fmt.Sprintf("The field %s.%s.%s references '%s.%s', which does not exist.", m.App, m.Name, f.Name, target, toField))
				continue
			}
			f.RemoteField = rf
		}
	}
	if len(errs) > 0 {
		// The messages keep Django's wording, so they are built as
		// display text and only become errors here. Joining them
		// prints one per line and gives the caller Unwrap() []error,
		// so it can still reach the individual problems.
		joined := make([]error, len(errs))
		for i, msg := range errs {
			joined[i] = errors.New(msg)
		}
		return nil, errors.Join(joined...)
	}
	return a, nil
}

// GetModel returns a rendered model by app label and model name. The name
// is matched case-insensitively.
//
// django: apps/registry.py Apps.get_model
func (a *Apps) GetModel(app, name string) (*Model, error) {
	m, ok := a.models[ModelKey{app, strings.ToLower(name)}]
	if !ok {
		return nil, &ModelNotFoundError{Msg: fmt.Sprintf("app '%s' doesn't have a '%s' model", app, name)}
	}
	return m, nil
}

// MustModel is GetModel for a model known to exist. It panics otherwise.
func (a *Apps) MustModel(app, name string) *Model {
	m, err := a.GetModel(app, name)
	if err != nil {
		panic(err)
	}
	return m
}

// Models returns all models in (app, model) order.
func (a *Apps) Models() []*Model {
	out := make([]*Model, len(a.order))
	for i, k := range a.order {
		out[i] = a.models[k]
	}
	return out
}

// Field returns the named field or nil.
func (m *Model) Field(name string) *ModelField {
	for _, f := range m.Fields {
		if f.Name == name {
			return f
		}
	}
	return nil
}

// PK returns the primary-key fields.
func (m *Model) PK() []*ModelField {
	var pk []*ModelField
	for _, f := range m.Fields {
		if f.Field.PrimaryKey {
			pk = append(pk, f)
		}
	}
	return pk
}

// Label returns "app.Name".
func (m *Model) Label() string { return m.App + "." + m.Name }

// Key returns the model key.
func (m *Model) Key() ModelKey { return ModelKey{m.App, strings.ToLower(m.Name)} }

// Options returns the model's Meta options.
func (m *Model) Options() Options { return m.State.Options }

// Managed reports whether gormgate may create and alter the model's table.
func (m *Model) Managed() bool { return m.State.Options.IsManaged() }

// RelatedFields returns every field in the registry that points at m,
// ordered by app, then model, then position within the model.
// Self-references are included.
//
// django: db/models/options.py Options.related_objects
func (m *Model) RelatedFields() []*ModelField {
	var out []*ModelField
	for _, other := range m.apps.Models() {
		for _, f := range other.Fields {
			if f.Remote == m {
				out = append(out, f)
			}
		}
	}
	return out
}

// Apps returns the registry the model belongs to.
func (m *Model) Apps() *Apps { return m.apps }

// NewModel renders state as a model in the same registry as origin, whose
// Fields the caller fills in. It exists for a backend that has to rebuild a
// table under a temporary name (SQLite): the copy stays able to resolve the
// targets of its table-level relations, which a bare Model literal could
// not.
func NewModel(origin *Model, state *ModelState) *Model {
	return &Model{State: state, App: state.App, Name: state.Name, Table: state.Table, apps: origin.apps}
}

// RelatedModel resolves the target of a table-level relation -- the To of a
// ForeignKeyConstraint, spelled "app_label.ModelName" or "ModelName" for a
// model of m's own app -- to the model it names.
func (m *Model) RelatedModel(to string) (*Model, error) {
	k := ResolveRelation(to, m.App)
	return m.apps.GetModel(k.App, k.Model)
}
