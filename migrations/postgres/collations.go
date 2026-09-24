package postgres

import (
	"strings"

	m "github.com/ctolon/gormgate/migrations"
)

// CreateCollation creates a database collation, and drops it again when the
// migration is unapplied. It changes no migration state.
//
// django: contrib/postgres/operations.py CreateCollation
type CreateCollation struct {
	baseOp
	// Name is the collation to create.
	Name string
	// Locale is the locale the collation sorts by.
	Locale string
	// Provider is the collation provider; empty means libc, which is
	// PostgreSQL's default.
	Provider string
	// Deterministic is nil for a deterministic collation, which is the
	// default, and m.Ptr(false) for a non-deterministic one.
	Deterministic *bool
}

// Category reports the symbol makemigrations prints for this operation.
func (o *CreateCollation) Category() m.Category { return m.CategoryAddition }

// StateForwards does nothing: a collation is not part of the model state.
//
// django: contrib/postgres/operations.py CollationOperation.state_forwards
func (o *CreateCollation) StateForwards(string, *m.ProjectState) error { return nil }

// DatabaseForwards creates the collation.
//
// django: contrib/postgres/operations.py CreateCollation.database_forwards
func (o *CreateCollation) DatabaseForwards(app string, ed m.SchemaEditor, _, _ *m.ProjectState) error {
	return runCollation(app, ed, createCollation, collation(o))
}

// DatabaseBackwards drops the collation.
//
// django: contrib/postgres/operations.py CreateCollation.database_backwards
func (o *CreateCollation) DatabaseBackwards(app string, ed m.SchemaEditor, _, _ *m.ProjectState) error {
	return runCollation(app, ed, removeCollation, collation(o))
}

// ReferencesField reports true: the operation names no model.
func (o *CreateCollation) ReferencesField(string, string, string) bool { return true }

// Describe is the line makemigrations and migrate --plan print.
//
// django: contrib/postgres/operations.py CreateCollation.describe
func (o *CreateCollation) Describe() string { return "Create collation " + o.Name }

// MigrationNameFragment is this operation's contribution to a suggested
// migration name.
//
// django: contrib/postgres/operations.py CreateCollation.migration_name_fragment
func (o *CreateCollation) MigrationNameFragment() string {
	return "create_collation_" + strings.ToLower(o.Name)
}

// Reduce cancels the creation against the removal of the same collation.
//
// django: contrib/postgres/operations.py CreateCollation.reduce
func (o *CreateCollation) Reduce(other m.Operation, _ string) ([]m.Operation, m.ReduceKind) {
	if x, ok := other.(*RemoveCollation); ok && o.Name == x.Name {
		return []m.Operation{}, m.ReduceReplace
	}
	return baseReduce(o, other)
}

// RemoveCollation drops a database collation, and creates it again when the
// migration is unapplied. It changes no migration state.
//
// django: contrib/postgres/operations.py RemoveCollation
type RemoveCollation struct {
	baseOp
	// Name is the collation to drop.
	Name string
	// Locale is the locale the collation sorts by; it is needed to
	// recreate the collation when the migration is unapplied.
	Locale string
	// Provider is the collation provider; empty means libc.
	Provider string
	// Deterministic is nil for a deterministic collation, which is the
	// default, and m.Ptr(false) for a non-deterministic one.
	Deterministic *bool
}

// Category reports the symbol makemigrations prints for this operation.
func (o *RemoveCollation) Category() m.Category { return m.CategoryRemoval }

// StateForwards does nothing: a collation is not part of the model state.
//
// django: contrib/postgres/operations.py CollationOperation.state_forwards
func (o *RemoveCollation) StateForwards(string, *m.ProjectState) error { return nil }

// DatabaseForwards drops the collation.
//
// django: contrib/postgres/operations.py RemoveCollation.database_forwards
func (o *RemoveCollation) DatabaseForwards(app string, ed m.SchemaEditor, _, _ *m.ProjectState) error {
	return runCollation(app, ed, removeCollation, collation(o))
}

// DatabaseBackwards creates the collation again.
//
// django: contrib/postgres/operations.py RemoveCollation.database_backwards
func (o *RemoveCollation) DatabaseBackwards(app string, ed m.SchemaEditor, _, _ *m.ProjectState) error {
	return runCollation(app, ed, createCollation, collation(o))
}

// ReferencesField reports true: the operation names no model.
func (o *RemoveCollation) ReferencesField(string, string, string) bool { return true }

// Describe is the line makemigrations and migrate --plan print.
//
// django: contrib/postgres/operations.py RemoveCollation.describe
func (o *RemoveCollation) Describe() string { return "Remove collation " + o.Name }

// MigrationNameFragment is this operation's contribution to a suggested
// migration name.
//
// django: contrib/postgres/operations.py RemoveCollation.migration_name_fragment
func (o *RemoveCollation) MigrationNameFragment() string {
	return "remove_collation_" + strings.ToLower(o.Name)
}

// Reduce is the default reduction.
func (o *RemoveCollation) Reduce(other m.Operation, _ string) ([]m.Operation, m.ReduceKind) {
	return baseReduce(o, other)
}

// collationArgs are the four attributes CollationOperation holds, gathered
// so that the two operations share one implementation.
//
// django: contrib/postgres/operations.py CollationOperation
type collationArgs struct {
	name, locale, provider string
	deterministic          *bool
}

// collation gathers the attributes of either collation operation.
func collation(o m.Operation) collationArgs {
	switch x := o.(type) {
	case *CreateCollation:
		return collationArgs{x.Name, x.Locale, x.Provider, x.Deterministic}
	case *RemoveCollation:
		return collationArgs{x.Name, x.Locale, x.Provider, x.Deterministic}
	}
	return collationArgs{}
}

// runCollation applies the vendor and router guards and then runs step.
func runCollation(app string, ed m.SchemaEditor, step func(Editor, collationArgs) error, a collationArgs) error {
	if !hasCollations(ed) || !routerAllows(ed, app, nil) {
		return nil
	}
	pg, err := editorOf(ed)
	if err != nil {
		return err
	}
	return step(pg, a)
}

// createCollation runs CREATE COLLATION. The locale and the provider go
// through quote_name, as Django writes them.
//
// django: contrib/postgres/operations.py CollationOperation.create_collation
func createCollation(pg Editor, a collationArgs) error {
	args := []string{"locale=" + pg.QuoteName(a.locale)}
	if a.provider != "" && a.provider != "libc" {
		args = append(args, "provider="+pg.QuoteName(a.provider))
	}
	if a.deterministic != nil && !*a.deterministic {
		args = append(args, "deterministic=false")
	}
	return pg.Execute("CREATE COLLATION " + pg.QuoteName(a.name) + " (" + strings.Join(args, ", ") + ")")
}

// removeCollation runs DROP COLLATION.
//
// django: contrib/postgres/operations.py CollationOperation.remove_collation
func removeCollation(pg Editor, a collationArgs) error {
	return pg.Execute("DROP COLLATION " + pg.QuoteName(a.name))
}
