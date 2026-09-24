package writer

// Port of Django 6.0's tests/migrations/test_writer.py.
//
// Each test keeps the name of the Django test it mirrors so the
// correspondence stays auditable. Where Django's test exercises a
// Python-only feature (lambdas, functools.partial, decimal, uuid, pathlib,
// enum flags, settings, managers, frozensets, ...) the equivalent Go
// behaviour is asserted instead and the test comment says so.

import (
	"errors"
	"flag"
	"fmt"
	"go/build"
	"go/format"
	"go/parser"
	"go/token"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	m "github.com/ctolon/gormgate/migrations"
)

var update = flag.Bool("update", false, "rewrite the golden files under testdata")

// writerPkgPath is the import path of this package; generated migrations that
// declare it as their PackagePath refer to its functions without an import.
var writerPkgPath = reflect.TypeOf(Writer{}).PkgPath()

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// serializeValue is the Go analogue of MigrationWriter.serialize(value): it
// returns the generated expression and the import paths it needs (the
// migrations package itself is always imported and is left out).
func serializeValue(t *testing.T, value any) (string, []string) {
	t.Helper()
	s := &serializer{w: &Writer{}, im: newImports(), manual: map[string]bool{}}
	code, err := s.value(reflect.ValueOf(value))
	if err != nil {
		t.Fatalf("serializing %#v: %v", value, err)
	}
	var paths []string
	for p := range s.im.byPath {
		if p != MigrationsImport {
			paths = append(paths, p)
		}
	}
	return gofmtExpr(t, code), sortedStrings(paths)
}

func sortedStrings(in []string) []string {
	out := append([]string(nil), in...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// gofmtExpr is the Go analogue of Django's safe_exec: it proves the generated
// expression is syntactically valid Go, and normalizes its layout so the
// expected values in this file can be written readably.
func gofmtExpr(t *testing.T, code string) string {
	t.Helper()
	const prefix = "package p\n\nvar _ = "
	out, err := format.Source([]byte(prefix + code + "\n"))
	if err != nil {
		t.Fatalf("generated expression does not parse: %v\n%s", err, code)
	}
	return strings.TrimSuffix(strings.TrimPrefix(string(out), prefix), "\n")
}

// assertSerializedResultEqual mirrors WriterTests.assertSerializedResultEqual.
func assertSerializedResultEqual(t *testing.T, value any, want string, wantImports ...string) {
	t.Helper()
	got, imports := serializeValue(t, value)
	if got != want {
		t.Errorf("serialize(%#v)\n got: %s\nwant: %s", value, got, want)
	}
	if len(imports) != len(wantImports) {
		t.Errorf("serialize(%#v) imports = %v, want %v", value, imports, wantImports)
		return
	}
	for i := range imports {
		if imports[i] != wantImports[i] {
			t.Errorf("serialize(%#v) imports = %v, want %v", value, imports, wantImports)
			return
		}
	}
}

// assertSerializeError mirrors the assertRaisesMessage(ValueError, ...) calls.
func assertSerializeError(t *testing.T, value any, wantMsg string) {
	t.Helper()
	s := &serializer{w: &Writer{}, im: newImports(), manual: map[string]bool{}}
	code, err := s.value(reflect.ValueOf(value))
	if err == nil {
		t.Fatalf("serializing %#v: expected an error, got %s", value, code)
	}
	var serr *SerializeError
	if !errors.As(err, &serr) {
		t.Fatalf("serializing %#v: error is %T, want *SerializeError", value, err)
	}
	if !strings.Contains(err.Error(), wantMsg) {
		t.Errorf("serializing %#v: error %q does not contain %q", value, err, wantMsg)
	}
}

// renderMigration renders a migration and checks it is gofmt-clean and
// parses; every test that produces a file goes through it.
func renderMigration(t *testing.T, w *Writer) string {
	t.Helper()
	out, err := w.AsString()
	if err != nil {
		t.Fatalf("AsString: %v", err)
	}
	if _, err := parser.ParseFile(token.NewFileSet(), "migration.go", out, parser.AllErrors); err != nil {
		t.Fatalf("generated migration does not parse: %v\n%s", err, out)
	}
	formatted, err := format.Source(out)
	if err != nil {
		t.Fatalf("gofmt of generated migration failed: %v\n%s", err, out)
	}
	if string(formatted) != string(out) {
		t.Errorf("generated migration is not gofmt-clean:\n--- got ---\n%s\n--- gofmt ---\n%s", out, formatted)
	}
	return string(out)
}

// checkGolden compares got with testdata/<name>.go.golden, rewriting it under
// -update.
func checkGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".go.golden")
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run `go test ./internal/writer -update` to create it)", err)
	}
	if string(want) != got {
		t.Errorf("generated migration differs from %s\n--- got ---\n%s\n--- want ---\n%s", path, got, want)
	}
}

// goBuild compiles src as a package in a throwaway module that points at this
// repository, proving the generated migration is valid Go that links
// against the migrations API.
func goBuild(t *testing.T, fileName, src string) {
	t.Helper()
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("go toolchain not available: %v", err)
	}
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	gomod := fmt.Sprintf("module buildtest\n\ngo 1.26\n\nrequire github.com/ctolon/gormgate v0.0.0\n\nreplace github.com/ctolon/gormgate => %s\n", repo)
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(gomod), 0o644); err != nil {
		t.Fatal(err)
	}
	sum, err := os.ReadFile(filepath.Join(repo, "go.sum"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.sum"), sum, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, fileName), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(goBin, "build", "./...")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod", "GOPROXY=off")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build of generated migration failed: %v\n%s\n--- source ---\n%s", err, out, src)
	}
}

// packageFunc and friends are the fixtures the function serializer tests use.
func packageFunc(apps *m.Apps, ed m.SchemaEditor) error { return nil }

func packageReverse(apps *m.Apps, ed m.SchemaEditor) error { return nil }

// decoratedFunc stands in for Django's @functools.wraps-decorated function:
// a package-level function reached through another package-level identifier
// still serializes to the name of the function it refers to.
var decoratedFunc = packageFunc

// fixtureModel has a method used as a value, which Go cannot write back out.
type fixtureModel struct{}

func (fixtureModel) UploadTo(apps *m.Apps, ed m.SchemaEditor) error { return nil }

func (*fixtureModel) PointerUploadTo(apps *m.Apps, ed m.SchemaEditor) error { return nil }

// TestOperation is a migration operation defined outside the migrations
// package, like Django's custom_migration_operations.operations.TestOperation.
// It embeds m.BaseOperation, which is how an operation outside the migrations
// package is written: everything it does not override comes from there, and
// the embedded field does not appear in the generated file.
type TestOperation struct {
	m.BaseOperation
	Arg1 string
	Arg2 int
}

var _ m.Operation = (*TestOperation)(nil)

func (o *TestOperation) StateForwards(string, *m.ProjectState) error { return nil }
func (o *TestOperation) DatabaseForwards(string, m.SchemaEditor, *m.ProjectState, *m.ProjectState) error {
	return nil
}
func (o *TestOperation) DatabaseBackwards(string, m.SchemaEditor, *m.ProjectState, *m.ProjectState) error {
	return nil
}
func (o *TestOperation) Describe() string     { return "Test operation" }
func (o *TestOperation) Category() m.Category { return m.CategoryMixed }
func (o *TestOperation) ReferencesField(string, string, string) bool {
	return true
}
func (o *TestOperation) Reduce(m.Operation, string) ([]m.Operation, m.ReduceKind) {
	return nil, m.ReduceBlock
}

// Atomic returns nil, unlike BaseOperation's non-nil false: the migration
// decides.
func (o *TestOperation) Atomic() *bool { return nil }

// ---------------------------------------------------------------------------
// OperationWriterTests
//
// Django's OperationWriter renders an operation as a constructor call with one
// keyword argument per line. gormgate renders a Go composite literal with one
// struct field per line; the tests below assert the same properties: the
// operation's own type is used, only the arguments that were set appear, and
// nested values are indented.
// ---------------------------------------------------------------------------

// django: tests/migrations/test_writer.py OperationWriterTests.test_empty_signature
func TestOperationWriter_EmptySignature(t *testing.T) {
	// An operation with no arguments set renders as an empty literal.
	assertSerializedResultEqual(t, &TestOperation{}, "&writer.TestOperation{}", writerPkgPath)
}

// django: tests/migrations/test_writer.py OperationWriterTests.test_args_signature
func TestOperationWriter_ArgsSignature(t *testing.T) {
	assertSerializedResultEqual(t, &TestOperation{Arg1: "one", Arg2: 2},
		"&writer.TestOperation{\n\tArg1: \"one\",\n\tArg2: 2,\n}", writerPkgPath)
}

// django: tests/migrations/test_writer.py OperationWriterTests.test_kwargs_signature
func TestOperationWriter_KwargsSignature(t *testing.T) {
	// Go has no positional arguments: every operation attribute is written as
	// a named struct field, and unset ones are omitted.
	assertSerializedResultEqual(t, &TestOperation{Arg2: 1},
		"&writer.TestOperation{\n\tArg2: 1,\n}", writerPkgPath)
}

// django: tests/migrations/test_writer.py OperationWriterTests.test_args_kwargs_signature
func TestOperationWriter_ArgsKwargsSignature(t *testing.T) {
	assertSerializedResultEqual(t, &m.AlterField{ModelName: "mymodel", Name: "myfield",
		Field: m.Field{Type: m.Int}, PreserveDefault: m.Ptr(false)},
		"&m.AlterField{\n\tModelName: \"mymodel\",\n\tName:      \"myfield\",\n\tField: m.Field{\n\t\tType: m.Int,\n\t},\n\tPreserveDefault: m.Ptr(false),\n}")
}

// django: tests/migrations/test_writer.py OperationWriterTests.test_keyword_only_args_signature
func TestOperationWriter_KeywordOnlyArgsSignature(t *testing.T) {
	// Fields are written in declaration order, not alphabetically.
	got, _ := serializeValue(t, &m.RenameField{ModelName: "mymodel", OldName: "old", NewName: "new"})
	want := "&m.RenameField{\n\tModelName: \"mymodel\",\n\tOldName:   \"old\",\n\tNewName:   \"new\",\n}"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

// django: tests/migrations/test_writer.py OperationWriterTests.test_nested_args_signature
func TestOperationWriter_NestedArgsSignature(t *testing.T) {
	op := &m.SeparateDatabaseAndState{
		DatabaseOperations: []m.Operation{&m.DeleteModel{Name: "One"}},
		StateOperations:    []m.Operation{&m.RenameModel{OldName: "Two", NewName: "Three"}},
	}
	assertSerializedResultEqual(t, op,
		"&m.SeparateDatabaseAndState{\n"+
			"\tDatabaseOperations: []m.Operation{\n"+
			"\t\t&m.DeleteModel{\n\t\t\tName: \"One\",\n\t\t},\n"+
			"\t},\n"+
			"\tStateOperations: []m.Operation{\n"+
			"\t\t&m.RenameModel{\n\t\t\tOldName: \"Two\",\n\t\t\tNewName: \"Three\",\n\t\t},\n"+
			"\t},\n}")
}

// django: tests/migrations/test_writer.py OperationWriterTests.test_multiline_args_signature
func TestOperationWriter_MultilineArgsSignature(t *testing.T) {
	op := &m.RunSQL{SQL: m.Script("test\n    arg1"), ReverseSQL: m.Script("test\narg2")}
	assertSerializedResultEqual(t, op,
		"&m.RunSQL{\n\tSQL:        m.Script(\"test\\n    arg1\"),\n\tReverseSQL: m.Script(\"test\\narg2\"),\n}")
}

// django: tests/migrations/test_writer.py OperationWriterTests.test_expand_args_signature
func TestOperationWriter_ExpandArgsSignature(t *testing.T) {
	op := &m.RunSQL{SQL: m.Statements{"SELECT 1", "SELECT 2"}}
	assertSerializedResultEqual(t, op,
		"&m.RunSQL{\n\tSQL: m.Statements{\n\t\t\"SELECT 1\",\n\t\t\"SELECT 2\",\n\t},\n}")
}

// django: tests/migrations/test_writer.py OperationWriterTests.test_nested_operation_expand_args_signature
func TestOperationWriter_NestedOperationExpandArgsSignature(t *testing.T) {
	op := &m.SeparateDatabaseAndState{
		StateOperations: []m.Operation{
			&m.RemoveField{ModelName: "mymodel", Name: "one"},
			&m.RemoveField{ModelName: "mymodel", Name: "two"},
		},
	}
	assertSerializedResultEqual(t, op,
		"&m.SeparateDatabaseAndState{\n"+
			"\tStateOperations: []m.Operation{\n"+
			"\t\t&m.RemoveField{\n\t\t\tModelName: \"mymodel\",\n\t\t\tName:      \"one\",\n\t\t},\n"+
			"\t\t&m.RemoveField{\n\t\t\tModelName: \"mymodel\",\n\t\t\tName:      \"two\",\n\t\t},\n"+
			"\t},\n}")
}

// ---------------------------------------------------------------------------
// WriterTests: serializer
// ---------------------------------------------------------------------------

// django: tests/migrations/test_writer.py WriterTests.test_serialize_numbers
func TestWriter_SerializeNumbers(t *testing.T) {
	// Every number keeps its exact Go type so the value read back from the
	// compiled migration compares equal; Python's single int/float types have
	// no such requirement.
	for _, tc := range []struct {
		value any
		want  string
	}{
		{1, "1"},
		{int8(3), "int8(3)"},
		{int16(3), "int16(3)"},
		{int32(3), "int32(3)"},
		{int64(5), "int64(5)"},
		{uint(7), "uint(7)"},
		{uint8(7), "uint8(7)"},
		{uint16(7), "uint16(7)"},
		{uint32(7), "uint32(7)"},
		{uint64(7), "uint64(7)"},
		{1.2, "1.2"},
		{float32(1.5), "float32(1.5)"},
		{2.0, "2.0"},
		{1e21, "1e+21"},
	} {
		assertSerializedResultEqual(t, tc.value, tc.want)
	}
	// Django writes float("inf")/float("nan"); Go has no such literals, so
	// the calls that produce them are written and math is imported.
	assertSerializedResultEqual(t, math.Inf(1), "math.Inf(1)", "math")
	assertSerializedResultEqual(t, math.Inf(-1), "math.Inf(-1)", "math")
	assertSerializedResultEqual(t, math.NaN(), "math.NaN()", "math")
	assertSerializedResultEqual(t, float32(math.Inf(1)), "float32(math.Inf(1))", "math")
	// Django's Money(decimal.Decimal) subclass: a named type over a builtin
	// is written with its conversion so the type survives the round trip.
	assertSerializedResultEqual(t, m.DataType("jsonb"), `"jsonb"`)
}

// django: tests/migrations/test_writer.py WriterTests.test_serialize_constants
func TestWriter_SerializeConstants(t *testing.T) {
	assertSerializedResultEqual(t, nil, "nil")
	assertSerializedResultEqual(t, true, "true")
	assertSerializedResultEqual(t, false, "false")
}

// django: tests/migrations/test_writer.py WriterTests.test_serialize_strings
func TestWriter_SerializeStrings(t *testing.T) {
	assertSerializedResultEqual(t, []byte("foobar"), `[]byte("foobar")`)
	assertSerializedResultEqual(t, "foobar", `"foobar"`)
	assertSerializedResultEqual(t, "föobár", `"föobár"`)
}

// django: tests/migrations/test_writer.py WriterTests.test_serialize_multiline_strings
func TestWriter_SerializeMultilineStrings(t *testing.T) {
	assertSerializedResultEqual(t, []byte("foo\nbar"), `[]byte("foo\nbar")`)
	assertSerializedResultEqual(t, "foo\nbar", `"foo\nbar"`)
	assertSerializedResultEqual(t, "föo\nbár", `"föo\nbár"`)
	assertSerializedResultEqual(t, "tab\there \"quoted\"", `"tab\there \"quoted\""`)
}

// django: tests/migrations/test_writer.py WriterTests.test_serialize_collections
func TestWriter_SerializeCollections(t *testing.T) {
	assertSerializedResultEqual(t, map[string]int{"one": 2}, "map[string]int{\n\t\"one\": 2,\n}")
	assertSerializedResultEqual(t, []string{"a", "b"}, "[]string{\n\t\"a\",\n\t\"b\",\n}")
	// Django sorts sets so the output is deterministic; gormgate sorts map
	// keys for the same reason.
	assertSerializedResultEqual(t, map[string]string{"c": "3", "b": "2", "a": "1"},
		"map[string]string{\n\t\"a\": \"1\",\n\t\"b\": \"2\",\n\t\"c\": \"3\",\n}")
	assertSerializedResultEqual(t, map[string][]string{"lalalala": {"yeah", "no", "maybe"}},
		"map[string][]string{\n\t\"lalalala\": []string{\n\t\t\"yeah\",\n\t\t\"no\",\n\t\t\"maybe\",\n\t},\n}")
}

// django: tests/migrations/test_writer.py WriterTests.test_serialize_empty_nonempty_tuple
func TestWriter_SerializeEmptyNonemptyTuple(t *testing.T) {
	assertSerializedResultEqual(t, []string{}, "[]string{}")
	assertSerializedResultEqual(t, []string{"a"}, "[]string{\n\t\"a\",\n}")
	assertSerializedResultEqual(t, []string{"a", "b", "c"}, "[]string{\n\t\"a\",\n\t\"b\",\n\t\"c\",\n}")
	// A nil slice is not an empty slice in Go.
	assertSerializedResultEqual(t, []string(nil), "nil")
}

// django: tests/migrations/test_writer.py WriterTests.test_serialize_enums
func TestWriter_SerializeEnums(t *testing.T) {
	// m.DataType is gormgate's enum. Its named members are written as the
	// constant, like Django writes TextEnum['A'] rather than its value.
	for value, want := range map[m.DataType]string{
		m.Bool: "m.Bool", m.Int: "m.Int", m.Uint: "m.Uint", m.Float: "m.Float",
		m.String: "m.String", m.Time: "m.Time", m.Bytes: "m.Bytes",
	} {
		assertSerializedResultEqual(t, value, want)
	}
	// A database type given through gorm's `type:` tag is not a member of the
	// enum and is written as the plain string it is.
	assertSerializedResultEqual(t, m.DataType("tsvector"), `"tsvector"`)
	// The enum inside a field.
	assertSerializedResultEqual(t, m.Field{Type: m.Bytes}, "m.Field{\n\tType: m.Bytes,\n}")
}

// django: tests/migrations/test_writer.py WriterTests.test_serialize_nested_class
func TestWriter_SerializeNestedClass(t *testing.T) {
	// Django serializes a class by its import path. gormgate's equivalent of
	// passing a class around is m.Custom[T](), which records the Go type of a
	// column implementing gorm's GormDataTypeInterface.
	assertSerializedResultEqual(t, m.Custom[time.Time](), "m.Custom[time.Time]()", "time")
	assertSerializedResultEqual(t, m.Custom[build.Context](), "m.Custom[build.Context]()", "go/build")
	// A type declared in the main package (or another package with no import
	// path) cannot be written into a migration file.
	assertSerializeError(t, &m.TypeRef{Name: "Local"}, "cannot serialize custom type Local")
}

// django: tests/migrations/test_writer.py WriterTests.test_serialize_nested_class_method
func TestWriter_SerializeNestedClassMethod(t *testing.T) {
	// Django can serialize a nested class method by its qualified name. Go
	// method values have no package-level name, so the writer refuses them
	// with an explanation instead of writing code that does not compile.
	var fixture fixtureModel
	assertSerializeError(t, m.RunGoFunc(fixture.UploadTo),
		"you cannot serialize method values; use a package-level function")
	assertSerializeError(t, m.RunGoFunc((&fixture).PointerUploadTo),
		"you cannot serialize method values; use a package-level function")
}

// django: tests/migrations/test_writer.py WriterTests.test_serialize_unbound_method_reference
func TestWriter_SerializeUnboundMethodReference(t *testing.T) {
	// Django serializes an unbound method used inside a class body. The Go
	// equivalent is a method expression, which again has no package-level
	// name.
	// A method expression takes the receiver as its first argument, so it is
	// not even a RunGoFunc; it is passed as a plain function value here.
	assertSerializeError(t, fixtureModel.UploadTo,
		"you cannot serialize method values; use a package-level function")
}

// django: tests/migrations/test_writer.py WriterTests.test_serialize_local_function_reference
func TestWriter_SerializeLocalFunctionReference(t *testing.T) {
	// "A reference in a local scope can't be serialized."
	local := func(apps *m.Apps, ed m.SchemaEditor) error { return nil }
	assertSerializeError(t, m.RunGoFunc(local), "cannot serialize function: function literal")
}

// django: tests/migrations/test_writer.py WriterTests.test_serialize_functions
func TestWriter_SerializeFunctions(t *testing.T) {
	// Django: a lambda cannot be serialized, a module-level function can.
	assertSerializeError(t, m.RunGoFunc(func(apps *m.Apps, ed m.SchemaEditor) error { return nil }),
		"cannot serialize function: function literal")
	assertSerializedResultEqual(t, m.RunGoFunc(packageFunc), "writer.packageFunc", writerPkgPath)
	// Functions of the migrations package itself need no import.
	assertSerializedResultEqual(t, m.RunGoFunc(m.RunGoNoop), "m.RunGoNoop")
	// A nil function is a missing reverse operation, not an error.
	assertSerializedResultEqual(t, m.RunGoFunc(nil), "nil")
}

// django: tests/migrations/test_writer.py WriterTests.test_serialize_decorated_functions
func TestWriter_SerializeDecoratedFunctions(t *testing.T) {
	// Django relies on functools.wraps keeping __qualname__. In Go a function
	// reached through another identifier still reports the name of the
	// function it refers to.
	assertSerializedResultEqual(t, m.RunGoFunc(decoratedFunc), "writer.packageFunc", writerPkgPath)
}

// django: tests/migrations/test_writer.py WriterTests.test_serialize_datetime
func TestWriter_SerializeDatetime(t *testing.T) {
	assertSerializedResultEqual(t,
		time.Date(2014, 1, 1, 1, 1, 0, 0, time.UTC),
		"time.Date(2014, time.January, 1, 1, 1, 0, 0, time.UTC)", "time")
	assertSerializedResultEqual(t,
		time.Date(2012, 1, 1, 1, 1, 0, 123, time.UTC),
		"time.Date(2012, time.January, 1, 1, 1, 0, 123, time.UTC)", "time")
	// A function that produces a time, Django's datetime.datetime.now.
	assertSerializedResultEqual(t, m.RunGoFunc(packageFunc), "writer.packageFunc", writerPkgPath)
}

// django: tests/migrations/test_writer.py WriterTests.test_serialize_zoneinfo
func TestWriter_SerializeZoneinfo(t *testing.T) {
	// Django writes zoneinfo.ZoneInfo(key=...); Go's equivalent of a time
	// zone attached to a value is its *time.Location.
	assertSerializedResultEqual(t,
		time.Date(2013, 12, 31, 22, 1, 0, 0, time.FixedZone("IST", 330*60)),
		`time.Date(2013, time.December, 31, 22, 1, 0, 0, time.FixedZone("IST", 19800))`, "time")
	assertSerializedResultEqual(t,
		time.Date(2013, 12, 31, 22, 1, 0, 0, time.FixedZone("", 180*60)),
		`time.Date(2013, time.December, 31, 22, 1, 0, 0, time.FixedZone("", 10800))`, "time")
	assertSerializedResultEqual(t,
		time.Date(2013, 12, 31, 22, 1, 0, 0, time.Local),
		"time.Date(2013, time.December, 31, 22, 1, 0, 0, time.Local)", "time")
}

// django: tests/migrations/test_writer.py WriterTests.test_serialize_timedelta
func TestWriter_SerializeTimedelta(t *testing.T) {
	assertSerializedResultEqual(t, time.Duration(0), "time.Duration(0)", "time")
	assertSerializedResultEqual(t, 42*time.Minute, "time.Duration(2520000000000)", "time")
}

// django: tests/migrations/test_writer.py WriterTests.test_serialize_type_none
func TestWriter_SerializeTypeNone(t *testing.T) {
	assertSerializedResultEqual(t, (*m.DBDefault)(nil), "nil")
	assertSerializedResultEqual(t, (*bool)(nil), "nil")
	assertSerializedResultEqual(t, m.Constraint(nil), "nil")
	assertSerializedResultEqual(t, map[string]string(nil), "nil")
}

// django: tests/migrations/test_writer.py WriterTests.test_serialize_fields
func TestWriter_SerializeFields(t *testing.T) {
	// Only the attributes that differ from the zero value are written, like
	// Django only writes the kwargs that differ from the field defaults.
	assertSerializedResultEqual(t, m.Field{Type: m.String, Size: 255},
		"m.Field{\n\tType: m.String,\n\tSize: 255,\n}")
	assertSerializedResultEqual(t, m.Field{Type: m.String, Null: true, Unique: true},
		"m.Field{\n\tType:   m.String,\n\tNull:   true,\n\tUnique: true,\n}")
	assertSerializedResultEqual(t, m.Field{Type: m.Int, ForeignKey: &m.ForeignKey{To: "testapp.Author", OnDelete: m.Cascade}},
		"m.Field{\n\tType: m.Int,\n\tForeignKey: &m.ForeignKey{\n\t\tTo:       \"testapp.Author\",\n\t\tOnDelete: \"CASCADE\",\n\t},\n}")
	// The database default and the one-off default of a field.
	assertSerializedResultEqual(t, m.Field{Type: m.Int, DBDefault: m.DBValue(3)},
		"m.Field{\n\tType:      m.Int,\n\tDBDefault: m.DBValue(3),\n}")
	assertSerializedResultEqual(t, m.Field{Type: m.Time, DBDefault: m.DBExpr("CURRENT_TIMESTAMP")},
		"m.Field{\n\tType:      m.Time,\n\tDBDefault: m.DBExpr(\"CURRENT_TIMESTAMP\"),\n}")
	// A default entered at the makemigrations prompt is written verbatim and
	// brings its import with it.
	assertSerializedResultEqual(t, m.Field{Type: m.Time, Default: &m.GoExpr{Source: "time.Now"}},
		"m.Field{\n\tType:    m.Time,\n\tDefault: time.Now,\n}", "time")
}

// django: tests/migrations/test_writer.py WriterTests.test_serialize_class_based_validators
func TestWriter_SerializeClassBasedValidators(t *testing.T) {
	// Django's deconstructible instances become a constructor call with the
	// arguments that were given. gormgate's constraints are the same idea:
	// a pointer to a struct with only its non-zero fields written out.
	assertSerializedResultEqual(t, &m.CheckConstraint{Name: "age_gte_0", Check: "age >= 0"},
		"&m.CheckConstraint{\n\tName:  \"age_gte_0\",\n\tCheck: \"age >= 0\",\n}")
	assertSerializedResultEqual(t,
		&m.UniqueConstraint{Name: "u", Fields: []string{"a", "b"}, Condition: "deleted IS NULL", NullsDistinct: m.Ptr(false)},
		"&m.UniqueConstraint{\n\tName: \"u\",\n\tFields: []string{\n\t\t\"a\",\n\t\t\"b\",\n\t},\n"+
			"\tCondition:     \"deleted IS NULL\",\n\tNullsDistinct: m.Ptr(false),\n}")
	// A constraint referenced through the interface serializes as its
	// concrete type.
	assertSerializedResultEqual(t, m.Constraint(&m.CheckConstraint{Name: "c", Check: "x"}),
		"&m.CheckConstraint{\n\tName:  \"c\",\n\tCheck: \"x\",\n}")
}

// django: tests/migrations/test_writer.py WriterTests.test_serialize_complex_func_index
func TestWriter_SerializeComplexFuncIndex(t *testing.T) {
	index := m.Index{
		Name: "complex_func_index",
		Fields: []m.IndexField{
			{Expression: "ABS(rating)"},
			{Column: "name", Sort: "DESC", Collate: "C"},
			{Column: "pages", Length: 10},
		},
		Type:      "btree",
		Where:     "pages > 0",
		Include:   []string{"isbn"},
		OpClasses: []string{"text_pattern_ops"},
	}
	assertSerializedResultEqual(t, index,
		"m.Index{\n"+
			"\tName: \"complex_func_index\",\n"+
			"\tFields: []m.IndexField{\n"+
			"\t\t{\n\t\t\tExpression: \"ABS(rating)\",\n\t\t},\n"+
			"\t\t{\n\t\t\tColumn:  \"name\",\n\t\t\tSort:    \"DESC\",\n\t\t\tCollate: \"C\",\n\t\t},\n"+
			"\t\t{\n\t\t\tColumn: \"pages\",\n\t\t\tLength: 10,\n\t\t},\n"+
			"\t},\n"+
			"\tType:  \"btree\",\n"+
			"\tWhere: \"pages > 0\",\n"+
			"\tInclude: []string{\n\t\t\"isbn\",\n\t},\n"+
			"\tOpClasses: []string{\n\t\t\"text_pattern_ops\",\n\t},\n"+
			"}")
}

// django: tests/migrations/test_writer.py WriterTests.test_register_serializer
//
// Django's registry allows a project to teach the writer about a new type;
// gormgate has no such registry, so only the other half of that test is
// ported: a value the writer does not understand is refused with
// "Cannot serialize" instead of producing broken code.
func TestWriter_SerializeUnserializableValue(t *testing.T) {
	assertSerializeError(t, complex(1, 2), "cannot serialize: (1+2i)")
	assertSerializeError(t, m.Field{Type: m.Int, Tags: map[string]string{"x": "y"}, Default: make(chan int)},
		"cannot serialize:")
}

// django: tests/migrations/test_writer.py WriterTests.test_deconstruct_class_arguments
func TestWriter_DeconstructClassArguments(t *testing.T) {
	// A Go type used as a field argument, the equivalent of Django's class
	// passed as a default.
	assertSerializedResultEqual(t, m.Field{Type: m.String, Custom: m.Custom[build.Context]()},
		"m.Field{\n\tType:   m.String,\n\tCustom: m.Custom[build.Context](),\n}", "go/build")
}

// ---------------------------------------------------------------------------
// WriterTests: whole migration files
// ---------------------------------------------------------------------------

func simpleMigration() *m.Migration {
	field := m.Field{Type: m.Time, Default: &m.GoExpr{Source: "time.Now"}}
	fields := m.Fields{
		{Name: "charfield", Field: m.Field{Type: m.String, Size: 100}},
		{Name: "datetimefield", Field: field},
	}
	return &m.Migration{
		App:  "testapp",
		Name: "0002_simple",
		Operations: []m.Operation{
			&m.CreateModel{Name: "MyModel", Table: "testapp_mymodel", Fields: fields},
			&m.CreateModel{Name: "MyModel2", Table: "testapp_mymodel2", Fields: fields},
			&m.CreateModel{Name: "MyModel3", Table: "testapp_mymodel3", Fields: fields,
				Options: m.Options{Managed: m.Ptr(false)}},
			&m.DeleteModel{Name: "MyModel"},
			&m.AddField{ModelName: "OtherModel", Name: "datetimefield", Field: field},
		},
		Dependencies: []m.Key{{App: "testapp", Name: "some_other_one"}},
	}
}

// django: tests/migrations/test_writer.py WriterTests.test_simple_migration
func TestWriter_SimpleMigration(t *testing.T) {
	w := &Writer{Migration: simpleMigration()}
	out := renderMigration(t, w)
	checkGolden(t, "simple_migration", out)
	if w.NeedsManualPorting {
		t.Error("NeedsManualPorting should be false")
	}
	goBuild(t, "0002_simple.go", out)
}

// django: tests/migrations/test_writer.py WriterTests.test_custom_operation
func TestWriter_CustomOperation(t *testing.T) {
	mig := &m.Migration{
		App:  "testapp",
		Name: "0001_initial",
		Operations: []m.Operation{
			&TestOperation{},
			&TestOperation{Arg1: "x", Arg2: 1},
			&m.CreateModel{Name: "MyModel", Table: "testapp_mymodel", Fields: m.Fields{}},
		},
	}
	out := renderMigration(t, &Writer{Migration: mig})
	// The operation's own package is imported next to the migrations API.
	if !strings.Contains(out, `"github.com/ctolon/gormgate/internal/writer"`) {
		t.Errorf("the custom operation's package is not imported:\n%s", out)
	}
	if !strings.Contains(out, "&writer.TestOperation{}") {
		t.Errorf("the custom operation is not rendered with its package:\n%s", out)
	}
	checkGolden(t, "custom_operation", out)
}

// django: tests/migrations/test_writer.py WriterTests.test_sorted_dependencies
func TestWriter_SortedDependencies(t *testing.T) {
	mig := &m.Migration{
		App:  "testapp",
		Name: "0002_x",
		Operations: []m.Operation{
			&m.AddField{ModelName: "mymodel", Name: "myfield", Field: m.Field{Type: m.Int}},
		},
		Dependencies: []m.Key{
			{App: "testapp10", Name: "0005_fifth"},
			{App: "testapp02", Name: "0005_third"},
			{App: "testapp02", Name: "0004_sixth"},
			{App: "testapp01", Name: "0001_initial"},
		},
	}
	out := renderMigration(t, &Writer{Migration: mig})
	want := "\t\tDependencies: []m.Key{\n" +
		"\t\t\t{App: \"testapp01\", Name: \"0001_initial\"},\n" +
		"\t\t\t{App: \"testapp02\", Name: \"0004_sixth\"},\n" +
		"\t\t\t{App: \"testapp02\", Name: \"0005_third\"},\n" +
		"\t\t\t{App: \"testapp10\", Name: \"0005_fifth\"},\n" +
		"\t\t},\n"
	if !strings.Contains(out, want) {
		t.Errorf("dependencies are not sorted:\n%s", out)
	}
	// The migration's own dependency list is not reordered in place.
	if mig.Dependencies[0].App != "testapp10" {
		t.Errorf("AsString reordered Migration.Dependencies: %v", mig.Dependencies)
	}
}

// django: tests/migrations/test_writer.py WriterTests.test_sorted_imports
func TestWriter_SortedImports(t *testing.T) {
	mig := &m.Migration{
		App:  "testapp",
		Name: "0002_imports",
		Operations: []m.Operation{
			&m.AddField{ModelName: "mymodel", Name: "myfield",
				Field: m.Field{Type: m.Time, Default: time.Date(2012, 1, 1, 1, 1, 0, 0, time.UTC)}},
			&m.AddField{ModelName: "mymodel", Name: "myfield2",
				Field: m.Field{Type: m.Float, Default: math.Inf(1)}},
		},
	}
	out := renderMigration(t, &Writer{Migration: mig})
	want := "import (\n" +
		"\tm \"github.com/ctolon/gormgate/migrations\"\n" +
		"\t\"math\"\n" +
		"\t\"time\"\n" +
		")\n"
	if !strings.Contains(out, want) {
		t.Errorf("imports are not sorted:\n%s", out)
	}
	checkGolden(t, "sorted_imports", out)
	goBuild(t, "0002_imports.go", out)
}

// django: tests/migrations/test_writer.py WriterTests.test_models_import_omitted
func TestWriter_ModelsImportOmitted(t *testing.T) {
	// The time package is not imported when nothing needs it.
	mig := &m.Migration{
		App:  "testapp",
		Name: "0002_options",
		Operations: []m.Operation{
			&m.AlterModelOptions{Name: "model", Managed: m.Ptr(false)},
		},
	}
	out := renderMigration(t, &Writer{Migration: mig})
	if strings.Contains(out, `"time"`) {
		t.Errorf("the time package should not be imported:\n%s", out)
	}
	if !strings.Contains(out, "import (\n\tm \"github.com/ctolon/gormgate/migrations\"\n)\n") {
		t.Errorf("unexpected import block:\n%s", out)
	}
}

// django: tests/migrations/test_writer.py WriterTests.test_composite_pk_import
func TestWriter_CompositePKImport(t *testing.T) {
	// A migration that only uses the migrations API has exactly one import.
	mig := &m.Migration{
		App:  "testapp",
		Name: "0002_pk",
		Operations: []m.Operation{
			&m.AddField{ModelName: "foo", Name: "bar", Field: m.Field{Type: m.Int, PrimaryKey: true}},
		},
	}
	out := renderMigration(t, &Writer{Migration: mig})
	if !strings.Contains(out, "import (\n\tm \"github.com/ctolon/gormgate/migrations\"\n)\n") {
		t.Errorf("unexpected import block:\n%s", out)
	}
	if n := strings.Count(out, "import"); n != 1 {
		t.Errorf("expected exactly one import statement, got %d:\n%s", n, out)
	}
	if !strings.Contains(out, `m "github.com/ctolon/gormgate/migrations"`) {
		t.Errorf("migrations API not imported:\n%s", out)
	}
}

// django: tests/migrations/test_writer.py WriterTests.test_migration_file_header_comments
func TestWriter_MigrationFileHeaderComments(t *testing.T) {
	oldNow, oldVersion := Now, Version
	t.Cleanup(func() { Now, Version = oldNow, oldVersion })
	Now = func() time.Time { return time.Date(2015, 7, 31, 4, 40, 0, 0, time.UTC) }
	Version = "1.2.3"

	for _, includeHeader := range []bool{true, false} {
		t.Run(fmt.Sprintf("include_header=%v", includeHeader), func(t *testing.T) {
			mig := &m.Migration{App: "testapp", Name: "0001_initial"}
			out := renderMigration(t, &Writer{Migration: mig, IncludeHeader: includeHeader})
			const header = "// Generated by gormgate 1.2.3 on 2015-07-31 04:40\n\n"
			if got := strings.HasPrefix(out, header); got != includeHeader {
				t.Errorf("header present = %v, want %v:\n%s", got, includeHeader, out)
			}
			if !includeHeader {
				// The output starts with something that is not a comment,
				// indentation or a blank line.
				first := strings.SplitN(out, "\n", 2)[0]
				if first == "" || strings.HasPrefix(first, "//") || strings.HasPrefix(first, " ") || strings.HasPrefix(first, "\t") {
					t.Errorf("first line %q should not be a comment or blank", first)
				}
			}
		})
	}
}

// django: tests/migrations/test_writer.py WriterTests.test_run_before
func TestWriter_RunBefore(t *testing.T) {
	for _, tc := range []struct {
		runBefore []m.Key
		want      string
	}{
		{
			[]m.Key{{App: "foo", Name: "0001_bar"}},
			"\t\tRunBefore: []m.Key{\n\t\t\t{App: \"foo\", Name: \"0001_bar\"},\n\t\t},\n",
		},
		{
			[]m.Key{{App: "foo", Name: "0001_bar"}, {App: "foo", Name: "0002_baz"}},
			"\t\tRunBefore: []m.Key{\n\t\t\t{App: \"foo\", Name: \"0001_bar\"},\n\t\t\t{App: \"foo\", Name: \"0002_baz\"},\n\t\t},\n",
		},
	} {
		mig := &m.Migration{App: "testapp", Name: "0001_initial", RunBefore: tc.runBefore}
		out := renderMigration(t, &Writer{Migration: mig})
		if !strings.Contains(out, tc.want) {
			t.Errorf("run_before %v not rendered:\n%s", tc.runBefore, out)
		}
	}
}

// django: tests/migrations/test_writer.py WriterTests.test_atomic_is_false
func TestWriter_AtomicIsFalse(t *testing.T) {
	mig := &m.Migration{App: "testapp", Name: "0001_initial", Atomic: m.Ptr(false)}
	out := renderMigration(t, &Writer{Migration: mig})
	if !strings.Contains(out, "\t\tAtomic:       m.Ptr(false),\n") {
		t.Errorf("atomic = false not rendered:\n%s", out)
	}
	// atomic = True is the default and is not written out.
	mig.Atomic = m.Ptr(true)
	if out := renderMigration(t, &Writer{Migration: mig}); strings.Contains(out, "Atomic:") {
		t.Errorf("atomic = true should not be rendered:\n%s", out)
	}
}

// django: tests/migrations/test_writer.py WriterTests.test_default_attributes
func TestWriter_DefaultAttributes(t *testing.T) {
	mig := &m.Migration{App: "testapp", Name: "0001_initial"}
	out := renderMigration(t, &Writer{Migration: mig})
	for _, want := range []string{
		"\t\tDependencies: []m.Key{},\n",
		"\t\tOperations:   []m.Operation{},\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("%q missing from:\n%s", want, out)
		}
	}
	for _, unwanted := range []string{"Atomic", "Initial", "RunBefore", "Replaces"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("%q should not appear in:\n%s", unwanted, out)
		}
	}
	checkGolden(t, "default_attributes", out)
	goBuild(t, "0001_initial.go", out)
}

// django: tests/migrations/test_writer.py WriterTests.test_simple_migration
//
// The squashed-migration attributes (replaces/initial) and a RunGo operation
// referring to a function of this package, in one file.
func TestWriter_ReplacesAndRunGo(t *testing.T) {
	mig := &m.Migration{
		App:      "testapp",
		Name:     "0001_squashed_0002",
		Initial:  m.Ptr(true),
		Replaces: []m.Key{{App: "testapp", Name: "0001_initial"}, {App: "testapp", Name: "0002_second"}},
		Operations: []m.Operation{
			&m.RunSQL{SQL: m.Script("SELECT 1"), ReverseSQL: m.NoSQL},
			&m.RunGo{Code: packageFunc, ReverseCode: packageReverse, IsElidable: true},
		},
	}
	w := &Writer{Migration: mig}
	out := renderMigration(t, w)
	for _, want := range []string{
		"\t\tReplaces: []m.Key{\n\t\t\t{App: \"testapp\", Name: \"0001_initial\"},\n",
		"\t\tInitial:      m.Ptr(true),\n",
		// RunSQLNoop is the empty string: a reversible no-op.
		"ReverseSQL: m.Statements{},",
		"Code:        writer.packageFunc,",
		"ReverseCode: writer.packageReverse,",
		"IsElidable:  true,",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("%q missing from:\n%s", want, out)
		}
	}
	if w.NeedsManualPorting {
		t.Error("functions outside a migration file do not need manual porting")
	}
	checkGolden(t, "replaces_and_rungo", out)
}

// django: tests/migrations/test_squashmigrations.py
// SquashMigrationsTests.test_squashmigrations_manual_porting
//
// A RunGo function defined in another migration file of the same package
// disappears when the squashed migrations are deleted, so the writer flags the
// migration and lists the files to copy from.
func TestWriter_NeedsManualPorting(t *testing.T) {
	mig := &m.Migration{
		App:        "testapp",
		Name:       "0002_squashed",
		Operations: []m.Operation{&m.RunGo{Code: sampleForwards}},
	}
	w := &Writer{Migration: mig, PackagePath: writerPkgPath}
	out := renderMigration(t, w)
	if !w.NeedsManualPorting {
		t.Fatalf("NeedsManualPorting should be true:\n%s", out)
	}
	if !strings.Contains(out, "// Functions from the following migrations need manual copying.") {
		t.Errorf("no manual porting comment:\n%s", out)
	}
	if !strings.Contains(out, "// 0001_manualporting_test") {
		t.Errorf("the source migration file is not listed:\n%s", out)
	}
	// Inside its own package the function is referenced without an import.
	if !strings.Contains(out, "Code: sampleForwards,") {
		t.Errorf("function not referenced locally:\n%s", out)
	}

	// The same function, from the file the migration itself will be written
	// to, needs no porting.
	mig.Name = "0001_manualporting_test"
	w = &Writer{Migration: mig, PackagePath: writerPkgPath}
	if out := renderMigration(t, w); w.NeedsManualPorting {
		t.Errorf("a function of the migration's own file needs no porting:\n%s", out)
	}
}

// PackageName controls the package clause of the generated file.
//
// django: tests/migrations/test_writer.py WriterTests.test_migration_path
func TestWriter_PackageName(t *testing.T) {
	mig := &m.Migration{App: "testapp", Name: "0001_initial"}
	out := renderMigration(t, &Writer{Migration: mig})
	if !strings.HasPrefix(out, "package migrations\n") {
		t.Errorf("default package clause wrong:\n%s", out)
	}
	out = renderMigration(t, &Writer{Migration: mig, PackageName: "testappmigrations"})
	if !strings.HasPrefix(out, "package testappmigrations\n") {
		t.Errorf("package clause not taken from PackageName:\n%s", out)
	}
}

// ---------------------------------------------------------------------------
// Filename
// ---------------------------------------------------------------------------

// django: tests/migrations/test_writer.py WriterTests.test_migration_path
//
// Django derives the file name by appending ".py". Go's build system reads
// GOOS/GOARCH and "_test" suffixes out of file names, so gormgate has to
// escape migration names that end in one of them.
func TestWriter_Filename(t *testing.T) {
	for _, tc := range []struct{ name, want string }{
		{"0001_initial", "0001_initial.go"},
		{"0002_auto_20150101_1000", "0002_auto_20150101_1000.go"},
		{"0003_add_author", "0003_add_author.go"},
		{"0002_add_linux", "0002_add_linux_.go"},
		{"0003_foo_test", "0003_foo_test_.go"},
		{"0004_windows_amd64", "0004_windows_amd64_.go"},
		{"0005_js", "0005_js_.go"},
		{"0006_arm", "0006_arm_.go"},
		{"0007_darwin", "0007_darwin_.go"},
		{"0008_unix", "0008_unix.go"}, // "unix" is a build tag, not a file suffix
		{"0009_tested", "0009_tested.go"},
		{"0010_linux_kernel", "0010_linux_kernel.go"},
	} {
		if got := Filename(tc.name); got != tc.want {
			t.Errorf("Filename(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestWriter_FilenameIsCompiled checks the property the escaping exists for:
// whatever the migration is called, the go tool compiles the file it lands in,
// on every platform.
//
// django: tests/migrations/test_writer.py WriterTests.test_migration_path
func TestWriter_FilenameIsCompiled(t *testing.T) {
	names := []string{
		"0001_initial", "0002_add_linux", "0003_foo_test", "0004_windows_amd64",
		"0005_js", "0006_arm", "0007_darwin", "0008_unix", "0009_tested",
		"0010_linux_kernel", "0011_auto_20150101_1000", "0012_squashed_0013",
	}
	platforms := []struct{ goos, goarch string }{
		{"linux", "amd64"}, {"windows", "amd64"}, {"darwin", "arm64"},
		{"js", "wasm"}, {"plan9", "386"},
	}
	for _, name := range names {
		file := Filename(name)
		dir := t.TempDir()
		src := "package migrations\n\nvar _ = \"" + name + "\"\n"
		if err := os.WriteFile(filepath.Join(dir, file), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
		for _, p := range platforms {
			ctxt := build.Default
			ctxt.GOOS, ctxt.GOARCH = p.goos, p.goarch
			ctxt.CgoEnabled = false
			ok, err := ctxt.MatchFile(dir, file)
			if err != nil {
				t.Fatalf("MatchFile(%q): %v", file, err)
			}
			if !ok {
				t.Errorf("%s: %s is not built on %s/%s", name, file, p.goos, p.goarch)
			}
			// MatchFile also accepts _test.go files, which are compiled only
			// for tests; ImportDir tells the two apart.
			pkg, err := ctxt.ImportDir(dir, 0)
			if err != nil {
				t.Fatalf("ImportDir for %q on %s/%s: %v", file, p.goos, p.goarch, err)
			}
			if !contains(pkg.GoFiles, file) {
				t.Errorf("%s: %s is not in GoFiles on %s/%s (GoFiles=%v TestGoFiles=%v IgnoredGoFiles=%v)",
					name, file, p.goos, p.goarch, pkg.GoFiles, pkg.TestGoFiles, pkg.IgnoredGoFiles)
			}
		}
	}
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// TestWriter_MigrationNameFromFile checks that the loader reads back exactly
// the migration name the writer escaped.
func TestWriter_MigrationNameFromFile(t *testing.T) {
	for _, name := range []string{
		"0001_initial", "0002_add_linux", "0003_foo_test", "0004_windows_amd64",
		"0005_js", "0006_arm", "0008_unix", "0010_linux_kernel", "0011_trailing_",
	} {
		if got := MigrationNameFromFile(Filename(name)); got != name {
			t.Errorf("MigrationNameFromFile(Filename(%q)) = %q", name, got)
		}
		// The loader is also given full paths.
		if got := MigrationNameFromFile(filepath.Join("app", "migrations", Filename(name))); got != name {
			t.Errorf("MigrationNameFromFile(path of %q) = %q", name, got)
		}
	}
}

// ---------------------------------------------------------------------------
// The other generated files
// ---------------------------------------------------------------------------

// PackageFile and LinkFile have no Django counterpart (Python has no build
// step); they must still be gofmt-clean and parse.
func TestWriter_GeneratedPackageFiles(t *testing.T) {
	pkg := PackageFile("migrations", "testapp")
	if _, err := parser.ParseFile(token.NewFileSet(), "migrations.go", pkg, parser.AllErrors); err != nil {
		t.Fatalf("PackageFile does not parse: %v\n%s", err, pkg)
	}
	if !strings.Contains(string(pkg), `m.RegisterPackage("testapp")`) {
		t.Errorf("PackageFile does not register the app:\n%s", pkg)
	}

	link := LinkFile("main", []string{"example.com/p/b/migrations", "example.com/p/a/migrations"})
	if _, err := parser.ParseFile(token.NewFileSet(), "link.go", link, parser.AllErrors); err != nil {
		t.Fatalf("LinkFile does not parse: %v\n%s", err, link)
	}
	want := "import (\n\t_ \"example.com/p/a/migrations\"\n\t_ \"example.com/p/b/migrations\"\n)\n"
	if !strings.Contains(string(link), want) {
		t.Errorf("LinkFile imports are not sorted:\n%s", link)
	}
	if got := LinkFile("main", nil); !strings.Contains(string(got), "package main") || strings.Contains(string(got), "import") {
		t.Errorf("LinkFile with no apps:\n%s", got)
	}
}

func TestWriter_PackageNameOf(t *testing.T) {
	files := map[string][]byte{
		"0002_second.go": []byte("package testappmigrations\n"),
		"0001_first.go":  []byte("package testappmigrations\n"),
	}
	if got := PackageNameOf(files); got != "testappmigrations" {
		t.Errorf("PackageNameOf = %q", got)
	}
	if got := PackageNameOf(map[string][]byte{"broken.go": []byte("not go at all")}); got != "" {
		t.Errorf("PackageNameOf of an unparsable file = %q, want \"\"", got)
	}
	if got := PackageNameOf(nil); got != "" {
		t.Errorf("PackageNameOf(nil) = %q", got)
	}
}

// TestWriter_CompositeForeignKeyConstraint pins the generated spelling of a
// composite foreign key. Generated migration files are a committed, public
// format, so the field names of m.ForeignKeyConstraint and the way the
// writer lays them out are part of it; the golden file is the contract and
// goBuild proves the file still compiles against the migrations API.
func TestWriter_CompositeForeignKeyConstraint(t *testing.T) {
	c := &m.ForeignKeyConstraint{
		Name:     "fk_testapp_stall_barn",
		Fields:   []string{"barn_tenant_id", "barn_code"},
		To:       "testapp.Barn",
		ToFields: []string{"tenant_id", "code"},
		OnDelete: m.Cascade,
		OnUpdate: m.Restrict,
	}
	mig := &m.Migration{
		App:  "testapp",
		Name: "0001_composite_fk",
		Operations: []m.Operation{
			&m.AddConstraint{ModelName: "Stall", Constraint: c},
			&m.RemoveConstraint{ModelName: "Stall", Name: c.Name},
		},
	}
	out := renderMigration(t, &Writer{Migration: mig})
	for _, want := range []string{
		"&m.ForeignKeyConstraint{",
		"Name: \"fk_testapp_stall_barn\",",
		"To: \"testapp.Barn\",",
		"OnDelete: \"CASCADE\",",
		"OnUpdate: \"RESTRICT\",",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("generated migration is missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "m.ReferentialAction(") {
		t.Errorf("generated migration converts an enumeration:\n%s", out)
	}
	checkGolden(t, "composite_fk", out)
	// The generated file must not only compile: reading it back must give
	// the constraint that was written.
	if got := goRunRoundTrip(t, "0001_composite_fk.go", out); got != roundTripReport(mig) {
		t.Errorf("the migration read back from the generated file differs:\n got %s\nwant %s", got, roundTripReport(mig))
	}
}

// roundTripReport renders the part of a migration a round trip compares:
// every operation's description and the deconstruction of each constraint
// it carries.
func roundTripReport(mig *m.Migration) string {
	var b strings.Builder
	for _, op := range mig.Operations {
		fmt.Fprintf(&b, "%s\n", op.Describe())
		if add, ok := op.(*m.AddConstraint); ok {
			fmt.Fprintf(&b, "%+v\n", add.Constraint)
		}
	}
	return b.String()
}

// goRunRoundTrip writes src as a migration file of a throwaway module,
// builds a program that imports it and reports what m.Register received,
// and returns that report. It proves a generated migration is not only
// valid Go but declares the value it was written from.
func goRunRoundTrip(t *testing.T, fileName, src string) string {
	t.Helper()
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("go toolchain not available: %v", err)
	}
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	gomod := fmt.Sprintf("module roundtrip\n\ngo 1.26\n\nrequire github.com/ctolon/gormgate v0.0.0\n\nreplace github.com/ctolon/gormgate => %s\n", repo)
	write := func(name, content string) {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	sum, err := os.ReadFile(filepath.Join(repo, "go.sum"))
	if err != nil {
		t.Fatal(err)
	}
	write("go.mod", gomod)
	write("go.sum", string(sum))
	write("migrations/"+fileName, src)
	write("main.go", `package main

import (
	"fmt"

	m "github.com/ctolon/gormgate/migrations"
	_ "roundtrip/migrations"
)

func main() {
	for _, mig := range m.Registered("testapp") {
		for _, op := range mig.Operations {
			fmt.Printf("%s\n", op.Describe())
			if add, ok := op.(*m.AddConstraint); ok {
				fmt.Printf("%+v\n", add.Constraint)
			}
		}
	}
}
`)
	cmd := exec.Command(goBin, "run", ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod", "GOPROXY=off")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go run of the generated migration failed: %v\n%s\n--- source ---\n%s", err, out, src)
	}
	return string(out)
}

// TestWriter_EnumFieldsAreBareStringLiterals pins the generated spelling of
// the three closed enumerations of the migration API. IndexField.Sort,
// UniqueConstraint.Deferrable and ForeignKey.OnDelete/OnUpdate are named
// string types (m.SortOrder, m.Deferrable, m.ReferentialAction), but the
// declared type of the struct field already supplies the conversion, so the
// writer must keep emitting the plain quoted keyword. Generated migration
// files are a committed, public format: `Sort: "DESC"` may never become
// `Sort: m.SortOrder("DESC")`.
func TestWriter_EnumFieldsAreBareStringLiterals(t *testing.T) {
	mig := &m.Migration{
		App:  "testapp",
		Name: "0001_enums",
		Operations: []m.Operation{
			&m.CreateModel{Name: "Book", Table: "testapp_book", Fields: m.Fields{
				{Name: "author_id", Field: m.Field{Type: m.Int, Size: 64, ForeignKey: &m.ForeignKey{
					To: "testapp.Author", ToField: "id", OnDelete: m.Cascade, OnUpdate: m.Restrict,
					Name: "fk_testapp_book_author",
				}}},
			}},
			&m.AddIndex{ModelName: "Book", Index: m.Index{
				Name:   "idx_book_title",
				Fields: []m.IndexField{{Column: "title", Sort: m.SortDesc}, {Column: "isbn", Sort: m.SortAsc}},
			}},
			&m.AddConstraint{ModelName: "Book", Constraint: &m.UniqueConstraint{
				Name: "uniq_book_isbn", Fields: []string{"isbn"}, Deferrable: m.Deferred,
			}},
			&m.AddConstraint{ModelName: "Book", Constraint: &m.UniqueConstraint{
				Name: "uniq_book_title", Fields: []string{"title"}, Deferrable: m.Immediate,
			}},
		},
	}
	out := renderMigration(t, &Writer{Migration: mig})
	for _, want := range []string{
		"OnDelete: \"CASCADE\",",
		"OnUpdate: \"RESTRICT\",",
		"Sort:   \"DESC\",",
		"Sort:   \"ASC\",",
		"Deferrable: \"DEFERRED\",",
		"Deferrable: \"IMMEDIATE\",",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("generated migration is missing %q:\n%s", want, out)
		}
	}
	// No conversion of a named enumeration type may appear anywhere.
	for _, bad := range []string{"m.SortOrder(", "m.Deferrable(", "m.ReferentialAction("} {
		if strings.Contains(out, bad) {
			t.Errorf("generated migration contains %s:\n%s", bad, out)
		}
	}
	checkGolden(t, "enum_fields", out)
	goBuild(t, "0001_enums.go", out)
}
