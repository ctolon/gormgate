package migrations

import "fmt"

// The errors the migration machinery returns.
//
// django: migrations/exceptions.py

// AmbiguityError reports that more than one migration matches a name prefix.
type AmbiguityError struct{ Msg string }

func (e *AmbiguityError) Error() string { return e.Msg }

// BadMigrationError reports a migration that cannot be used: it is
// unreadable or badly formed.
type BadMigrationError struct{ Msg string }

func (e *BadMigrationError) Error() string { return e.Msg }

// CircularDependencyError reports a circular dependency between migrations,
// which cannot be resolved into a plan.
type CircularDependencyError struct{ Msg string }

func (e *CircularDependencyError) Error() string { return e.Msg }

// InconsistentMigrationHistory reports an applied migration some of whose
// dependencies are not applied.
type InconsistentMigrationHistory struct{ Msg string }

func (e *InconsistentMigrationHistory) Error() string { return e.Msg }

// IrreversibleError reports an attempt to reverse a migration that contains
// an irreversible operation.
type IrreversibleError struct{ Msg string }

func (e *IrreversibleError) Error() string { return e.Msg }

// NodeNotFoundError reports a reference to a migration that is not in the
// graph.
type NodeNotFoundError struct {
	// Message is the error text.
	Message string
	// Node is the missing migration.
	Node Key
	// Origin names the migration that referred to it, when known.
	Origin string
}

func (e *NodeNotFoundError) Error() string { return e.Message }

// MigrationSchemaMissing reports that the table recording applied migrations
// could not be created.
type MigrationSchemaMissing struct{ Msg string }

func (e *MigrationSchemaMissing) Error() string { return e.Msg }

// InvalidMigrationPlan reports a plan that mixes forwards and backwards
// migrations, which cannot be run as one.
type InvalidMigrationPlan struct {
	// Msg is the error text.
	Msg string
	// Plan is the offending plan.
	Plan []PlanStep
}

func (e *InvalidMigrationPlan) Error() string { return e.Msg }

// NotSupportedError reports an operation the backend cannot perform.
//
// django: db/__init__.py NotSupportedError
type NotSupportedError struct{ Msg string }

func (e *NotSupportedError) Error() string { return e.Msg }

// NotSupportedf builds a NotSupportedError with a formatted message.
func NotSupportedf(format string, a ...any) error {
	return &NotSupportedError{Msg: fmt.Sprintf(format, a...)}
}

// FieldDoesNotExist reports a reference to a field a model does not have.
//
// django: core/exceptions.py FieldDoesNotExist
type FieldDoesNotExist struct{ Msg string }

func (e *FieldDoesNotExist) Error() string { return e.Msg }

// ModelNotFoundError reports a reference to a model an app does not have.
//
// django: apps/registry.py Apps.get_model (LookupError)
type ModelNotFoundError struct{ Msg string }

func (e *ModelNotFoundError) Error() string { return e.Msg }

// ValueError reports a value the migration machinery cannot work with: a
// malformed operation argument, a dependency on an unknown app, or a change
// the backend refuses. It carries the name of the Python exception Django
// raises in the same places.
type ValueError struct {
	// Msg is the error text.
	Msg string
	// Err is the error this one was built from, when there is one.
	Err error
}

func (e *ValueError) Error() string { return e.Msg }

// Unwrap returns the cause, so that errors.Is and errors.As reach it.
func (e *ValueError) Unwrap() error { return e.Err }

// valueError wraps err in a ValueError that prints exactly as err does.
func valueError(err error) *ValueError { return &ValueError{Msg: err.Error(), Err: err} }
