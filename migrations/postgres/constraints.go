package postgres

import (
	"fmt"
	"strings"

	m "github.com/ctolon/gormgate/migrations"
)

// AddConstraintNotValid adds a check constraint with PostgreSQL's NOT VALID
// suffix: the constraint is enforced for new rows without the table being
// scanned for existing violations. ValidateConstraint checks the rest
// later.
//
// django: contrib/postgres/operations.py AddConstraintNotValid
type AddConstraintNotValid struct {
	baseOp
	// ModelName is the model the constraint belongs to.
	ModelName string
	// Constraint is the constraint to add; it must be a
	// *migrations.CheckConstraint.
	Constraint m.Constraint
}

// Category reports the symbol makemigrations prints for this operation.
func (o *AddConstraintNotValid) Category() m.Category { return m.CategoryAddition }

// StateForwards adds the constraint to the model state, exactly as
// AddConstraint does. Django rejects a constraint that is not a check
// constraint in the operation's constructor; a Go composite literal has no
// constructor to reject it in, so it is rejected here, the first step of
// the operation that runs.
//
// django: contrib/postgres/operations.py AddConstraintNotValid.__init__
func (o *AddConstraintNotValid) StateForwards(app string, s *m.ProjectState) error {
	if err := o.checkConstraint(); err != nil {
		return err
	}
	if _, err := s.Model(app, o.ModelName); err != nil {
		return err
	}
	s.AddConstraint(app, o.ModelName, o.Constraint)
	return nil
}

// checkConstraint refuses a constraint PostgreSQL cannot add NOT VALID.
//
// django: contrib/postgres/operations.py AddConstraintNotValid.__init__
func (o *AddConstraintNotValid) checkConstraint() error {
	if _, ok := o.Constraint.(*m.CheckConstraint); !ok {
		return &m.ValueError{Msg: "AddConstraintNotValid.Constraint must be a check constraint"}
	}
	return nil
}

// DatabaseForwards adds the constraint with NOT VALID. Django reads the
// model out of the state the migration starts from here, unlike
// AddConstraint, which reads it out of the state it ends in.
//
// django: contrib/postgres/operations.py AddConstraintNotValid.database_forwards
func (o *AddConstraintNotValid) DatabaseForwards(app string, ed m.SchemaEditor, from, _ *m.ProjectState) error {
	if err := o.checkConstraint(); err != nil {
		return err
	}
	if !isFamily(ed) {
		return nil
	}
	model, err := getModel(from, app, o.ModelName)
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
	return pg.AddConstraintNotValid(model, o.Constraint)
}

// DatabaseBackwards drops the constraint, exactly as AddConstraint does.
//
// django: operations/models.py AddConstraint.database_backwards
func (o *AddConstraintNotValid) DatabaseBackwards(app string, ed m.SchemaEditor, _, to *m.ProjectState) error {
	if !isFamily(ed) {
		return nil
	}
	model, err := getModel(to, app, o.ModelName)
	if err != nil {
		return err
	}
	if !allowMigrateModel(ed.Connection(), model) {
		return nil
	}
	return ed.RemoveConstraint(model, o.Constraint)
}

// ReferencesField reports whether the operation touches the named field.
func (o *AddConstraintNotValid) ReferencesField(model, _, app string) bool {
	return o.ReferencesModel(model, app)
}

// Describe is the line makemigrations and migrate --plan print.
//
// django: contrib/postgres/operations.py AddConstraintNotValid.describe
func (o *AddConstraintNotValid) Describe() string {
	return fmt.Sprintf("Create not valid constraint %s on model %s", o.Constraint.ConstraintName(), o.ModelName)
}

// MigrationNameFragment is this operation's contribution to a suggested
// migration name: AddConstraint's, with "_not_valid" appended.
//
// django: contrib/postgres/operations.py AddConstraintNotValid.migration_name_fragment
func (o *AddConstraintNotValid) MigrationNameFragment() string {
	return strings.ToLower(o.ModelName) + "_" + strings.ToLower(o.Constraint.ConstraintName()) + "_not_valid"
}

// Reduce cancels the addition against a removal of the same constraint, and
// follows an alteration, exactly as AddConstraint does.
//
// django: operations/models.py AddConstraint.reduce
func (o *AddConstraintNotValid) Reduce(other m.Operation, _ string) ([]m.Operation, m.ReduceKind) {
	switch x := other.(type) {
	case *m.RemoveConstraint:
		if strings.EqualFold(o.ModelName, x.ModelName) && o.Constraint.ConstraintName() == x.Name {
			return []m.Operation{}, m.ReduceReplace
		}
	case *m.AlterConstraint:
		if strings.EqualFold(o.ModelName, x.ModelName) && o.Constraint.ConstraintName() == x.Name {
			cp := *o
			cp.Constraint = x.Constraint
			return []m.Operation{&cp}, m.ReduceReplace
		}
	}
	return baseReduce(o, other)
}

// ValidateConstraint checks the rows of a constraint that was added NOT
// VALID and marks the constraint valid. It changes no migration state, and
// unapplying it does nothing: PostgreSQL has no way to make a constraint
// invalid again.
//
// django: contrib/postgres/operations.py ValidateConstraint
type ValidateConstraint struct {
	baseOp
	// ModelName is the model the constraint belongs to.
	ModelName string
	// Name is the constraint to validate.
	Name string
}

// Category reports the symbol makemigrations prints for this operation.
func (o *ValidateConstraint) Category() m.Category { return m.CategoryAlteration }

// StateForwards does nothing: validating a constraint changes no state.
//
// django: contrib/postgres/operations.py ValidateConstraint.state_forwards
func (o *ValidateConstraint) StateForwards(string, *m.ProjectState) error { return nil }

// DatabaseForwards validates the constraint.
//
// django: contrib/postgres/operations.py ValidateConstraint.database_forwards
func (o *ValidateConstraint) DatabaseForwards(app string, ed m.SchemaEditor, from, _ *m.ProjectState) error {
	if !isFamily(ed) {
		return nil
	}
	model, err := getModel(from, app, o.ModelName)
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
	return pg.ValidateConstraint(model, o.Name)
}

// DatabaseBackwards does nothing: PostgreSQL does not provide a way to make
// a constraint invalid.
//
// django: contrib/postgres/operations.py ValidateConstraint.database_backwards
func (o *ValidateConstraint) DatabaseBackwards(string, m.SchemaEditor, *m.ProjectState, *m.ProjectState) error {
	return nil
}

// ReferencesField reports whether the operation touches the named field.
func (o *ValidateConstraint) ReferencesField(model, _, app string) bool {
	return o.ReferencesModel(model, app)
}

// Describe is the line makemigrations and migrate --plan print.
//
// django: contrib/postgres/operations.py ValidateConstraint.describe
func (o *ValidateConstraint) Describe() string {
	return fmt.Sprintf("Validate constraint %s on model %s", o.Name, o.ModelName)
}

// MigrationNameFragment is this operation's contribution to a suggested
// migration name.
//
// django: contrib/postgres/operations.py ValidateConstraint.migration_name_fragment
func (o *ValidateConstraint) MigrationNameFragment() string {
	return strings.ToLower(o.ModelName) + "_validate_" + strings.ToLower(o.Name)
}

// Reduce is the default reduction.
func (o *ValidateConstraint) Reduce(other m.Operation, _ string) ([]m.Operation, m.ReduceKind) {
	return baseReduce(o, other)
}

// The operations of this package all satisfy migrations.Operation.
var (
	_ m.Operation = (*CreateExtension)(nil)
	_ m.Operation = (*CreateCollation)(nil)
	_ m.Operation = (*RemoveCollation)(nil)
	_ m.Operation = (*AddIndexConcurrently)(nil)
	_ m.Operation = (*RemoveIndexConcurrently)(nil)
	_ m.Operation = (*AddConstraintNotValid)(nil)
	_ m.Operation = (*ValidateConstraint)(nil)
)
