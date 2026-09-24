package migrations

import (
	"cmp"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"gorm.io/gorm"
)

// Category is the symbol shown in front of an operation by makemigrations.
// The zero value, the empty string, means the operation's category is not
// one of the constants below; FormattedDescription prints it as
// CategoryMixed.
//
// django: operations/base.py OperationCategory
type Category string

const (
	CategoryAddition   Category = "+"
	CategoryRemoval    Category = "-"
	CategoryAlteration Category = "~"
	CategoryCode       Category = "p"
	CategorySQL        Category = "s"
	CategoryMixed      Category = "?"
)

// ReduceKind is the outcome of Operation.Reduce.
type ReduceKind int

const (
	// ReduceBlock means the operation cannot be optimized across.
	ReduceBlock ReduceKind = iota
	// ReduceThrough means the operation can be optimized across.
	ReduceThrough
	// ReduceReplace means both operations are replaced by the returned list.
	ReduceReplace
)

// Operation is one step of a migration.
//
// django: migrations/operations/base.py Operation
type Operation interface {
	StateForwards(app string, state *ProjectState) error
	DatabaseForwards(app string, ed SchemaEditor, from, to *ProjectState) error
	DatabaseBackwards(app string, ed SchemaEditor, from, to *ProjectState) error
	Describe() string
	// MigrationNameFragment returns the part this operation contributes
	// to a suggested migration name, or "" when it contributes none.
	MigrationNameFragment() string
	Category() Category
	ReferencesModel(name, app string) bool
	ReferencesField(model, name, app string) bool
	Reduce(other Operation, app string) ([]Operation, ReduceKind)
	Reversible() bool
	ReducesToSQL() bool
	// Atomic overrides the migration's own atomic setting; nil leaves
	// the migration to decide.
	Atomic() *bool
	Elidable() bool
	// isOperation seals the interface: an operation outside this package
	// is written by embedding BaseOperation, which supplies this method
	// along with the defaults. Sealing lets the interface grow without
	// breaking those operations.
	isOperation()
}

// Connection is what operations and historical models need from a database
// connection.
type Connection interface {
	Alias() string
	Vendor() string
	// DB is the gorm handle; inside an atomic block it is the transaction.
	DB() *gorm.DB
	// AllowMigrate asks the configured routers whether what h describes,
	// in app, may be migrated on this connection.
	AllowMigrate(app string, h Hints) bool
}

// Hints are what a router is told about the migration it is being asked
// about.
//
// The framework fills in the typed fields, so a router can rely on them
// being there and spelled the same way every time it is called. Extra
// carries the Hints map a RunSQL or RunGo operation was written with,
// unchanged.
type Hints struct {
	// Model is the model being migrated, when the question is about one;
	// nil when the operation names no model.
	Model *Model
	// ModelName is the lower-cased model name, set whenever Model is.
	ModelName string
	// Extra is the operation's own Hints map.
	Extra map[string]any
}

// SchemaEditor performs DDL for operations. Backends implement it.
//
// django: db/backends/base/schema.py BaseDatabaseSchemaEditor
type SchemaEditor interface {
	Connection() Connection
	// AtomicMigration reports whether the whole migration runs in one
	// transaction, which needs both the migration's atomic setting and a
	// backend that can roll back DDL.
	AtomicMigration() bool
	// Atomic runs fn inside a transaction on this connection.
	Atomic(fn func() error) error
	// CollectSQL reports whether SQL is being collected instead of run.
	CollectSQL() bool
	// CollectedSQL returns the statements collected so far.
	CollectedSQL() []string
	// AddCollected appends raw lines, such as comments, to the collected
	// SQL.
	AddCollected(lines ...string)
	Execute(sql string, args ...any) error

	CreateModel(m *Model) error
	DeleteModel(m *Model) error
	AddField(m *Model, f *ModelField) error
	RemoveField(m *Model, f *ModelField) error
	AlterField(m *Model, old, new *ModelField, strict bool) error
	AlterDBTable(m *Model, oldTable, newTable string) error
	AlterDBTableComment(m *Model, oldComment, newComment string) error
	AlterUniqueTogether(m *Model, old, new [][]string) error
	AddIndex(m *Model, ix Index) error
	RemoveIndex(m *Model, ix Index) error
	RenameIndex(m *Model, old, new Index) error
	AddConstraint(m *Model, c Constraint) error
	RemoveConstraint(m *Model, c Constraint) error
}

// Ptr returns a pointer to v, for the optional attributes of
// migrations and operations.
func Ptr[T any](v T) *T { return &v }

// BaseOperation supplies the defaults every operation shares and the marker
// method that makes a type an Operation. Embed it in a custom operation and
// implement only what differs from these defaults. The Atomic default is a
// non-nil false: an operation that wants the migration to decide overrides
// it with nil.
//
// It is an embedded field with no exported fields of its own, so it does not
// appear in the deconstruction a migration file is written from.
type BaseOperation struct{}

func (BaseOperation) isOperation()                        {}
func (BaseOperation) MigrationNameFragment() string       { return "" }
func (BaseOperation) Reversible() bool                    { return true }
func (BaseOperation) ReducesToSQL() bool                  { return true }
func (BaseOperation) Atomic() *bool                       { return Ptr(false) }
func (BaseOperation) Elidable() bool                      { return false }
func (BaseOperation) ReferencesModel(string, string) bool { return true }

// baseOp is the alias the operations in this package embed, so that the
// embedded field is unexported and does not show up as a promoted field in
// their documentation.
type baseOp = BaseOperation

// The operations of this package all satisfy Operation.
var (
	_ Operation = (*CreateModel)(nil)
	_ Operation = (*DeleteModel)(nil)
	_ Operation = (*RenameModel)(nil)
	_ Operation = (*AlterModelTable)(nil)
	_ Operation = (*AlterModelTableComment)(nil)
	_ Operation = (*AlterUniqueTogether)(nil)
	_ Operation = (*AlterModelOptions)(nil)
	_ Operation = (*AddIndex)(nil)
	_ Operation = (*RemoveIndex)(nil)
	_ Operation = (*RenameIndex)(nil)
	_ Operation = (*AddConstraint)(nil)
	_ Operation = (*RemoveConstraint)(nil)
	_ Operation = (*AlterConstraint)(nil)
	_ Operation = (*AddField)(nil)
	_ Operation = (*RemoveField)(nil)
	_ Operation = (*AlterField)(nil)
	_ Operation = (*RenameField)(nil)
	_ Operation = (*SeparateDatabaseAndState)(nil)
	_ Operation = (*RunSQL)(nil)
	_ Operation = (*RunGo)(nil)
)

// baseReduce is the reduction every operation falls back to: an elidable
// operation on either side drops out, and otherwise op blocks optimization
// across it.
//
// django: operations/base.py Operation.reduce
func baseReduce(op, other Operation) ([]Operation, ReduceKind) {
	switch {
	case op.Elidable():
		return []Operation{other}, ReduceReplace
	case other.Elidable():
		return []Operation{op}, ReduceReplace
	}
	return nil, ReduceBlock
}

// validateOperations checks the operation arguments the compiler cannot:
// the shape of RunSQL's SQL values, and of a field's one-off default. It is
// what Register calls, so that a migration file with a bad argument fails at
// init rather than part way through a migration.
func validateOperations(ops []Operation) error {
	for _, op := range ops {
		if err := validateOperation(op); err != nil {
			return err
		}
	}
	return nil
}

func validateOperation(op Operation) error {
	switch x := op.(type) {
	case *RunSQL:
		// The shape is now the compiler's business; what it cannot check
		// is that there is any SQL at all.
		if x.SQL == nil {
			return &ValueError{Msg: "RunSQL requires SQL"}
		}
		return validateOperations(x.StateOperations)
	case *SeparateDatabaseAndState:
		if err := validateOperations(x.DatabaseOperations); err != nil {
			return err
		}
		return validateOperations(x.StateOperations)
	case *CreateModel:
		for _, f := range x.Fields {
			if err := checkDefault(x.Name+"."+f.Name, f.Field); err != nil {
				return err
			}
		}
	case *AddField:
		return checkDefault(x.ModelName+"."+x.Name, x.Field)
	case *AlterField:
		return checkDefault(x.ModelName+"."+x.Name, x.Field)
	}
	return nil
}

// checkDefault reports a Field.Default that is neither a literal value nor a
// function with no arguments and a single result. Such a default would be
// written into DDL with the wrong shape.
func checkDefault(name string, f Field) error {
	if f.Default == nil {
		return nil
	}
	if _, ok := f.Default.(*GoExpr); ok {
		return nil
	}
	t := reflect.TypeOf(f.Default)
	if t.Kind() != reflect.Func {
		return nil
	}
	if t.IsVariadic() || t.NumIn() != 0 || t.NumOut() != 1 {
		return &ValueError{Msg: fmt.Sprintf("field %s: Default must be a literal value or a function with no arguments and a single result, not %s", name, t)}
	}
	return nil
}

// FormattedDescription prefixes the description with the category symbol.
//
// django: operations/base.py Operation.formatted_description
func FormattedDescription(op Operation) string {
	c := op.Category()
	if c == "" {
		c = CategoryMixed
	}
	return string(c) + " " + op.Describe()
}

// allowMigrateModel reports whether m may be migrated: an unmanaged model
// never is, and otherwise the routers decide.
//
// django: operations/base.py Operation.allow_migrate_model
func allowMigrateModel(conn Connection, m *Model) bool {
	if !m.Managed() {
		return false
	}
	return conn.AllowMigrate(m.App, Hints{Model: m, ModelName: strings.ToLower(m.Name)})
}

// OpName returns the Go type name of op, with any pointer indirection
// stripped. It is the name that appears in operation descriptions and in the
// output of FormatOperation.
func OpName(op Operation) string {
	t := reflect.TypeOf(op)
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t.Name()
}

// FormatOperation renders an operation and its deconstructed arguments on a
// single line, for error messages and test output.
//
// django: operations/base.py Operation.__repr__
func FormatOperation(op Operation) string {
	var parts []string
	for _, kv := range deconstructStruct(op) {
		parts = append(parts, fmt.Sprintf(" %s=%s", kv.Key, formatReflect(reflect.ValueOf(kv.Value))))
	}
	return "<" + OpName(op) + strings.Join(parts, ",") + ">"
}

var typeOfField = reflect.TypeOf(Field{})

// formatReflect renders one deconstructed argument. Pointers are followed so
// that the result describes the value rather than its address, and maps are
// rendered in key order, which together keep FormatOperation deterministic
// and comparable.
//
// django: operations/base.py Operation.__repr__
func formatReflect(rv reflect.Value) string {
	if !rv.IsValid() {
		return "<nil>"
	}
	if rv.Type() == typeOfField {
		return "<field>"
	}
	switch rv.Kind() {
	case reflect.Func:
		if rv.IsNil() {
			return "<nil>"
		}
		return "<function " + FuncName(rv.Interface()) + ">"
	case reflect.Pointer, reflect.Interface:
		if rv.IsNil() {
			return "<nil>"
		}
		return formatReflect(rv.Elem())
	case reflect.String:
		return "'" + rv.String() + "'"
	case reflect.Slice, reflect.Array:
		if rv.Kind() == reflect.Slice && rv.IsNil() {
			return "<nil>"
		}
		parts := make([]string, rv.Len())
		for i := range parts {
			parts[i] = formatReflect(rv.Index(i))
		}
		return "[" + strings.Join(parts, " ") + "]"
	case reflect.Map:
		keys := rv.MapKeys()
		slices.SortFunc(keys, func(a, b reflect.Value) int {
			return cmp.Compare(fmt.Sprint(a.Interface()), fmt.Sprint(b.Interface()))
		})
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, formatReflect(k)+": "+formatReflect(rv.MapIndex(k)))
		}
		return "{" + strings.Join(parts, " ") + "}"
	case reflect.Struct:
		t := rv.Type()
		var parts []string
		for i := range t.NumField() {
			sf := t.Field(i)
			if !sf.IsExported() || sf.Anonymous {
				continue
			}
			parts = append(parts, sf.Name+"="+formatReflect(rv.Field(i)))
		}
		return "{" + strings.Join(parts, " ") + "}"
	}
	return fmt.Sprintf("%v", rv.Interface())
}
