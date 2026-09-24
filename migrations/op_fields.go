package migrations

import (
	"fmt"
	"strings"
)

// django: migrations/operations/fields.py

// fieldOpReferencesModel reports whether a field operation on modelName
// touches the named model, either because it is that model or because the
// field points at it.
//
// django: operations/fields.py FieldOperation.references_model
func fieldOpReferencesModel(modelName string, field *Field, name, app string) bool {
	if strings.EqualFold(name, modelName) {
		return true
	}
	if field != nil {
		return FieldReferences(ModelKey{app, strings.ToLower(modelName)}, *field, ModelKey{app, strings.ToLower(name)}, "")
	}
	return false
}

// fieldOpReferencesField reports whether a field operation on
// modelName.fieldName touches the named field of the named model.
//
// django: operations/fields.py FieldOperation.references_field
func fieldOpReferencesField(modelName, fieldName string, field *Field, model, name, app string) bool {
	if strings.EqualFold(model, modelName) {
		if name == fieldName {
			return true
		}
	}
	if field == nil {
		return false
	}
	return FieldReferences(ModelKey{app, strings.ToLower(modelName)}, *field, ModelKey{app, strings.ToLower(model)}, name)
}

// fieldOpReduce is the reduction a field operation falls back to: another
// operation that does not touch the same field can be optimized across.
//
// django: operations/fields.py FieldOperation.reduce
func fieldOpReduce(op Operation, modelName, fieldName string, other Operation, app string) ([]Operation, ReduceKind) {
	if ops, kind := baseReduce(op, other); kind == ReduceReplace {
		return ops, kind
	}
	if !other.ReferencesField(modelName, fieldName, app) {
		return nil, ReduceThrough
	}
	return nil, ReduceBlock
}

// fieldOperation is implemented by AddField, RemoveField, AlterField and
// RenameField.
type fieldOperation interface {
	Operation
	fieldOpModel() string
	fieldOpName() string
}

func isSameFieldOperation(a, b fieldOperation) bool {
	return strings.EqualFold(a.fieldOpModel(), b.fieldOpModel()) && strings.EqualFold(a.fieldOpName(), b.fieldOpName())
}

// preserve reads an operation's PreserveDefault, which defaults to true.
func preserve(p *bool) bool { return p == nil || *p }

// AddField adds a field to a model.
type AddField struct {
	baseOp
	ModelName string
	Name      string
	Field     Field
	// PreserveDefault is nil for the default (true). When false, Field's
	// Default is only used to populate existing rows.
	PreserveDefault *bool
}

func (o *AddField) fieldOpModel() string { return o.ModelName }
func (o *AddField) fieldOpName() string  { return o.Name }
func (o *AddField) Category() Category   { return CategoryAddition }

func (o *AddField) StateForwards(app string, s *ProjectState) error {
	if _, err := s.Model(app, o.ModelName); err != nil {
		return err
	}
	s.AddField(app, strings.ToLower(o.ModelName), o.Name, o.Field, preserve(o.PreserveDefault))
	return nil
}

func (o *AddField) DatabaseForwards(app string, ed SchemaEditor, from, to *ProjectState) error {
	toModel, err := getModel(to, app, o.ModelName)
	if err != nil {
		return err
	}
	if !allowMigrateModel(ed.Connection(), toModel) {
		return nil
	}
	fromModel, err := getModel(from, app, o.ModelName)
	if err != nil {
		return err
	}
	field := *toModel.Field(o.Name)
	if !preserve(o.PreserveDefault) {
		field.Field.Default = o.Field.Default
	}
	return ed.AddField(fromModel, &field)
}

func (o *AddField) DatabaseBackwards(app string, ed SchemaEditor, from, to *ProjectState) error {
	fromModel, err := getModel(from, app, o.ModelName)
	if err != nil {
		return err
	}
	if !allowMigrateModel(ed.Connection(), fromModel) {
		return nil
	}
	return ed.RemoveField(fromModel, fromModel.Field(o.Name))
}

func (o *AddField) Describe() string {
	return fmt.Sprintf("add field %s to %s", o.Name, o.ModelName)
}

func (o *AddField) MigrationNameFragment() string {
	return strings.ToLower(o.ModelName) + "_" + strings.ToLower(o.Name)
}

func (o *AddField) ReferencesModel(name, app string) bool {
	return fieldOpReferencesModel(o.ModelName, &o.Field, name, app)
}

func (o *AddField) ReferencesField(model, name, app string) bool {
	return fieldOpReferencesField(o.ModelName, o.Name, &o.Field, model, name, app)
}

func (o *AddField) Reduce(other Operation, app string) ([]Operation, ReduceKind) {
	if fo, ok := other.(fieldOperation); ok && isSameFieldOperation(o, fo) {
		switch x := other.(type) {
		case *AlterField:
			cp := *o
			cp.Name = x.Name
			cp.Field = x.Field
			return []Operation{&cp}, ReduceReplace
		case *RemoveField:
			return []Operation{}, ReduceReplace
		case *RenameField:
			cp := *o
			cp.Name = x.NewName
			return []Operation{&cp}, ReduceReplace
		}
	}
	return fieldOpReduce(o, o.ModelName, o.Name, other, app)
}

// RemoveField removes a field from a model.
type RemoveField struct {
	baseOp
	ModelName string
	Name      string
}

func (o *RemoveField) fieldOpModel() string { return o.ModelName }
func (o *RemoveField) fieldOpName() string  { return o.Name }
func (o *RemoveField) Category() Category   { return CategoryRemoval }

func (o *RemoveField) StateForwards(app string, s *ProjectState) error {
	ms, err := s.Model(app, o.ModelName)
	if err != nil {
		return err
	}
	if _, err := ms.GetField(o.Name); err != nil {
		return err
	}
	s.RemoveField(app, o.ModelName, o.Name)
	return nil
}

func (o *RemoveField) DatabaseForwards(app string, ed SchemaEditor, from, to *ProjectState) error {
	fromModel, err := getModel(from, app, o.ModelName)
	if err != nil {
		return err
	}
	if !allowMigrateModel(ed.Connection(), fromModel) {
		return nil
	}
	return ed.RemoveField(fromModel, fromModel.Field(o.Name))
}

func (o *RemoveField) DatabaseBackwards(app string, ed SchemaEditor, from, to *ProjectState) error {
	toModel, err := getModel(to, app, o.ModelName)
	if err != nil {
		return err
	}
	if !allowMigrateModel(ed.Connection(), toModel) {
		return nil
	}
	fromModel, err := getModel(from, app, o.ModelName)
	if err != nil {
		return err
	}
	return ed.AddField(fromModel, toModel.Field(o.Name))
}

func (o *RemoveField) Describe() string {
	return fmt.Sprintf("remove field %s from %s", o.Name, o.ModelName)
}

func (o *RemoveField) MigrationNameFragment() string {
	return "remove_" + strings.ToLower(o.ModelName) + "_" + strings.ToLower(o.Name)
}

func (o *RemoveField) ReferencesModel(name, app string) bool {
	return fieldOpReferencesModel(o.ModelName, nil, name, app)
}

func (o *RemoveField) ReferencesField(model, name, app string) bool {
	return fieldOpReferencesField(o.ModelName, o.Name, nil, model, name, app)
}

func (o *RemoveField) Reduce(other Operation, app string) ([]Operation, ReduceKind) {
	if d, ok := other.(*DeleteModel); ok && strings.EqualFold(d.Name, o.ModelName) {
		return []Operation{other}, ReduceReplace
	}
	return fieldOpReduce(o, o.ModelName, o.Name, other, app)
}

// AlterField alters a field's database column to the provided new field.
type AlterField struct {
	baseOp
	ModelName       string
	Name            string
	Field           Field
	PreserveDefault *bool
}

func (o *AlterField) fieldOpModel() string { return o.ModelName }
func (o *AlterField) fieldOpName() string  { return o.Name }
func (o *AlterField) Category() Category   { return CategoryAlteration }

func (o *AlterField) StateForwards(app string, s *ProjectState) error {
	ms, err := s.Model(app, o.ModelName)
	if err != nil {
		return err
	}
	if _, err := ms.GetField(o.Name); err != nil {
		return err
	}
	s.AlterField(app, o.ModelName, o.Name, o.Field, preserve(o.PreserveDefault))
	return nil
}

func (o *AlterField) DatabaseForwards(app string, ed SchemaEditor, from, to *ProjectState) error {
	toModel, err := getModel(to, app, o.ModelName)
	if err != nil {
		return err
	}
	if !allowMigrateModel(ed.Connection(), toModel) {
		return nil
	}
	fromModel, err := getModel(from, app, o.ModelName)
	if err != nil {
		return err
	}
	toField := *toModel.Field(o.Name)
	if !preserve(o.PreserveDefault) {
		toField.Field.Default = o.Field.Default
	}
	return ed.AlterField(fromModel, fromModel.Field(o.Name), &toField, false)
}

func (o *AlterField) DatabaseBackwards(app string, ed SchemaEditor, from, to *ProjectState) error {
	return o.DatabaseForwards(app, ed, from, to)
}

func (o *AlterField) Describe() string {
	return fmt.Sprintf("alter field %s on %s", o.Name, o.ModelName)
}

func (o *AlterField) MigrationNameFragment() string {
	return "alter_" + strings.ToLower(o.ModelName) + "_" + strings.ToLower(o.Name)
}

func (o *AlterField) ReferencesModel(name, app string) bool {
	return fieldOpReferencesModel(o.ModelName, &o.Field, name, app)
}

func (o *AlterField) ReferencesField(model, name, app string) bool {
	return fieldOpReferencesField(o.ModelName, o.Name, &o.Field, model, name, app)
}

func (o *AlterField) Reduce(other Operation, app string) ([]Operation, ReduceKind) {
	switch x := other.(type) {
	case *AlterField:
		if isSameFieldOperation(o, x) {
			return []Operation{other}, ReduceReplace
		}
	case *RemoveField:
		if isSameFieldOperation(o, x) {
			return []Operation{other}, ReduceReplace
		}
	case *RenameField:
		if isSameFieldOperation(o, x) {
			cp := *o
			cp.Name = x.NewName
			return []Operation{other, &cp}, ReduceReplace
		}
	}
	return fieldOpReduce(o, o.ModelName, o.Name, other, app)
}

// RenameField renames a field (and its column) on a model.
type RenameField struct {
	baseOp
	ModelName string
	OldName   string
	NewName   string
}

func (o *RenameField) fieldOpModel() string { return o.ModelName }
func (o *RenameField) fieldOpName() string  { return o.OldName }
func (o *RenameField) Category() Category   { return CategoryAlteration }

func (o *RenameField) StateForwards(app string, s *ProjectState) error {
	if _, err := s.Model(app, o.ModelName); err != nil {
		return err
	}
	return s.RenameField(app, o.ModelName, o.OldName, o.NewName)
}

func (o *RenameField) DatabaseForwards(app string, ed SchemaEditor, from, to *ProjectState) error {
	toModel, err := getModel(to, app, o.ModelName)
	if err != nil {
		return err
	}
	if !allowMigrateModel(ed.Connection(), toModel) {
		return nil
	}
	fromModel, err := getModel(from, app, o.ModelName)
	if err != nil {
		return err
	}
	return ed.AlterField(fromModel, fromModel.Field(o.OldName), toModel.Field(o.NewName), false)
}

func (o *RenameField) DatabaseBackwards(app string, ed SchemaEditor, from, to *ProjectState) error {
	toModel, err := getModel(to, app, o.ModelName)
	if err != nil {
		return err
	}
	if !allowMigrateModel(ed.Connection(), toModel) {
		return nil
	}
	fromModel, err := getModel(from, app, o.ModelName)
	if err != nil {
		return err
	}
	return ed.AlterField(fromModel, fromModel.Field(o.NewName), toModel.Field(o.OldName), false)
}

func (o *RenameField) Describe() string {
	return fmt.Sprintf("rename field %s on %s to %s", o.OldName, o.ModelName, o.NewName)
}

func (o *RenameField) MigrationNameFragment() string {
	return fmt.Sprintf("rename_%s_%s_%s", strings.ToLower(o.OldName), strings.ToLower(o.ModelName), strings.ToLower(o.NewName))
}

func (o *RenameField) ReferencesModel(name, app string) bool {
	return fieldOpReferencesModel(o.ModelName, nil, name, app)
}

func (o *RenameField) ReferencesField(model, name, app string) bool {
	return o.ReferencesModel(model, app) && (strings.EqualFold(name, o.OldName) || strings.EqualFold(name, o.NewName))
}

func (o *RenameField) Reduce(other Operation, app string) ([]Operation, ReduceKind) {
	if x, ok := other.(*RenameField); ok && strings.EqualFold(o.ModelName, x.ModelName) && strings.EqualFold(o.NewName, x.OldName) {
		cp := *o
		cp.NewName = x.NewName
		return []Operation{&cp}, ReduceReplace
	}
	if ops, kind := baseReduce(o, other); kind == ReduceReplace {
		return ops, kind
	}
	if !(other.ReferencesField(o.ModelName, o.OldName, app) || other.ReferencesField(o.ModelName, o.NewName, app)) {
		return nil, ReduceThrough
	}
	return nil, ReduceBlock
}

func getModel(s *ProjectState, app, name string) (*Model, error) {
	apps, err := s.Apps()
	if err != nil {
		return nil, err
	}
	return apps.GetModel(app, name)
}
