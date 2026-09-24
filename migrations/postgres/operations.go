// Package postgres holds the migration operations PostgreSQL has and the
// core framework does not: extensions, collations, concurrent index
// creation, and constraints added without validating the rows already in
// the table.
//
// It is the port of django.contrib.postgres.operations. Every operation
// here is a no-op on a database that does not have the feature, exactly as
// Django's extension and collation operations are, so a migration written
// for PostgreSQL stays harmless in a project whose other databases are not
// PostgreSQL. Which vendors each operation counts as having the feature is
// spelled out on the vendor sets below.
//
// django: contrib/postgres/operations.py
package postgres

import (
	"fmt"
	"strings"

	m "github.com/ctolon/gormgate/migrations"
)

// baseOp is the alias these operations embed, so that the embedded field
// does not show up as a promoted field in their documentation. It supplies
// the Operation defaults and the marker method that seals the interface.
type baseOp = m.BaseOperation

// Editor is what these operations need from the schema editor beyond
// migrations.SchemaEditor: identifier quoting and the PostgreSQL-only DDL
// steps. backends/postgresql.Editor implements it, and so do the editors of
// the forks that embed it.
type Editor interface {
	m.SchemaEditor
	QuoteName(name string) string
	AddIndexConcurrently(model *m.Model, ix m.Index) error
	RemoveIndexConcurrently(model *m.Model, ix m.Index) error
	AddConstraintNotValid(model *m.Model, c m.Constraint) error
	ValidateConstraint(model *m.Model, name string) error
	// HasExtensions and HasCollations report what this server actually
	// has. CockroachDB and openGauss speak PostgreSQL's dialect and
	// implement everything above, but neither has extensions or
	// collations, so the operations that need them do nothing there.
	HasExtensions() bool
	HasCollations() bool
}

// AtomicBlockReporter is the part of a connection that reports whether a
// transaction is open on it, which is Django's connection.in_atomic_block.
// backends/base.Conn implements it.
type AtomicBlockReporter interface{ InAtomicBlock() bool }

// editorOf returns the PostgreSQL steps of ed. The vendor guard has already
// run when this is reached, so a schema editor that does not have them is a
// backend that reports a PostgreSQL-family vendor without being built on
// backends/postgresql.
func editorOf(ed m.SchemaEditor) (Editor, error) {
	pg, ok := ed.(Editor)
	if !ok {
		return nil, m.NotSupportedf("the schema editor of %s does not implement the PostgreSQL operations", ed.Connection().Vendor())
	}
	return pg, nil
}

// isFamily reports whether ed is a PostgreSQL-family schema editor. The
// concurrent-index and NOT VALID constraint operations need nothing beyond
// that: they were verified against PostgreSQL, CockroachDB and openGauss
// alike.
func isFamily(ed m.SchemaEditor) bool {
	_, ok := ed.(Editor)
	return ok
}

// hasExtensions and hasCollations ask the server what it has, rather than
// matching its name against a list a new backend would have to be added to.
func hasExtensions(ed m.SchemaEditor) bool {
	pg, ok := ed.(Editor)
	return ok && pg.HasExtensions()
}

func hasCollations(ed m.SchemaEditor) bool {
	pg, ok := ed.(Editor)
	return ok && pg.HasCollations()
}

// routerAllows asks the database routers about an operation that names no
// model, passing the operation's hints through unchanged.
//
// django: db/utils.py ConnectionRouter.allow_migrate
func routerAllows(ed m.SchemaEditor, app string, hints map[string]any) bool {
	return ed.Connection().AllowMigrate(app, m.Hints{Extra: hints})
}

// allowMigrateModel reports whether model may be migrated on this
// connection. It is the unexported helper of the core operations, which a
// package outside migrations cannot reach.
//
// django: operations/base.py Operation.allow_migrate_model
func allowMigrateModel(conn m.Connection, model *m.Model) bool {
	if !model.Managed() {
		return false
	}
	return conn.AllowMigrate(model.App, m.Hints{Model: model, ModelName: strings.ToLower(model.Name)})
}

// getModel renders the model of a state.
func getModel(s *m.ProjectState, app, name string) (*m.Model, error) {
	apps, err := s.Apps()
	if err != nil {
		return nil, err
	}
	return apps.GetModel(app, name)
}

// ensureNotInTransaction refuses an operation PostgreSQL cannot run inside
// a transaction.
//
// Django's message names the migration attribute to set; in a Go migration
// that attribute is the Atomic field, and its value is m.Ptr(false).
//
// django: contrib/postgres/operations.py NotInTransactionMixin._ensure_not_in_transaction
func ensureNotInTransaction(ed m.SchemaEditor, op m.Operation) error {
	c, ok := ed.Connection().(AtomicBlockReporter)
	if !ok || !c.InAtomicBlock() {
		return nil
	}
	return &m.NotSupportedError{Msg: fmt.Sprintf(
		"the %s operation cannot be executed inside a transaction (set Atomic: m.Ptr(false) on the migration)",
		m.OpName(op))}
}

// baseReduce is the reduction every operation falls back to: an elidable
// operation on either side drops out, and otherwise op blocks optimization
// across it. It is the core helper of the same name, which a package
// outside migrations cannot reach.
//
// django: operations/base.py Operation.reduce
func baseReduce(op, other m.Operation) ([]m.Operation, m.ReduceKind) {
	switch {
	case op.Elidable():
		return []m.Operation{other}, m.ReduceReplace
	case other.Elidable():
		return []m.Operation{op}, m.ReduceReplace
	}
	return nil, m.ReduceBlock
}

// valueError wraps err the way the core operations do.
func valueError(err error) *m.ValueError { return &m.ValueError{Msg: err.Error(), Err: err} }
