package migrations

import (
	"fmt"
	"strings"
)

// django: migrations/operations/models.py

// modelOpReduce is the reduction a model operation falls back to: another
// operation that does not reference the same model can be optimized across.
//
// django: operations/models.py ModelOperation.reduce
func modelOpReduce(op Operation, name string, other Operation, app string) ([]Operation, ReduceKind) {
	if ops, kind := baseReduce(op, other); kind == ReduceReplace {
		return ops, kind
	}
	if !other.ReferencesModel(name, app) {
		return nil, ReduceThrough
	}
	return nil, ReduceBlock
}

// CreateModel creates a model's table.
type CreateModel struct {
	baseOp
	Name    string
	Table   string
	Fields  Fields
	Options Options
}

func (o *CreateModel) Category() Category { return CategoryAddition }

func (o *CreateModel) StateForwards(app string, s *ProjectState) error {
	ms := &ModelState{App: app, Name: o.Name, Table: o.Table, Options: o.Options.Clone()}
	ms.Fields = make(Fields, len(o.Fields))
	for i, f := range o.Fields {
		ms.Fields[i] = NamedField{Name: f.Name, Field: f.Field.Clone()}
	}
	if err := ms.Validate(); err != nil {
		return valueError(err)
	}
	s.AddModel(ms)
	return nil
}

func (o *CreateModel) DatabaseForwards(app string, ed SchemaEditor, from, to *ProjectState) error {
	m, err := getModel(to, app, o.Name)
	if err != nil {
		return err
	}
	if !allowMigrateModel(ed.Connection(), m) {
		return nil
	}
	return ed.CreateModel(m)
}

func (o *CreateModel) DatabaseBackwards(app string, ed SchemaEditor, from, to *ProjectState) error {
	m, err := getModel(from, app, o.Name)
	if err != nil {
		return err
	}
	if !allowMigrateModel(ed.Connection(), m) {
		return nil
	}
	return ed.DeleteModel(m)
}

func (o *CreateModel) Describe() string { return "create model " + o.Name }

func (o *CreateModel) MigrationNameFragment() string { return strings.ToLower(o.Name) }

func (o *CreateModel) ReferencesModel(name, app string) bool {
	if strings.EqualFold(name, o.Name) {
		return true
	}
	ref := ModelKey{app, strings.ToLower(name)}
	for _, f := range o.Fields {
		if FieldReferences(ModelKey{app, strings.ToLower(o.Name)}, f.Field, ref, "") {
			return true
		}
	}
	return false
}

func (o *CreateModel) ReferencesField(model, name, app string) bool {
	return o.ReferencesModel(model, app)
}

func (o *CreateModel) with(fn func(c *CreateModel)) []Operation {
	cp := *o
	cp.Fields = append(Fields(nil), o.Fields...)
	cp.Options = o.Options.Clone()
	fn(&cp)
	return []Operation{&cp}
}

func (o *CreateModel) Reduce(other Operation, app string) ([]Operation, ReduceKind) {
	lower := strings.ToLower(o.Name)
	switch x := other.(type) {
	case *DeleteModel:
		if strings.ToLower(x.Name) == lower {
			return []Operation{}, ReduceReplace
		}
	case *RenameModel:
		if strings.ToLower(x.OldName) == lower {
			return o.with(func(c *CreateModel) { c.Name = x.NewName }), ReduceReplace
		}
	case *AlterModelOptions:
		if strings.ToLower(x.Name) == lower {
			return o.with(func(c *CreateModel) { c.Options.Managed = x.Managed }), ReduceReplace
		}
	case *AlterModelTable:
		if strings.ToLower(x.Name) == lower {
			return o.with(func(c *CreateModel) { c.Table = x.Table }), ReduceReplace
		}
	case *AlterModelTableComment:
		if strings.ToLower(x.Name) == lower {
			return o.with(func(c *CreateModel) { c.Options.DBTableComment = x.TableComment }), ReduceReplace
		}
	case *AlterUniqueTogether:
		if strings.ToLower(x.Name) == lower {
			return o.with(func(c *CreateModel) { c.Options.UniqueTogether = normalizeTogether(x.UniqueTogether) }), ReduceReplace
		}
	case *AddField:
		if strings.ToLower(x.ModelName) == lower {
			return o.with(func(c *CreateModel) {
				c.Fields = append(c.Fields, NamedField{Name: x.Name, Field: x.Field})
			}), ReduceReplace
		}
	case *AlterField:
		if strings.ToLower(x.ModelName) == lower {
			return o.with(func(c *CreateModel) {
				for i, f := range c.Fields {
					if f.Name == x.Name {
						c.Fields[i] = NamedField{Name: f.Name, Field: x.Field}
					}
				}
			}), ReduceReplace
		}
	case *RemoveField:
		if strings.ToLower(x.ModelName) == lower {
			return o.with(func(c *CreateModel) {
				var ut [][]string
				for _, t := range c.Options.UniqueTogether {
					var kept []string
					for _, f := range t {
						if f != strings.ToLower(x.Name) {
							kept = append(kept, f)
						}
					}
					if len(kept) > 0 {
						ut = append(ut, kept)
					}
				}
				c.Options.UniqueTogether = ut
				var fields Fields
				for _, f := range c.Fields {
					if !strings.EqualFold(f.Name, x.Name) {
						fields = append(fields, f)
					}
				}
				c.Fields = fields
			}), ReduceReplace
		}
	case *RenameField:
		if strings.ToLower(x.ModelName) == lower {
			return o.with(func(c *CreateModel) {
				for i, t := range c.Options.UniqueTogether {
					c.Options.UniqueTogether[i] = renameIn(t, x.OldName, x.NewName)
				}
				for i, f := range c.Fields {
					if f.Name == x.OldName {
						c.Fields[i].Name = x.NewName
					}
				}
			}), ReduceReplace
		}
	case *AddIndex:
		if strings.ToLower(x.ModelName) == lower {
			return o.with(func(c *CreateModel) { c.Options.Indexes = append(c.Options.Indexes, x.Index.Clone()) }), ReduceReplace
		}
	case *RemoveIndex:
		if strings.ToLower(x.ModelName) == lower {
			return o.with(func(c *CreateModel) {
				var ix []Index
				for _, i := range c.Options.Indexes {
					if i.Name != x.Name {
						ix = append(ix, i)
					}
				}
				c.Options.Indexes = ix
			}), ReduceReplace
		}
	case *AddConstraint:
		if strings.ToLower(x.ModelName) == lower {
			return o.with(func(c *CreateModel) {
				c.Options.Constraints = append(c.Options.Constraints, x.Constraint.cloneConstraint())
			}), ReduceReplace
		}
	case *AlterConstraint:
		if strings.ToLower(x.ModelName) == lower {
			return o.with(func(c *CreateModel) {
				var cs []Constraint
				for _, k := range c.Options.Constraints {
					if k.ConstraintName() != x.Name {
						cs = append(cs, k)
					}
				}
				c.Options.Constraints = append(cs, x.Constraint.cloneConstraint())
			}), ReduceReplace
		}
	case *RemoveConstraint:
		if strings.ToLower(x.ModelName) == lower {
			return o.with(func(c *CreateModel) {
				var cs []Constraint
				for _, k := range c.Options.Constraints {
					if k.ConstraintName() != x.Name {
						cs = append(cs, k)
					}
				}
				c.Options.Constraints = cs
			}), ReduceReplace
		}
	}
	return modelOpReduce(o, o.Name, other, app)
}

// DeleteModel drops a model's table.
type DeleteModel struct {
	baseOp
	Name string
}

func (o *DeleteModel) Category() Category { return CategoryRemoval }

func (o *DeleteModel) StateForwards(app string, s *ProjectState) error {
	if _, err := s.Model(app, o.Name); err != nil {
		return err
	}
	s.RemoveModel(app, o.Name)
	return nil
}

func (o *DeleteModel) DatabaseForwards(app string, ed SchemaEditor, from, to *ProjectState) error {
	m, err := getModel(from, app, o.Name)
	if err != nil {
		return err
	}
	if !allowMigrateModel(ed.Connection(), m) {
		return nil
	}
	return ed.DeleteModel(m)
}

func (o *DeleteModel) DatabaseBackwards(app string, ed SchemaEditor, from, to *ProjectState) error {
	m, err := getModel(to, app, o.Name)
	if err != nil {
		return err
	}
	if !allowMigrateModel(ed.Connection(), m) {
		return nil
	}
	return ed.CreateModel(m)
}

func (o *DeleteModel) ReferencesModel(string, string) bool { return true }
func (o *DeleteModel) ReferencesField(model, name, app string) bool {
	return o.ReferencesModel(model, app)
}
func (o *DeleteModel) Describe() string { return "delete model " + o.Name }
func (o *DeleteModel) MigrationNameFragment() string {
	return "delete_" + strings.ToLower(o.Name)
}
func (o *DeleteModel) Reduce(other Operation, app string) ([]Operation, ReduceKind) {
	return modelOpReduce(o, o.Name, other, app)
}

// RenameModel renames a model. The table name is part of the model state,
// so renaming a model doesn't rename its table; a changed table name is a
// separate AlterModelTable.
type RenameModel struct {
	baseOp
	OldName string
	NewName string
}

func (o *RenameModel) Category() Category { return CategoryAlteration }

func (o *RenameModel) StateForwards(app string, s *ProjectState) error {
	if _, err := s.Model(app, o.OldName); err != nil {
		return err
	}
	s.RenameModel(app, o.OldName, o.NewName)
	return nil
}

func (o *RenameModel) DatabaseForwards(app string, ed SchemaEditor, from, to *ProjectState) error {
	return renameModelDB(app, ed, from, to, o.OldName, o.NewName)
}

func (o *RenameModel) DatabaseBackwards(app string, ed SchemaEditor, from, to *ProjectState) error {
	return renameModelDB(app, ed, from, to, o.NewName, o.OldName)
}

// renameModelDB renames a model's table and fixes up the columns and index
// names that referred to it. RenameModel runs it in both directions.
//
// django: operations/models.py RenameModel.database_forwards
func renameModelDB(app string, ed SchemaEditor, from, to *ProjectState, oldName, newName string) error {
	newModel, err := getModel(to, app, newName)
	if err != nil {
		return err
	}
	if !allowMigrateModel(ed.Connection(), newModel) {
		return nil
	}
	oldModel, err := getModel(from, app, oldName)
	if err != nil {
		return err
	}
	if err := ed.AlterDBTable(newModel, oldModel.Table, newModel.Table); err != nil {
		return err
	}
	toApps := to.MustApps()
	for _, rel := range oldModel.RelatedFields() {
		var model *Model
		var relatedKey ModelKey
		if rel.Model == oldModel {
			model = newModel
			relatedKey = ModelKey{app, strings.ToLower(newName)}
		} else {
			relatedKey = rel.Model.Key()
			model = toApps.MustModel(relatedKey.App, relatedKey.Model)
		}
		toField := toApps.MustModel(relatedKey.App, relatedKey.Model).Field(rel.Name)
		if err := ed.AlterField(model, rel, toField, false); err != nil {
			return err
		}
	}
	return nil
}

func (o *RenameModel) ReferencesModel(name, app string) bool {
	return strings.EqualFold(name, o.OldName) || strings.EqualFold(name, o.NewName)
}
func (o *RenameModel) ReferencesField(model, name, app string) bool {
	return o.ReferencesModel(model, app)
}
func (o *RenameModel) Describe() string {
	return fmt.Sprintf("rename model %s to %s", o.OldName, o.NewName)
}
func (o *RenameModel) MigrationNameFragment() string {
	return fmt.Sprintf("rename_%s_%s", strings.ToLower(o.OldName), strings.ToLower(o.NewName))
}
func (o *RenameModel) Reduce(other Operation, app string) ([]Operation, ReduceKind) {
	if x, ok := other.(*RenameModel); ok && strings.EqualFold(o.NewName, x.OldName) {
		cp := *o
		cp.NewName = x.NewName
		return []Operation{&cp}, ReduceReplace
	}
	if ops, kind := baseReduce(o, other); kind == ReduceReplace {
		return ops, kind
	}
	if !other.ReferencesModel(o.NewName, app) {
		return nil, ReduceThrough
	}
	return nil, ReduceBlock
}

// modelOptionReduce reduces an operation that only changes a model option:
// a later operation of the same kind, or a DeleteModel, supersedes it.
// sameType says whether other is of the same kind as the operation reducing.
//
// django: operations/models.py ModelOptionOperation.reduce
func modelOptionReduce(op Operation, name string, other Operation, app string, sameType bool) ([]Operation, ReduceKind) {
	_, isDelete := other.(*DeleteModel)
	if (sameType || isDelete) && strings.EqualFold(name, modelOpName(other)) {
		return []Operation{other}, ReduceReplace
	}
	return modelOpReduce(op, name, other, app)
}

// modelOpName returns the model an operation names, or "" for operations
// that do not name one.
func modelOpName(op Operation) string {
	switch x := op.(type) {
	case *DeleteModel:
		return x.Name
	case *AlterModelTable:
		return x.Name
	case *AlterModelTableComment:
		return x.Name
	case *AlterUniqueTogether:
		return x.Name
	case *AlterModelOptions:
		return x.Name
	}
	return ""
}

// AlterModelTable renames a model's table.
type AlterModelTable struct {
	baseOp
	Name  string
	Table string
}

func (o *AlterModelTable) Category() Category { return CategoryAlteration }

func (o *AlterModelTable) StateForwards(app string, s *ProjectState) error {
	if _, err := s.Model(app, o.Name); err != nil {
		return err
	}
	s.AlterTable(app, o.Name, o.Table)
	return nil
}

func (o *AlterModelTable) DatabaseForwards(app string, ed SchemaEditor, from, to *ProjectState) error {
	newModel, err := getModel(to, app, o.Name)
	if err != nil {
		return err
	}
	if !allowMigrateModel(ed.Connection(), newModel) {
		return nil
	}
	oldModel, err := getModel(from, app, o.Name)
	if err != nil {
		return err
	}
	return ed.AlterDBTable(newModel, oldModel.Table, newModel.Table)
}

func (o *AlterModelTable) DatabaseBackwards(app string, ed SchemaEditor, from, to *ProjectState) error {
	return o.DatabaseForwards(app, ed, from, to)
}

func (o *AlterModelTable) ReferencesModel(name, app string) bool {
	return strings.EqualFold(name, o.Name)
}
func (o *AlterModelTable) ReferencesField(model, name, app string) bool {
	return o.ReferencesModel(model, app)
}
func (o *AlterModelTable) Describe() string {
	return fmt.Sprintf("rename table for %s to %s", o.Name, o.Table)
}
func (o *AlterModelTable) MigrationNameFragment() string {
	return "alter_" + strings.ToLower(o.Name) + "_table"
}
func (o *AlterModelTable) Reduce(other Operation, app string) ([]Operation, ReduceKind) {
	_, same := other.(*AlterModelTable)
	return modelOptionReduce(o, o.Name, other, app, same)
}

// AlterModelTableComment changes the table comment.
type AlterModelTableComment struct {
	baseOp
	Name         string
	TableComment string
}

func (o *AlterModelTableComment) Category() Category { return CategoryAlteration }

func (o *AlterModelTableComment) StateForwards(app string, s *ProjectState) error {
	if _, err := s.Model(app, o.Name); err != nil {
		return err
	}
	s.AlterTableComment(app, o.Name, o.TableComment)
	return nil
}

func (o *AlterModelTableComment) DatabaseForwards(app string, ed SchemaEditor, from, to *ProjectState) error {
	newModel, err := getModel(to, app, o.Name)
	if err != nil {
		return err
	}
	if !allowMigrateModel(ed.Connection(), newModel) {
		return nil
	}
	oldModel, err := getModel(from, app, o.Name)
	if err != nil {
		return err
	}
	return ed.AlterDBTableComment(newModel, oldModel.State.Options.DBTableComment, newModel.State.Options.DBTableComment)
}

func (o *AlterModelTableComment) DatabaseBackwards(app string, ed SchemaEditor, from, to *ProjectState) error {
	return o.DatabaseForwards(app, ed, from, to)
}

func (o *AlterModelTableComment) ReferencesModel(name, app string) bool {
	return strings.EqualFold(name, o.Name)
}
func (o *AlterModelTableComment) ReferencesField(model, name, app string) bool {
	return o.ReferencesModel(model, app)
}
func (o *AlterModelTableComment) Describe() string {
	return fmt.Sprintf("alter %s table comment", o.Name)
}
func (o *AlterModelTableComment) MigrationNameFragment() string {
	return "alter_" + strings.ToLower(o.Name) + "_table_comment"
}
func (o *AlterModelTableComment) Reduce(other Operation, app string) ([]Operation, ReduceKind) {
	_, same := other.(*AlterModelTableComment)
	return modelOptionReduce(o, o.Name, other, app, same)
}

// AlterUniqueTogether changes unique_together to the target value.
type AlterUniqueTogether struct {
	baseOp
	Name           string
	UniqueTogether [][]string
}

func (o *AlterUniqueTogether) Category() Category { return CategoryAlteration }

func (o *AlterUniqueTogether) StateForwards(app string, s *ProjectState) error {
	if _, err := s.Model(app, o.Name); err != nil {
		return err
	}
	s.AlterUniqueTogether(app, o.Name, o.UniqueTogether)
	return nil
}

func (o *AlterUniqueTogether) DatabaseForwards(app string, ed SchemaEditor, from, to *ProjectState) error {
	newModel, err := getModel(to, app, o.Name)
	if err != nil {
		return err
	}
	if !allowMigrateModel(ed.Connection(), newModel) {
		return nil
	}
	fromMS := from.mustModel(app, o.Name)
	toMS := to.mustModel(app, o.Name)
	return ed.AlterUniqueTogether(newModel, fromMS.Options.UniqueTogether, toMS.Options.UniqueTogether)
}

func (o *AlterUniqueTogether) DatabaseBackwards(app string, ed SchemaEditor, from, to *ProjectState) error {
	return o.DatabaseForwards(app, ed, from, to)
}

func (o *AlterUniqueTogether) ReferencesModel(name, app string) bool {
	return strings.EqualFold(name, o.Name)
}

func (o *AlterUniqueTogether) ReferencesField(model, name, app string) bool {
	if !o.ReferencesModel(model, app) {
		return false
	}
	if len(o.UniqueTogether) == 0 {
		return true
	}
	for _, t := range o.UniqueTogether {
		for _, f := range t {
			if f == name {
				return true
			}
		}
	}
	return false
}

func (o *AlterUniqueTogether) Describe() string {
	return fmt.Sprintf("alter unique_together for %s (%d constraint(s))", o.Name, len(dedupTogether(o.UniqueTogether)))
}

func (o *AlterUniqueTogether) MigrationNameFragment() string {
	return "alter_" + strings.ToLower(o.Name) + "_unique_together"
}

func (o *AlterUniqueTogether) Reduce(other Operation, app string) ([]Operation, ReduceKind) {
	_, isDelete := other.(*DeleteModel)
	_, same := other.(*AlterUniqueTogether)
	if (same || isDelete) && strings.EqualFold(o.Name, modelOpName(other)) {
		return []Operation{other}, ReduceReplace
	}
	if ops, kind := baseReduce(o, other); kind == ReduceReplace {
		return ops, kind
	}
	// Django lets a together option reduce through a together option of a
	// different kind; unique_together is the only one gormgate has, so
	// this is the plain model check.
	if !other.ReferencesModel(o.Name, app) {
		return nil, ReduceThrough
	}
	return nil, ReduceBlock
}

// AlterModelOptions sets model options that don't affect the database
// schema. For gorm models the only such option is managed.
type AlterModelOptions struct {
	baseOp
	Name    string
	Managed *bool
}

func (o *AlterModelOptions) Category() Category { return CategoryAlteration }

func (o *AlterModelOptions) StateForwards(app string, s *ProjectState) error {
	if _, err := s.Model(app, o.Name); err != nil {
		return err
	}
	s.AlterModelOptions(app, o.Name, o.Managed, true)
	return nil
}

func (o *AlterModelOptions) DatabaseForwards(string, SchemaEditor, *ProjectState, *ProjectState) error {
	return nil
}
func (o *AlterModelOptions) DatabaseBackwards(string, SchemaEditor, *ProjectState, *ProjectState) error {
	return nil
}
func (o *AlterModelOptions) ReferencesModel(name, app string) bool {
	return strings.EqualFold(name, o.Name)
}
func (o *AlterModelOptions) ReferencesField(model, name, app string) bool {
	return o.ReferencesModel(model, app)
}
func (o *AlterModelOptions) Describe() string { return "change meta options on " + o.Name }
func (o *AlterModelOptions) MigrationNameFragment() string {
	return "alter_" + strings.ToLower(o.Name) + "_options"
}
func (o *AlterModelOptions) Reduce(other Operation, app string) ([]Operation, ReduceKind) {
	_, same := other.(*AlterModelOptions)
	return modelOptionReduce(o, o.Name, other, app, same)
}

// AddIndex adds an index on a model.
type AddIndex struct {
	baseOp
	ModelName string
	Index     Index
}

func (o *AddIndex) Category() Category { return CategoryAddition }

func (o *AddIndex) StateForwards(app string, s *ProjectState) error {
	if o.Index.Name == "" {
		return &ValueError{Msg: fmt.Sprintf("indexes passed to AddIndex operations require a name argument. %#v doesn't have one", o.Index)}
	}
	if _, err := s.Model(app, o.ModelName); err != nil {
		return err
	}
	s.AddIndex(app, o.ModelName, o.Index)
	return nil
}

func (o *AddIndex) DatabaseForwards(app string, ed SchemaEditor, from, to *ProjectState) error {
	m, err := getModel(to, app, o.ModelName)
	if err != nil {
		return err
	}
	if !allowMigrateModel(ed.Connection(), m) {
		return nil
	}
	return ed.AddIndex(m, o.Index)
}

func (o *AddIndex) DatabaseBackwards(app string, ed SchemaEditor, from, to *ProjectState) error {
	m, err := getModel(from, app, o.ModelName)
	if err != nil {
		return err
	}
	if !allowMigrateModel(ed.Connection(), m) {
		return nil
	}
	return ed.RemoveIndex(m, o.Index)
}

func (o *AddIndex) ReferencesField(model, name, app string) bool {
	return o.ReferencesModel(model, app)
}
func (o *AddIndex) Describe() string { return o.Index.describeFor(o.ModelName) }
func (o *AddIndex) MigrationNameFragment() string {
	return strings.ToLower(o.ModelName) + "_" + strings.ToLower(o.Index.Name)
}
func (o *AddIndex) Reduce(other Operation, app string) ([]Operation, ReduceKind) {
	if x, ok := other.(IndexRemoval); ok {
		if _, name := x.RemovedIndex(); o.Index.Name == name {
			return []Operation{}, ReduceReplace
		}
	}
	if x, ok := other.(*RenameIndex); ok && o.Index.Name == x.OldName {
		cp := *o
		cp.Index = o.Index.Clone()
		cp.Index.Name = x.NewName
		return []Operation{&cp}, ReduceReplace
	}
	return baseReduce(o, other)
}

// IndexRemoval is implemented by an operation that removes an index, so
// that AddIndex folds into any of them. Django gets this from inheritance
// -- its RemoveIndexConcurrently *is* a RemoveIndex, so one isinstance
// check covers both -- and an interface is the Go equivalent, which is what
// lets an operation in another package, such as
// migrations/postgres.RemoveIndexConcurrently, reduce the same way.
type IndexRemoval interface {
	Operation
	// RemovedIndex reports the model the operation removes an index from
	// and the name of that index.
	RemovedIndex() (modelName, indexName string)
}

// RemoveIndex removes an index from a model.
type RemoveIndex struct {
	baseOp
	ModelName string
	Name      string
}

func (o *RemoveIndex) Category() Category { return CategoryRemoval }

// RemovedIndex implements IndexRemoval.
func (o *RemoveIndex) RemovedIndex() (string, string) { return o.ModelName, o.Name }

func (o *RemoveIndex) StateForwards(app string, s *ProjectState) error {
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

func (o *RemoveIndex) DatabaseForwards(app string, ed SchemaEditor, from, to *ProjectState) error {
	m, err := getModel(from, app, o.ModelName)
	if err != nil {
		return err
	}
	if !allowMigrateModel(ed.Connection(), m) {
		return nil
	}
	ix, err := from.mustModel(app, o.ModelName).GetIndexByName(o.Name)
	if err != nil {
		return valueError(err)
	}
	return ed.RemoveIndex(m, ix)
}

func (o *RemoveIndex) DatabaseBackwards(app string, ed SchemaEditor, from, to *ProjectState) error {
	m, err := getModel(to, app, o.ModelName)
	if err != nil {
		return err
	}
	if !allowMigrateModel(ed.Connection(), m) {
		return nil
	}
	ix, err := to.mustModel(app, o.ModelName).GetIndexByName(o.Name)
	if err != nil {
		return valueError(err)
	}
	return ed.AddIndex(m, ix)
}

func (o *RemoveIndex) ReferencesField(model, name, app string) bool {
	return o.ReferencesModel(model, app)
}
func (o *RemoveIndex) Describe() string {
	return fmt.Sprintf("remove index %s from %s", o.Name, o.ModelName)
}
func (o *RemoveIndex) MigrationNameFragment() string {
	return "remove_" + strings.ToLower(o.ModelName) + "_" + strings.ToLower(o.Name)
}
func (o *RemoveIndex) Reduce(other Operation, app string) ([]Operation, ReduceKind) {
	return baseReduce(o, other)
}

// RenameIndex renames an index.
type RenameIndex struct {
	baseOp
	ModelName string
	NewName   string
	OldName   string
}

func (o *RenameIndex) Category() Category { return CategoryAlteration }

func (o *RenameIndex) StateForwards(app string, s *ProjectState) error {
	if o.OldName == "" {
		return &ValueError{Msg: "RenameIndex requires one of old_name and old_fields arguments to be set"}
	}
	if _, err := s.Model(app, o.ModelName); err != nil {
		return err
	}
	s.RenameIndex(app, o.ModelName, o.OldName, o.NewName)
	return nil
}

func (o *RenameIndex) DatabaseForwards(app string, ed SchemaEditor, from, to *ProjectState) error {
	return renameIndexDB(app, ed, from, to, o.ModelName, o.OldName, o.NewName)
}

func (o *RenameIndex) DatabaseBackwards(app string, ed SchemaEditor, from, to *ProjectState) error {
	return renameIndexDB(app, ed, from, to, o.ModelName, o.NewName, o.OldName)
}

func renameIndexDB(app string, ed SchemaEditor, from, to *ProjectState, modelName, oldName, newName string) error {
	m, err := getModel(to, app, modelName)
	if err != nil {
		return err
	}
	if !allowMigrateModel(ed.Connection(), m) {
		return nil
	}
	oldIndex, err := from.mustModel(app, modelName).GetIndexByName(oldName)
	if err != nil {
		return valueError(err)
	}
	if oldIndex.Name == newName {
		return nil
	}
	newIndex, err := to.mustModel(app, modelName).GetIndexByName(newName)
	if err != nil {
		return valueError(err)
	}
	return ed.RenameIndex(m, oldIndex, newIndex)
}

func (o *RenameIndex) ReferencesField(model, name, app string) bool {
	return o.ReferencesModel(model, app)
}
func (o *RenameIndex) Describe() string {
	return fmt.Sprintf("rename index %s on %s to %s", o.OldName, o.ModelName, o.NewName)
}
func (o *RenameIndex) MigrationNameFragment() string {
	return fmt.Sprintf("rename_%s_%s", strings.ToLower(o.OldName), strings.ToLower(o.NewName))
}
func (o *RenameIndex) Reduce(other Operation, app string) ([]Operation, ReduceKind) {
	if x, ok := other.(*RenameIndex); ok && strings.EqualFold(o.ModelName, x.ModelName) && x.OldName != "" && strings.EqualFold(o.NewName, x.OldName) {
		cp := *o
		cp.NewName = x.NewName
		return []Operation{&cp}, ReduceReplace
	}
	return baseReduce(o, other)
}

// AddConstraint adds a table constraint.
type AddConstraint struct {
	baseOp
	ModelName  string
	Constraint Constraint
}

func (o *AddConstraint) Category() Category { return CategoryAddition }

func (o *AddConstraint) StateForwards(app string, s *ProjectState) error {
	if _, err := s.Model(app, o.ModelName); err != nil {
		return err
	}
	s.AddConstraint(app, o.ModelName, o.Constraint)
	return nil
}

func (o *AddConstraint) DatabaseForwards(app string, ed SchemaEditor, from, to *ProjectState) error {
	m, err := getModel(to, app, o.ModelName)
	if err != nil {
		return err
	}
	if !allowMigrateModel(ed.Connection(), m) {
		return nil
	}
	return ed.AddConstraint(m, o.Constraint)
}

func (o *AddConstraint) DatabaseBackwards(app string, ed SchemaEditor, from, to *ProjectState) error {
	m, err := getModel(to, app, o.ModelName)
	if err != nil {
		return err
	}
	if !allowMigrateModel(ed.Connection(), m) {
		return nil
	}
	return ed.RemoveConstraint(m, o.Constraint)
}

func (o *AddConstraint) ReferencesField(model, name, app string) bool {
	return o.ReferencesModel(model, app)
}
func (o *AddConstraint) Describe() string {
	return fmt.Sprintf("create constraint %s on model %s", o.Constraint.ConstraintName(), o.ModelName)
}
func (o *AddConstraint) MigrationNameFragment() string {
	return strings.ToLower(o.ModelName) + "_" + strings.ToLower(o.Constraint.ConstraintName())
}
func (o *AddConstraint) Reduce(other Operation, app string) ([]Operation, ReduceKind) {
	if x, ok := other.(*RemoveConstraint); ok && strings.EqualFold(o.ModelName, x.ModelName) && o.Constraint.ConstraintName() == x.Name {
		return []Operation{}, ReduceReplace
	}
	if x, ok := other.(*AlterConstraint); ok && strings.EqualFold(o.ModelName, x.ModelName) && o.Constraint.ConstraintName() == x.Name {
		cp := *o
		cp.Constraint = x.Constraint
		return []Operation{&cp}, ReduceReplace
	}
	return baseReduce(o, other)
}

// RemoveConstraint removes a table constraint.
type RemoveConstraint struct {
	baseOp
	ModelName string
	Name      string
}

func (o *RemoveConstraint) Category() Category { return CategoryRemoval }

func (o *RemoveConstraint) StateForwards(app string, s *ProjectState) error {
	if _, err := s.Model(app, o.ModelName); err != nil {
		return err
	}
	s.RemoveConstraint(app, o.ModelName, o.Name)
	return nil
}

func (o *RemoveConstraint) DatabaseForwards(app string, ed SchemaEditor, from, to *ProjectState) error {
	m, err := getModel(to, app, o.ModelName)
	if err != nil {
		return err
	}
	if !allowMigrateModel(ed.Connection(), m) {
		return nil
	}
	c, err := from.mustModel(app, o.ModelName).GetConstraintByName(o.Name)
	if err != nil {
		return valueError(err)
	}
	return ed.RemoveConstraint(m, c)
}

func (o *RemoveConstraint) DatabaseBackwards(app string, ed SchemaEditor, from, to *ProjectState) error {
	m, err := getModel(to, app, o.ModelName)
	if err != nil {
		return err
	}
	if !allowMigrateModel(ed.Connection(), m) {
		return nil
	}
	c, err := to.mustModel(app, o.ModelName).GetConstraintByName(o.Name)
	if err != nil {
		return valueError(err)
	}
	return ed.AddConstraint(m, c)
}

func (o *RemoveConstraint) ReferencesField(model, name, app string) bool {
	return o.ReferencesModel(model, app)
}
func (o *RemoveConstraint) Describe() string {
	return fmt.Sprintf("remove constraint %s from model %s", o.Name, o.ModelName)
}
func (o *RemoveConstraint) MigrationNameFragment() string {
	return "remove_" + strings.ToLower(o.ModelName) + "_" + strings.ToLower(o.Name)
}
func (o *RemoveConstraint) Reduce(other Operation, app string) ([]Operation, ReduceKind) {
	return baseReduce(o, other)
}

// AlterConstraint changes state-only attributes of a constraint.
type AlterConstraint struct {
	baseOp
	ModelName  string
	Name       string
	Constraint Constraint
}

func (o *AlterConstraint) Category() Category { return CategoryAlteration }

func (o *AlterConstraint) StateForwards(app string, s *ProjectState) error {
	if _, err := s.Model(app, o.ModelName); err != nil {
		return err
	}
	s.AlterConstraint(app, o.ModelName, o.Name, o.Constraint)
	return nil
}

func (o *AlterConstraint) DatabaseForwards(string, SchemaEditor, *ProjectState, *ProjectState) error {
	return nil
}
func (o *AlterConstraint) DatabaseBackwards(string, SchemaEditor, *ProjectState, *ProjectState) error {
	return nil
}
func (o *AlterConstraint) ReferencesField(model, name, app string) bool {
	return o.ReferencesModel(model, app)
}
func (o *AlterConstraint) Describe() string {
	return fmt.Sprintf("alter constraint %s on %s", o.Name, o.ModelName)
}
func (o *AlterConstraint) MigrationNameFragment() string {
	return "alter_" + strings.ToLower(o.ModelName) + "_" + strings.ToLower(o.Constraint.ConstraintName())
}
func (o *AlterConstraint) Reduce(other Operation, app string) ([]Operation, ReduceKind) {
	switch x := other.(type) {
	case *AlterConstraint:
		if strings.EqualFold(o.ModelName, x.ModelName) && o.Name == x.Name {
			return []Operation{other}, ReduceReplace
		}
	case *RemoveConstraint:
		if strings.EqualFold(o.ModelName, x.ModelName) && o.Name == x.Name {
			return []Operation{other}, ReduceReplace
		}
	}
	return baseReduce(o, other)
}
