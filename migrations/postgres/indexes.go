package postgres

import (
	"fmt"
	"strings"

	m "github.com/ctolon/gormgate/migrations"
)

// AddIndexConcurrently creates an index with PostgreSQL's CREATE INDEX
// CONCURRENTLY syntax, which does not lock the table against writes. The
// statement cannot run inside a transaction, so the migration holding it
// must set Atomic: m.Ptr(false).
//
// django: contrib/postgres/operations.py AddIndexConcurrently
type AddIndexConcurrently struct {
	baseOp
	// ModelName is the model the index belongs to.
	ModelName string
	// Index is the index to create.
	Index m.Index
}

// Category reports the symbol makemigrations prints for this operation.
func (o *AddIndexConcurrently) Category() m.Category { return m.CategoryAddition }

// Atomic refuses a transaction of the operation's own, so that a migration
// that is not atomic does not open one around it.
//
// django: contrib/postgres/operations.py AddIndexConcurrently.atomic
func (o *AddIndexConcurrently) Atomic() *bool { return m.Ptr(false) }

// StateForwards adds the index to the model state, exactly as AddIndex
// does.
//
// django: operations/models.py AddIndex.state_forwards
func (o *AddIndexConcurrently) StateForwards(app string, s *m.ProjectState) error {
	if o.Index.Name == "" {
		return &m.ValueError{Msg: fmt.Sprintf("indexes passed to AddIndex operations require a name argument. %#v doesn't have one", o.Index)}
	}
	if _, err := s.Model(app, o.ModelName); err != nil {
		return err
	}
	s.AddIndex(app, o.ModelName, o.Index)
	return nil
}

// DatabaseForwards creates the index concurrently.
//
// django: contrib/postgres/operations.py AddIndexConcurrently.database_forwards
func (o *AddIndexConcurrently) DatabaseForwards(app string, ed m.SchemaEditor, _, to *m.ProjectState) error {
	return concurrentIndex(app, ed, to, o.ModelName, o, func(pg Editor, model *m.Model) error {
		return pg.AddIndexConcurrently(model, o.Index)
	})
}

// DatabaseBackwards drops the index concurrently.
//
// django: contrib/postgres/operations.py AddIndexConcurrently.database_backwards
func (o *AddIndexConcurrently) DatabaseBackwards(app string, ed m.SchemaEditor, from, _ *m.ProjectState) error {
	return concurrentIndex(app, ed, from, o.ModelName, o, func(pg Editor, model *m.Model) error {
		return pg.RemoveIndexConcurrently(model, o.Index)
	})
}

// ReferencesField reports whether the operation touches the named field.
func (o *AddIndexConcurrently) ReferencesField(model, _, app string) bool {
	return o.ReferencesModel(model, app)
}

// Describe is the line makemigrations and migrate --plan print. Django
// prints the index's fields here even for an index built from expressions,
// which is what AddIndex's own description branches on.
//
// django: contrib/postgres/operations.py AddIndexConcurrently.describe
func (o *AddIndexConcurrently) Describe() string {
	return fmt.Sprintf("Concurrently create index %s on field(s) %s of model %s",
		o.Index.Name, strings.Join(indexFieldNames(o.Index), ", "), o.ModelName)
}

// MigrationNameFragment is this operation's contribution to a suggested
// migration name.
//
// django: operations/models.py AddIndex.migration_name_fragment
func (o *AddIndexConcurrently) MigrationNameFragment() string {
	return strings.ToLower(o.ModelName) + "_" + strings.ToLower(o.Index.Name)
}

// Reduce cancels the creation against a removal of the same index, and
// follows a rename, exactly as AddIndex does.
//
// django: operations/models.py AddIndex.reduce
func (o *AddIndexConcurrently) Reduce(other m.Operation, _ string) ([]m.Operation, m.ReduceKind) {
	switch x := other.(type) {
	case *RemoveIndexConcurrently:
		if o.Index.Name == x.Name {
			return []m.Operation{}, m.ReduceReplace
		}
	case *m.RemoveIndex:
		if o.Index.Name == x.Name {
			return []m.Operation{}, m.ReduceReplace
		}
	case *m.RenameIndex:
		if o.Index.Name == x.OldName {
			cp := *o
			cp.Index = o.Index.Clone()
			cp.Index.Name = x.NewName
			return []m.Operation{&cp}, m.ReduceReplace
		}
	}
	return baseReduce(o, other)
}

// RemoveIndexConcurrently drops an index with PostgreSQL's DROP INDEX
// CONCURRENTLY syntax. The statement cannot run inside a transaction, so
// the migration holding it must set Atomic: m.Ptr(false).
//
// django: contrib/postgres/operations.py RemoveIndexConcurrently
type RemoveIndexConcurrently struct {
	baseOp
	// ModelName is the model the index belongs to.
	ModelName string
	// Name is the index to drop.
	Name string
}

// Category reports the symbol makemigrations prints for this operation.
func (o *RemoveIndexConcurrently) Category() m.Category { return m.CategoryRemoval }

// RemovedIndex implements migrations.IndexRemoval, so that a core AddIndex
// folds into this operation the way it folds into a core RemoveIndex --
// which in Django comes from RemoveIndexConcurrently being a subclass.
func (o *RemoveIndexConcurrently) RemovedIndex() (string, string) { return o.ModelName, o.Name }

// Atomic refuses a transaction of the operation's own.
//
// django: contrib/postgres/operations.py RemoveIndexConcurrently.atomic
func (o *RemoveIndexConcurrently) Atomic() *bool { return m.Ptr(false) }

// StateForwards removes the index from the model state, exactly as
// RemoveIndex does.
//
// django: operations/models.py RemoveIndex.state_forwards
func (o *RemoveIndexConcurrently) StateForwards(app string, s *m.ProjectState) error {
	ms, err := s.Model(app, o.ModelName)
	if err != nil {
		return err
	}
	if _, err := ms.GetIndexByName(o.Name); err != nil {
		return valueError(err)
	}
	s.RemoveIndex(app, o.ModelName, o.Name)
	return nil
}

// DatabaseForwards drops the index concurrently.
//
// django: contrib/postgres/operations.py RemoveIndexConcurrently.database_forwards
func (o *RemoveIndexConcurrently) DatabaseForwards(app string, ed m.SchemaEditor, from, _ *m.ProjectState) error {
	return concurrentIndex(app, ed, from, o.ModelName, o, func(pg Editor, model *m.Model) error {
		ix, err := indexOf(from, app, o.ModelName, o.Name)
		if err != nil {
			return err
		}
		return pg.RemoveIndexConcurrently(model, ix)
	})
}

// DatabaseBackwards creates the index again, concurrently.
//
// django: contrib/postgres/operations.py RemoveIndexConcurrently.database_backwards
func (o *RemoveIndexConcurrently) DatabaseBackwards(app string, ed m.SchemaEditor, _, to *m.ProjectState) error {
	return concurrentIndex(app, ed, to, o.ModelName, o, func(pg Editor, model *m.Model) error {
		ix, err := indexOf(to, app, o.ModelName, o.Name)
		if err != nil {
			return err
		}
		return pg.AddIndexConcurrently(model, ix)
	})
}

// ReferencesField reports whether the operation touches the named field.
func (o *RemoveIndexConcurrently) ReferencesField(model, _, app string) bool {
	return o.ReferencesModel(model, app)
}

// Describe is the line makemigrations and migrate --plan print.
//
// django: contrib/postgres/operations.py RemoveIndexConcurrently.describe
func (o *RemoveIndexConcurrently) Describe() string {
	return fmt.Sprintf("Concurrently remove index %s from %s", o.Name, o.ModelName)
}

// MigrationNameFragment is this operation's contribution to a suggested
// migration name.
//
// django: operations/models.py RemoveIndex.migration_name_fragment
func (o *RemoveIndexConcurrently) MigrationNameFragment() string {
	return "remove_" + strings.ToLower(o.ModelName) + "_" + strings.ToLower(o.Name)
}

// Reduce is the default reduction, as RemoveIndex's is.
func (o *RemoveIndexConcurrently) Reduce(other m.Operation, _ string) ([]m.Operation, m.ReduceKind) {
	return baseReduce(o, other)
}

// concurrentIndex applies the guards the two operations share -- no
// transaction, a vendor that has CONCURRENTLY, and the routers -- and then
// runs step on the model of state.
//
// django: contrib/postgres/operations.py AddIndexConcurrently.database_forwards
func concurrentIndex(app string, ed m.SchemaEditor, state *m.ProjectState, modelName string, op m.Operation, step func(Editor, *m.Model) error) error {
	// The vendor guard comes before Django's transaction check: on a
	// database that has no CONCURRENTLY the operation is a silent no-op,
	// and a no-op has no reason to refuse the transaction it never uses.
	if !isFamily(ed) {
		return nil
	}
	if err := ensureNotInTransaction(ed, op); err != nil {
		return err
	}
	model, err := getModel(state, app, modelName)
	if err != nil {
		return err
	}
	if !allowMigrateModel(ed.Connection(), model) {
		return nil
	}
	pg, err := editorOf(ed)
	if err != nil {
		return err
	}
	return step(pg, model)
}

// indexOf reads an index out of a model state by name.
func indexOf(state *m.ProjectState, app, modelName, name string) (m.Index, error) {
	ms, err := state.Model(app, modelName)
	if err != nil {
		return m.Index{}, err
	}
	ix, err := ms.GetIndexByName(name)
	if err != nil {
		return m.Index{}, valueError(err)
	}
	return ix, nil
}

// indexFieldNames is the index's column names with a "-" in front of a
// descending one, which is how Django's Index.fields spells them.
//
// django: db/models/indexes.py Index.fields
func indexFieldNames(ix m.Index) []string {
	var out []string
	for _, f := range ix.Fields {
		if f.Column == "" {
			continue
		}
		if strings.EqualFold(string(f.Sort), string(m.SortDesc)) {
			out = append(out, "-"+f.Column)
		} else {
			out = append(out, f.Column)
		}
	}
	return out
}
