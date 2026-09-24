// Port of Django's tests/migrations/test_optimizer.py (OptimizerTests).
//
// Each Go test keeps the name of the Django test it mirrors and cites it in a
// `// django:` comment. Django compares optimizer output by serializing every
// operation (OptimizerTestBase.assertOptimizesTo); this port compares a
// deterministic rendering of the operation structs, which omits zero/empty
// attributes the same way Django's deconstruct() omits defaults.
package optimizer

import (
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	m "github.com/ctolon/gormgate/migrations"
)

// defaultApp is the app label Django's OptimizerTestBase passes when a test
// doesn't specify one.
const defaultApp = "migrations"

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// optimizeCounting mirrors Optimize but also returns the number of passes.
// Django's MigrationOptimizer records them in _iterations (used by
// assertOptimizesTo's `exact`/`less_than`); gormgate's Optimize doesn't expose
// a counter, so the test drives the same fixed-point loop directly.
func optimizeCounting(ops []m.Operation, app string) ([]m.Operation, int) {
	iterations := 0
	for {
		result, reduced := optimizeInner(ops, app)
		iterations++
		if !reduced {
			return result, iterations
		}
		ops = result
	}
}

// assertOptimizesTo is Django's OptimizerTestBase.assertOptimizesTo. An empty
// app uses Django's default label "migrations".
func assertOptimizesTo(t *testing.T, app string, in, want []m.Operation) {
	t.Helper()
	assertOptimizesToExact(t, app, in, want, 0)
}

// assertOptimizesToExact additionally checks the pass count (Django's
// `exact=` keyword); exact <= 0 skips that check.
func assertOptimizesToExact(t *testing.T, app string, in, want []m.Operation, exact int) {
	t.Helper()
	if app == "" {
		app = defaultApp
	}
	got, iterations := optimizeCounting(in, app)
	gotStr, wantStr := renderOps(got), renderOps(want)
	if !reflect.DeepEqual(gotStr, wantStr) {
		t.Errorf("optimize(app=%q) mismatch\n--- got (%d) ---\n%s\n--- want (%d) ---\n%s",
			app, len(gotStr), strings.Join(gotStr, "\n"), len(wantStr), strings.Join(wantStr, "\n"))
	}
	if exact > 0 && iterations != exact {
		t.Errorf("optimization did not take exactly %d iterations (it took %d)", exact, iterations)
	}
}

// assertDoesNotOptimize is Django's OptimizerTestBase.assertDoesNotOptimize.
func assertDoesNotOptimize(t *testing.T, app string, in []m.Operation) {
	t.Helper()
	assertOptimizesTo(t, app, in, in)
}

func ops(list ...m.Operation) []m.Operation { return list }

// model is a compact CreateModel builder. gormgate always carries the table
// name explicitly (documented deviation: there is no separate db_table
// option), so every model gets one; it defaults to "migrations_<name>".
type model struct {
	name    string
	table   string
	fields  m.Fields
	options m.Options
}

func (x model) op() *m.CreateModel {
	table := x.table
	if table == "" {
		table = "migrations_" + strings.ToLower(x.name)
	}
	return &m.CreateModel{Name: x.name, Table: table, Fields: x.fields, Options: x.options}
}

func f(name string, field m.Field) m.NamedField { return m.NamedField{Name: name, Field: field} }

// Field analogues of the Django field classes used by test_optimizer.py.
func charField(maxLength int) m.Field { return m.Field{Type: m.String, Size: maxLength} }
func intField() m.Field               { return m.Field{Type: m.Int, Size: 32} }
func textField() m.Field              { return m.Field{Type: m.String} }

// foreignKey is models.ForeignKey(to, models.CASCADE).
func foreignKey(to string) m.Field {
	return m.Field{Type: m.Uint, Size: 64, ForeignKey: &m.ForeignKey{To: to, OnDelete: m.Cascade}}
}

// elidable is an operation with elidable=True. Django's test uses a bare
// operations.base.Operation(); gormgate has no instantiable base operation, so
// the port uses the elidable form of RunGo (Django's RunPython).
func elidable() m.Operation {
	return &m.RunGo{Code: m.RunGoNoop, ReverseCode: m.RunGoNoop, IsElidable: true}
}

// ---------------------------------------------------------------------------
// Rendering (the analogue of Django's serializer-based comparison)
// ---------------------------------------------------------------------------

var (
	typeOfTypeRef = reflect.TypeOf(&m.TypeRef{})
	typeOfTime    = reflect.TypeOf(time.Time{})
)

func renderOps(list []m.Operation) []string {
	out := make([]string, len(list))
	for i, op := range list {
		out[i] = renderValue(reflect.ValueOf(op))
	}
	return out
}

// isEmptyValue reports whether v carries no information; such attributes are
// left out of the rendering, mirroring the way Django's deconstruct() omits
// attributes that still hold their default.
func isEmptyValue(v reflect.Value) bool {
	if !v.IsValid() {
		return true
	}
	switch v.Kind() {
	case reflect.Slice, reflect.Map:
		return v.Len() == 0
	case reflect.Struct:
		if v.Type() == typeOfTime {
			return v.IsZero()
		}
		t := v.Type()
		for i := 0; i < t.NumField(); i++ {
			sf := t.Field(i)
			if !sf.IsExported() || sf.Anonymous {
				continue
			}
			if !isEmptyValue(v.Field(i)) {
				return false
			}
		}
		return true
	}
	return v.IsZero()
}

func renderValue(v reflect.Value) string {
	if !v.IsValid() {
		return "nil"
	}
	switch v.Type() {
	case typeOfTypeRef:
		if v.IsNil() {
			return "nil"
		}
		return "TypeRef(" + v.Interface().(*m.TypeRef).String() + ")"
	case typeOfTime:
		return v.Interface().(time.Time).UTC().Format(time.RFC3339Nano)
	}
	switch v.Kind() {
	case reflect.Interface:
		if v.IsNil() {
			return "nil"
		}
		return renderValue(v.Elem())
	case reflect.Pointer:
		if v.IsNil() {
			return "nil"
		}
		return "&" + renderValue(v.Elem())
	case reflect.Func:
		if v.IsNil() {
			return "nil"
		}
		return "func:" + m.FuncName(v.Interface())
	case reflect.Struct:
		t := v.Type()
		var parts []string
		for i := 0; i < t.NumField(); i++ {
			sf := t.Field(i)
			if !sf.IsExported() || sf.Anonymous {
				continue
			}
			fv := v.Field(i)
			if isEmptyValue(fv) {
				continue
			}
			parts = append(parts, sf.Name+": "+renderValue(fv))
		}
		return t.Name() + "{" + strings.Join(parts, ", ") + "}"
	case reflect.Slice, reflect.Array:
		var parts []string
		for i := 0; i < v.Len(); i++ {
			parts = append(parts, renderValue(v.Index(i)))
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case reflect.Map:
		keys := v.MapKeys()
		sort.Slice(keys, func(i, j int) bool {
			return fmt.Sprint(keys[i].Interface()) < fmt.Sprint(keys[j].Interface())
		})
		var parts []string
		for _, k := range keys {
			parts = append(parts, renderValue(k)+": "+renderValue(v.MapIndex(k)))
		}
		return "{" + strings.Join(parts, ", ") + "}"
	case reflect.String:
		return strconv.Quote(v.String())
	}
	return fmt.Sprintf("%v", v.Interface())
}

// ---------------------------------------------------------------------------
// Model operations
// ---------------------------------------------------------------------------

// TestOptimizer_Single checks that the optimizer does nothing on a single
// operation, and that it does it in just one pass.
//
// django: tests/migrations/test_optimizer.py OptimizerTests.test_single
func TestOptimizer_Single(t *testing.T) {
	assertOptimizesToExact(t, "",
		ops(&m.DeleteModel{Name: "Foo"}),
		ops(&m.DeleteModel{Name: "Foo"}),
		1,
	)
}

// TestOptimizer_CreateDeleteModel checks that CreateModel and DeleteModel
// collapse into nothing.
//
// django: tests/migrations/test_optimizer.py OptimizerTests.test_create_delete_model
func TestOptimizer_CreateDeleteModel(t *testing.T) {
	assertOptimizesTo(t, "",
		ops(
			model{name: "Foo", fields: m.Fields{f("name", charField(255))}}.op(),
			&m.DeleteModel{Name: "Foo"},
		),
		nil,
	)
}

// TestOptimizer_CreateRenameModel checks that CreateModel absorbs RenameModel.
//
// Django's model carries options/bases/managers that gormgate has no analogue
// for; the port keeps the table name instead, which also pins the documented
// gormgate deviation: RenameModel does NOT rename the table, so the absorbed
// CreateModel keeps the table it was created with.
//
// django: tests/migrations/test_optimizer.py OptimizerTests.test_create_rename_model
func TestOptimizer_CreateRenameModel(t *testing.T) {
	assertOptimizesTo(t, "",
		ops(
			model{
				name:    "Foo",
				table:   "migrations_foo",
				fields:  m.Fields{f("name", charField(255))},
				options: m.Options{DBTableComment: "Foo"},
			}.op(),
			&m.RenameModel{OldName: "Foo", NewName: "Bar"},
		),
		ops(
			model{
				name:    "Bar",
				table:   "migrations_foo",
				fields:  m.Fields{f("name", charField(255))},
				options: m.Options{DBTableComment: "Foo"},
			}.op(),
		),
	)
}

// TestOptimizer_RenameModelSelf checks that RenameModels absorb themselves.
//
// django: tests/migrations/test_optimizer.py OptimizerTests.test_rename_model_self
func TestOptimizer_RenameModelSelf(t *testing.T) {
	assertOptimizesTo(t, "",
		ops(
			&m.RenameModel{OldName: "Foo", NewName: "Baa"},
			&m.RenameModel{OldName: "Baa", NewName: "Bar"},
		),
		ops(&m.RenameModel{OldName: "Foo", NewName: "Bar"}),
	)
}

// TestOptimizer_CreateAlterModelOptions checks that CreateModel absorbs
// AlterModelOptions.
//
// Django alters verbose_name_plural; gormgate's AlterModelOptions carries only
// `managed`, which is the single Meta option gorm models have.
//
// django: tests/migrations/test_optimizer.py OptimizerTests.test_create_alter_model_options
func TestOptimizer_CreateAlterModelOptions(t *testing.T) {
	assertOptimizesTo(t, "",
		ops(
			model{name: "Foo"}.op(),
			&m.AlterModelOptions{Name: "Foo", Managed: m.Ptr(false)},
		),
		ops(model{name: "Foo", options: m.Options{Managed: m.Ptr(false)}}.op()),
	)
}

// TestOptimizer_CreateAlterModelTable checks that CreateModel absorbs
// AlterModelTable.
//
// django: tests/migrations/test_optimizer.py OptimizerTests.test_create_alter_model_table
func TestOptimizer_CreateAlterModelTable(t *testing.T) {
	assertOptimizesTo(t, "",
		ops(
			model{name: "Foo", table: "migrations_foo"}.op(),
			&m.AlterModelTable{Name: "foo", Table: "foo"},
		),
		ops(model{name: "Foo", table: "foo"}.op()),
	)
}

// TestOptimizer_CreateAlterModelTableComment checks that CreateModel absorbs
// AlterModelTableComment.
//
// django: tests/migrations/test_optimizer.py OptimizerTests.test_create_alter_model_table_comment
func TestOptimizer_CreateAlterModelTableComment(t *testing.T) {
	assertOptimizesTo(t, "",
		ops(
			model{name: "Foo"}.op(),
			&m.AlterModelTableComment{Name: "foo", TableComment: "A lovely table."},
		),
		ops(model{name: "Foo", options: m.Options{DBTableComment: "A lovely table."}}.op()),
	)
}

// TestOptimizer_CreateModelAndRemoveModelOptions checks that an
// AlterModelOptions that resets an option removes it from the absorbing
// CreateModel.
//
// Django's second assertion narrows a two-option dict down to one option;
// gormgate has a single alterable option (`managed`), so only the "reset it
// completely" half of the Django test has an analogue.
//
// django: tests/migrations/test_optimizer.py OptimizerTests.test_create_model_and_remove_model_options
func TestOptimizer_CreateModelAndRemoveModelOptions(t *testing.T) {
	assertOptimizesTo(t, "",
		ops(
			model{name: "MyModel", options: m.Options{Managed: m.Ptr(false)}}.op(),
			&m.AlterModelOptions{Name: "MyModel"},
		),
		ops(model{name: "MyModel"}.op()),
	)
}

// testCreateAlterFooDeleteModel checks that CreateModel, AlterModelTable,
// AlterUniqueTogether and DeleteModel collapse into nothing.
//
// django: tests/migrations/test_optimizer.py OptimizerTests._test_create_alter_foo_delete_model
func testCreateAlterFooDeleteModel(t *testing.T, alterFoo m.Operation) {
	t.Helper()
	assertOptimizesTo(t, "",
		ops(
			model{name: "Foo", fields: m.Fields{f("name", charField(255))}}.op(),
			&m.AlterModelTable{Name: "Foo", Table: "woohoo"},
			alterFoo,
			&m.DeleteModel{Name: "Foo"},
		),
		nil,
	)
}

// django: tests/migrations/test_optimizer.py OptimizerTests.test_create_alter_unique_delete_model
func TestOptimizer_CreateAlterUniqueDeleteModel(t *testing.T) {
	testCreateAlterFooDeleteModel(t, &m.AlterUniqueTogether{
		Name: "Foo", UniqueTogether: [][]string{{"a", "b"}},
	})
}

// testAlterAlter checks that two AlterUniqueTogether/AlterModelTable/
// AlterField operations collapse into the second.
//
// django: tests/migrations/test_optimizer.py OptimizerTests._test_alter_alter
func testAlterAlter(t *testing.T, alterFoo, alterBar m.Operation) {
	t.Helper()
	assertOptimizesTo(t, "", ops(alterFoo, alterBar), ops(alterBar))
}

// django: tests/migrations/test_optimizer.py OptimizerTests.test_alter_alter_table_model
func TestOptimizer_AlterAlterTableModel(t *testing.T) {
	testAlterAlter(t,
		&m.AlterModelTable{Name: "Foo", Table: "a"},
		&m.AlterModelTable{Name: "Foo", Table: "b"},
	)
}

// django: tests/migrations/test_optimizer.py OptimizerTests.test_alter_alter_unique_model
func TestOptimizer_AlterAlterUniqueModel(t *testing.T) {
	testAlterAlter(t,
		&m.AlterUniqueTogether{Name: "Foo", UniqueTogether: [][]string{{"a", "b"}}},
		&m.AlterUniqueTogether{Name: "Foo", UniqueTogether: [][]string{{"a", "c"}}},
	)
}

// TestOptimizer_AlterAlterField mirrors Django's IntegerField() vs
// IntegerField(help_text="help") pair with two fields that differ in a
// non-schema attribute gormgate does have (the column comment).
//
// django: tests/migrations/test_optimizer.py OptimizerTests.test_alter_alter_field
func TestOptimizer_AlterAlterField(t *testing.T) {
	helped := intField()
	helped.Comment = "help"
	testAlterAlter(t,
		&m.AlterField{ModelName: "Foo", Name: "name", Field: intField()},
		&m.AlterField{ModelName: "Foo", Name: "name", Field: helped},
	)
}

// TestOptimizer_OptimizeThroughCreate checks that create/delete can be
// optimized away through a create or delete of a different model, but only if
// the create operation does not mention the model at all.
//
// The `bases=` halves of the Django test are skipped: gormgate has no model
// inheritance.
//
// django: tests/migrations/test_optimizer.py OptimizerTests.test_optimize_through_create
func TestOptimizer_OptimizeThroughCreate(t *testing.T) {
	foo := func() *m.CreateModel {
		return model{name: "Foo", fields: m.Fields{f("name", charField(255))}}.op()
	}
	bar := func() *m.CreateModel {
		return model{name: "Bar", fields: m.Fields{f("size", intField())}}.op()
	}

	// These should work.
	assertOptimizesTo(t, "",
		ops(foo(), bar(), &m.DeleteModel{Name: "Foo"}),
		ops(bar()),
	)
	assertOptimizesTo(t, "",
		ops(foo(), bar(), &m.DeleteModel{Name: "Bar"}, &m.DeleteModel{Name: "Foo"}),
		nil,
	)
	assertOptimizesTo(t, "",
		ops(foo(), bar(), &m.DeleteModel{Name: "Foo"}, &m.DeleteModel{Name: "Bar"}),
		nil,
	)

	// Operations should be optimized if the FK references a model from the
	// other app.
	barFK := func(to string) *m.CreateModel {
		return model{name: "Bar", fields: m.Fields{f("other", foreignKey(to))}}.op()
	}
	assertOptimizesTo(t, "otherapp",
		ops(foo(), barFK("testapp.Foo"), &m.DeleteModel{Name: "Foo"}),
		ops(barFK("testapp.Foo")),
	)

	// But it shouldn't work if a FK references a model with the same
	// app_label.
	assertDoesNotOptimize(t, "",
		ops(foo(), barFK("Foo"), &m.DeleteModel{Name: "Foo"}),
	)
	assertDoesNotOptimize(t, "testapp",
		ops(foo(), barFK("testapp.Foo"), &m.DeleteModel{Name: "Foo"}),
	)

	// A longer chain: Book/Person/Review/Reviewer.
	assertOptimizesTo(t, "test_app",
		ops(
			model{name: "Book", fields: m.Fields{f("name", charField(255))}}.op(),
			model{name: "Person", fields: m.Fields{f("name", charField(255))}}.op(),
			&m.AddField{ModelName: "book", Name: "author", Field: foreignKey("test_app.Person")},
			model{name: "Review", fields: m.Fields{f("book", foreignKey("test_app.Book"))}}.op(),
			model{name: "Reviewer", fields: m.Fields{f("name", charField(255))}}.op(),
			&m.AddField{ModelName: "review", Name: "reviewer", Field: foreignKey("test_app.Reviewer")},
			&m.RemoveField{ModelName: "book", Name: "author"},
			&m.DeleteModel{Name: "Person"},
		),
		ops(
			model{name: "Book", fields: m.Fields{f("name", charField(255))}}.op(),
			model{name: "Reviewer", fields: m.Fields{f("name", charField(255))}}.op(),
			model{name: "Review", fields: m.Fields{
				f("book", foreignKey("test_app.Book")),
				f("reviewer", foreignKey("test_app.Reviewer")),
			}}.op(),
		),
	)
}

// ---------------------------------------------------------------------------
// CreateModel absorbing field operations
// ---------------------------------------------------------------------------

// TestOptimizer_CreateModelAddField checks that AddField optimizes into
// CreateModel.
//
// django: tests/migrations/test_optimizer.py OptimizerTests.test_create_model_add_field
func TestOptimizer_CreateModelAddField(t *testing.T) {
	assertOptimizesTo(t, "",
		ops(
			model{
				name:    "Foo",
				fields:  m.Fields{f("name", charField(255))},
				options: m.Options{DBTableComment: "Foo"},
			}.op(),
			&m.AddField{ModelName: "Foo", Name: "age", Field: intField()},
		),
		ops(model{
			name:    "Foo",
			fields:  m.Fields{f("name", charField(255)), f("age", intField())},
			options: m.Options{DBTableComment: "Foo"},
		}.op()),
	)
}

// TestOptimizer_CreateModelReordering checks that AddField optimizes into
// CreateModel if it's a FK to a model that's between them (and there's no FK
// in the other direction), by changing the order of the CreateModel
// operations.
//
// django: tests/migrations/test_optimizer.py OptimizerTests.test_create_model_reordering
func TestOptimizer_CreateModelReordering(t *testing.T) {
	assertOptimizesTo(t, "",
		ops(
			model{name: "Foo", fields: m.Fields{f("name", charField(255))}}.op(),
			model{name: "Link", fields: m.Fields{f("url", textField())}}.op(),
			&m.AddField{ModelName: "Foo", Name: "link", Field: foreignKey("migrations.Link")},
		),
		ops(
			model{name: "Link", fields: m.Fields{f("url", textField())}}.op(),
			model{name: "Foo", fields: m.Fields{
				f("name", charField(255)),
				f("link", foreignKey("migrations.Link")),
			}}.op(),
		),
	)
}

// TestOptimizer_CreateModelReorderingCircularFK checks that the CreateModel
// reordering behaviour doesn't result in an infinite loop if there are FKs in
// both directions.
//
// django: tests/migrations/test_optimizer.py OptimizerTests.test_create_model_reordering_circular_fk
func TestOptimizer_CreateModelReorderingCircularFK(t *testing.T) {
	assertOptimizesTo(t, "",
		ops(
			model{name: "Bar", fields: m.Fields{f("url", textField())}}.op(),
			model{name: "Foo", fields: m.Fields{f("name", charField(255))}}.op(),
			&m.AddField{ModelName: "Bar", Name: "foo_fk", Field: foreignKey("migrations.Foo")},
			&m.AddField{ModelName: "Foo", Name: "bar_fk", Field: foreignKey("migrations.Bar")},
		),
		ops(
			model{name: "Foo", fields: m.Fields{f("name", charField(255))}}.op(),
			model{name: "Bar", fields: m.Fields{
				f("url", textField()),
				f("foo_fk", foreignKey("migrations.Foo")),
			}}.op(),
			&m.AddField{ModelName: "Foo", Name: "bar_fk", Field: foreignKey("migrations.Bar")},
		),
	)
}

// TestOptimizer_CreateModelNoReorderingForUnrelatedFK checks that the
// CreateModel order remains unchanged if the later AddField operation isn't a
// FK between them.
//
// django: tests/migrations/test_optimizer.py OptimizerTests.test_create_model_no_reordering_for_unrelated_fk
func TestOptimizer_CreateModelNoReorderingForUnrelatedFK(t *testing.T) {
	assertDoesNotOptimize(t, "",
		ops(
			model{name: "Foo", fields: m.Fields{f("name", charField(255))}}.op(),
			model{name: "Link", fields: m.Fields{f("url", textField())}}.op(),
			&m.AddField{ModelName: "Other", Name: "link", Field: foreignKey("migrations.Link")},
		),
	)
}

// TestOptimizer_CreateModelAlterField checks that AlterField optimizes into
// CreateModel.
//
// django: tests/migrations/test_optimizer.py OptimizerTests.test_create_model_alter_field
func TestOptimizer_CreateModelAlterField(t *testing.T) {
	assertOptimizesTo(t, "",
		ops(
			model{
				name:    "Foo",
				fields:  m.Fields{f("name", charField(255))},
				options: m.Options{DBTableComment: "Foo"},
			}.op(),
			&m.AlterField{ModelName: "Foo", Name: "name", Field: intField()},
		),
		ops(model{
			name:    "Foo",
			fields:  m.Fields{f("name", intField())},
			options: m.Options{DBTableComment: "Foo"},
		}.op()),
	)
}

// TestOptimizer_CreateModelRenameField checks that RenameField optimizes into
// CreateModel.
//
// django: tests/migrations/test_optimizer.py OptimizerTests.test_create_model_rename_field
func TestOptimizer_CreateModelRenameField(t *testing.T) {
	assertOptimizesTo(t, "",
		ops(
			model{
				name:    "Foo",
				fields:  m.Fields{f("name", charField(255))},
				options: m.Options{DBTableComment: "Foo"},
			}.op(),
			&m.RenameField{ModelName: "Foo", OldName: "name", NewName: "title"},
		),
		ops(model{
			name:    "Foo",
			fields:  m.Fields{f("title", charField(255))},
			options: m.Options{DBTableComment: "Foo"},
		}.op()),
	)
}

// TestOptimizer_AddFieldRenameField checks that RenameField optimizes into
// AddField.
//
// django: tests/migrations/test_optimizer.py OptimizerTests.test_add_field_rename_field
func TestOptimizer_AddFieldRenameField(t *testing.T) {
	assertOptimizesTo(t, "",
		ops(
			&m.AddField{ModelName: "Foo", Name: "name", Field: charField(255)},
			&m.RenameField{ModelName: "Foo", OldName: "name", NewName: "title"},
		),
		ops(&m.AddField{ModelName: "Foo", Name: "title", Field: charField(255)}),
	)
}

// TestOptimizer_AlterFieldRenameField checks that RenameField optimizes to the
// other side of AlterField, and into itself.
//
// Django guards this reduction with `self.field.db_column is None`; gormgate
// has no db_column (a field's name IS the column), so the guard is always
// satisfied.
//
// django: tests/migrations/test_optimizer.py OptimizerTests.test_alter_field_rename_field
func TestOptimizer_AlterFieldRenameField(t *testing.T) {
	assertOptimizesTo(t, "",
		ops(
			&m.AlterField{ModelName: "Foo", Name: "name", Field: charField(255)},
			&m.RenameField{ModelName: "Foo", OldName: "name", NewName: "title"},
			&m.RenameField{ModelName: "Foo", OldName: "title", NewName: "nom"},
		),
		ops(
			&m.RenameField{ModelName: "Foo", OldName: "name", NewName: "nom"},
			&m.AlterField{ModelName: "Foo", Name: "nom", Field: charField(255)},
		),
	)
}

// TestOptimizer_SwappingFieldsNames checks that a cycle of RenameField
// operations around a non-reducible operation is left alone.
//
// django: tests/migrations/test_optimizer.py OptimizerTests.test_swapping_fields_names
func TestOptimizer_SwappingFieldsNames(t *testing.T) {
	assertDoesNotOptimize(t, "",
		ops(
			model{name: "MyModel", fields: m.Fields{
				f("field_a", intField()),
				f("field_b", intField()),
			}}.op(),
			&m.RunGo{Code: m.RunGoNoop},
			&m.RenameField{ModelName: "MyModel", OldName: "field_a", NewName: "field_c"},
			&m.RenameField{ModelName: "MyModel", OldName: "field_b", NewName: "field_a"},
			&m.RenameField{ModelName: "MyModel", OldName: "field_c", NewName: "field_b"},
		),
	)
}

// TestOptimizer_CreateModelRemoveField checks that RemoveField optimizes into
// CreateModel.
//
// django: tests/migrations/test_optimizer.py OptimizerTests.test_create_model_remove_field
func TestOptimizer_CreateModelRemoveField(t *testing.T) {
	assertOptimizesTo(t, "",
		ops(
			model{
				name: "Foo",
				fields: m.Fields{
					f("name", charField(255)),
					f("age", intField()),
				},
				options: m.Options{DBTableComment: "Foo"},
			}.op(),
			&m.RemoveField{ModelName: "Foo", Name: "age"},
		),
		ops(model{
			name:    "Foo",
			fields:  m.Fields{f("name", charField(255))},
			options: m.Options{DBTableComment: "Foo"},
		}.op()),
	)
}

// TestOptimizer_AddFieldAlterField checks that AlterField optimizes into
// AddField.
//
// django: tests/migrations/test_optimizer.py OptimizerTests.test_add_field_alter_field
func TestOptimizer_AddFieldAlterField(t *testing.T) {
	floatWithDefault := m.Field{Type: m.Float, Default: 2.4}
	assertOptimizesTo(t, "",
		ops(
			&m.AddField{ModelName: "Foo", Name: "age", Field: intField()},
			&m.AlterField{ModelName: "Foo", Name: "age", Field: floatWithDefault},
		),
		ops(&m.AddField{ModelName: "Foo", Name: "age", Field: floatWithDefault}),
	)
}

// TestOptimizer_AddFieldDeleteField checks that RemoveField cancels AddField.
//
// django: tests/migrations/test_optimizer.py OptimizerTests.test_add_field_delete_field
func TestOptimizer_AddFieldDeleteField(t *testing.T) {
	assertOptimizesTo(t, "",
		ops(
			&m.AddField{ModelName: "Foo", Name: "age", Field: intField()},
			&m.RemoveField{ModelName: "Foo", Name: "age"},
		),
		nil,
	)
}

// TestOptimizer_AlterFieldDeleteField checks that RemoveField absorbs
// AlterField.
//
// django: tests/migrations/test_optimizer.py OptimizerTests.test_alter_field_delete_field
func TestOptimizer_AlterFieldDeleteField(t *testing.T) {
	assertOptimizesTo(t, "",
		ops(
			&m.AlterField{ModelName: "Foo", Name: "age", Field: intField()},
			&m.RemoveField{ModelName: "Foo", Name: "age"},
		),
		ops(&m.RemoveField{ModelName: "Foo", Name: "age"}),
	)
}

// testCreateAlterFooField checks that CreateModel followed by
// AlterUniqueTogether and an add/alter/rename/remove field optimizes to a
// CreateModel carrying the option.
//
// django: tests/migrations/test_optimizer.py OptimizerTests._test_create_alter_foo_field
func testCreateAlterFooField(t *testing.T, optionValue [][]string) {
	t.Helper()
	alter := func() m.Operation {
		return &m.AlterUniqueTogether{Name: "Foo", UniqueTogether: optionValue}
	}
	options := func(value [][]string) m.Options { return m.Options{UniqueTogether: value} }

	ab := m.Fields{f("a", intField()), f("b", intField())}
	abc := m.Fields{f("a", intField()), f("b", intField()), f("c", intField())}

	// AddField
	assertOptimizesTo(t, "",
		ops(
			model{name: "Foo", fields: ab}.op(),
			alter(),
			&m.AddField{ModelName: "Foo", Name: "c", Field: intField()},
		),
		ops(model{name: "Foo", fields: abc, options: options(optionValue)}.op()),
	)

	// AlterField
	assertOptimizesTo(t, "",
		ops(
			model{name: "Foo", fields: ab}.op(),
			alter(),
			&m.AlterField{ModelName: "Foo", Name: "b", Field: charField(255)},
		),
		ops(model{name: "Foo", fields: m.Fields{
			f("a", intField()), f("b", charField(255)),
		}, options: options(optionValue)}.op()),
	)
	assertOptimizesTo(t, "",
		ops(
			model{name: "Foo", fields: abc}.op(),
			alter(),
			&m.AlterField{ModelName: "Foo", Name: "c", Field: charField(255)},
		),
		ops(model{name: "Foo", fields: m.Fields{
			f("a", intField()), f("b", intField()), f("c", charField(255)),
		}, options: options(optionValue)}.op()),
	)

	// RenameField: "b" becomes "c" in the option value too.
	renamed := make([][]string, len(optionValue))
	for i, item := range optionValue {
		row := make([]string, len(item))
		for j, v := range item {
			if v == "b" {
				v = "c"
			}
			row[j] = v
		}
		renamed[i] = row
	}
	assertOptimizesTo(t, "",
		ops(
			model{name: "Foo", fields: ab}.op(),
			alter(),
			&m.RenameField{ModelName: "Foo", OldName: "b", NewName: "c"},
		),
		ops(model{name: "Foo", fields: m.Fields{
			f("a", intField()), f("c", intField()),
		}, options: options(renamed)}.op()),
	)
	assertOptimizesTo(t, "",
		ops(
			model{name: "Foo", fields: ab}.op(),
			alter(),
			&m.RenameField{ModelName: "Foo", OldName: "b", NewName: "x"},
			&m.RenameField{ModelName: "Foo", OldName: "x", NewName: "c"},
		),
		ops(model{name: "Foo", fields: m.Fields{
			f("a", intField()), f("c", intField()),
		}, options: options(renamed)}.op()),
	)
	assertOptimizesTo(t, "",
		ops(
			model{name: "Foo", fields: abc}.op(),
			alter(),
			&m.RenameField{ModelName: "Foo", OldName: "c", NewName: "d"},
		),
		ops(model{name: "Foo", fields: m.Fields{
			f("a", intField()), f("b", intField()), f("d", intField()),
		}, options: options(optionValue)}.op()),
	)

	// RemoveField: "b" drops out of the option value; a tuple that becomes
	// empty drops the option entirely.
	var removed [][]string
	for _, item := range optionValue {
		var row []string
		for _, v := range item {
			if v != "b" {
				row = append(row, v)
			}
		}
		if len(row) > 0 {
			removed = append(removed, row)
		}
	}
	assertOptimizesTo(t, "",
		ops(
			model{name: "Foo", fields: ab}.op(),
			alter(),
			&m.RemoveField{ModelName: "Foo", Name: "b"},
		),
		ops(model{name: "Foo", fields: m.Fields{f("a", intField())}, options: options(removed)}.op()),
	)
	assertOptimizesTo(t, "",
		ops(
			model{name: "Foo", fields: abc}.op(),
			alter(),
			&m.RemoveField{ModelName: "Foo", Name: "c"},
		),
		ops(model{name: "Foo", fields: ab, options: options(optionValue)}.op()),
	)
}

// django: tests/migrations/test_optimizer.py OptimizerTests.test_create_alter_unique_field
func TestOptimizer_CreateAlterUniqueField(t *testing.T) {
	testCreateAlterFooField(t, [][]string{{"a", "b"}})
}

// TestOptimizer_OptimizeThroughFields checks that field-level through checking
// works: model Foo collapses to nonexistence and model Bar to a single
// IntegerField called "width".
//
// django: tests/migrations/test_optimizer.py OptimizerTests.test_optimize_through_fields
func TestOptimizer_OptimizeThroughFields(t *testing.T) {
	assertOptimizesTo(t, "",
		ops(
			model{name: "Foo", fields: m.Fields{f("name", charField(255))}}.op(),
			model{name: "Bar", fields: m.Fields{f("size", intField())}}.op(),
			&m.AddField{ModelName: "Foo", Name: "age", Field: intField()},
			&m.AddField{ModelName: "Bar", Name: "width", Field: intField()},
			&m.AlterField{ModelName: "Foo", Name: "age", Field: intField()},
			&m.RenameField{ModelName: "Bar", OldName: "size", NewName: "dimensions"},
			&m.RemoveField{ModelName: "Foo", Name: "age"},
			&m.RenameModel{OldName: "Foo", NewName: "Phou"},
			&m.RemoveField{ModelName: "Bar", Name: "dimensions"},
			&m.RenameModel{OldName: "Phou", NewName: "Fou"},
			&m.DeleteModel{Name: "Fou"},
		),
		ops(model{name: "Bar", fields: m.Fields{f("width", intField())}}.op()),
	)
}

// TestOptimizer_OptimizeElidableOperation checks that elidable operations are
// dropped and don't block reductions across them.
//
// django: tests/migrations/test_optimizer.py OptimizerTests.test_optimize_elidable_operation
func TestOptimizer_OptimizeElidableOperation(t *testing.T) {
	assertOptimizesTo(t, "",
		ops(
			elidable(),
			model{name: "Foo", fields: m.Fields{f("name", charField(255))}}.op(),
			elidable(),
			model{name: "Bar", fields: m.Fields{f("size", intField())}}.op(),
			elidable(),
			&m.RenameModel{OldName: "Foo", NewName: "Phou"},
			&m.DeleteModel{Name: "Bar"},
			elidable(),
		),
		ops(model{
			name:   "Phou",
			table:  "migrations_foo", // RenameModel doesn't rename the table.
			fields: m.Fields{f("name", charField(255))},
		}.op()),
	)
}

// ---------------------------------------------------------------------------
// Index and constraint operations
// ---------------------------------------------------------------------------

// TestOptimizer_RenameIndex checks that RenameIndex absorbs a following
// RenameIndex on the same model.
//
// Django's first and third assertions use RenameIndex(old_fields=...), which
// gormgate doesn't have (an index is always renamed by name). The third
// assertion's point — that a non-matching pair is left alone — is kept here
// with a rename on a different model, which Django's reduce() also refuses.
//
// django: tests/migrations/test_optimizer.py OptimizerTests.test_rename_index
func TestOptimizer_RenameIndex(t *testing.T) {
	assertOptimizesTo(t, "",
		ops(
			&m.RenameIndex{ModelName: "Pony", NewName: "mid_name", OldName: "old_name"},
			&m.RenameIndex{ModelName: "Pony", NewName: "new_name", OldName: "mid_name"},
		),
		ops(&m.RenameIndex{ModelName: "Pony", NewName: "new_name", OldName: "old_name"}),
	)
	assertDoesNotOptimize(t, "",
		ops(
			&m.RenameIndex{ModelName: "Pony", NewName: "mid_name", OldName: "old_name"},
			&m.RenameIndex{ModelName: "Horse", NewName: "new_name", OldName: "mid_name"},
		),
	)
}

// TestOptimizer_AddRenameIndex checks that AddIndex absorbs a RenameIndex of
// the index it adds, for plain, expression and partial-expression indexes.
//
// django: tests/migrations/test_optimizer.py OptimizerTests.test_add_rename_index
func TestOptimizer_AddRenameIndex(t *testing.T) {
	tests := []struct {
		name  string
		index m.Index
	}{
		{"fields", m.Index{Name: "mid_name", Fields: []m.IndexField{
			{Column: "weight"}, {Column: "pink"},
		}}},
		{"expression", m.Index{Name: "mid_name", Fields: []m.IndexField{
			{Expression: "ABS(weight)"},
		}}},
		{"expression_with_condition", m.Index{Name: "mid_name", Fields: []m.IndexField{
			{Expression: "ABS(weight)"},
		}, Where: "weight > 0"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			renamed := tc.index.Clone()
			renamed.Name = "new_name"
			assertOptimizesTo(t, "",
				ops(
					&m.AddIndex{ModelName: "Pony", Index: tc.index.Clone()},
					&m.RenameIndex{ModelName: "Pony", NewName: "new_name", OldName: "mid_name"},
				),
				ops(&m.AddIndex{ModelName: "Pony", Index: renamed}),
			)
			assertDoesNotOptimize(t, "",
				ops(
					&m.AddIndex{ModelName: "Pony", Index: tc.index.Clone()},
					&m.RenameIndex{ModelName: "Pony", NewName: "new_name", OldName: "other_name"},
				),
			)
		})
	}
}

// TestOptimizer_AddRemoveIndex checks that RemoveIndex cancels AddIndex.
//
// django: tests/migrations/test_optimizer.py OptimizerTests.test_add_remove_index
func TestOptimizer_AddRemoveIndex(t *testing.T) {
	assertOptimizesTo(t, "",
		ops(
			&m.AddIndex{ModelName: "Pony", Index: m.Index{
				Name:   "idx_pony_weight_pink",
				Fields: []m.IndexField{{Column: "weight"}, {Column: "pink"}},
			}},
			&m.RemoveIndex{ModelName: "Pony", Name: "idx_pony_weight_pink"},
		),
		nil,
	)
}

// TestOptimizer_AddRemoveConstraint checks that RemoveConstraint cancels
// AddConstraint, but only for the same constraint name.
//
// django: tests/migrations/test_optimizer.py OptimizerTests.test_add_remove_constraint
func TestOptimizer_AddRemoveConstraint(t *testing.T) {
	gtConstraint := func() m.Constraint {
		return &m.CheckConstraint{Name: "constraint_pony_pink_gt_2", Check: "pink > 2"}
	}
	assertOptimizesTo(t, "",
		ops(
			&m.AddConstraint{ModelName: "Pony", Constraint: gtConstraint()},
			&m.RemoveConstraint{ModelName: "Pony", Name: "constraint_pony_pink_gt_2"},
		),
		nil,
	)
	assertDoesNotOptimize(t, "",
		ops(
			&m.AddConstraint{ModelName: "Pony", Constraint: gtConstraint()},
			&m.RemoveConstraint{ModelName: "Pony", Name: "other_name"},
		),
	)
}

// TestOptimizer_MultipleAlterConstraints checks that two AlterConstraint
// operations on the same constraint collapse into the second, and that ones on
// different constraints don't.
//
// django: tests/migrations/test_optimizer.py OptimizerTests.test_multiple_alter_constraints
func TestOptimizer_MultipleAlterConstraints(t *testing.T) {
	added := &m.CheckConstraint{Name: "pink_gt_2", Check: "pink > 2", ViolationErrorMessage: "ERROR"}
	altered := &m.CheckConstraint{Name: "pink_gt_2", Check: "pink > 2", ViolationErrorMessage: "error"}
	assertOptimizesTo(t, "",
		ops(
			&m.AlterConstraint{ModelName: "Pony", Name: "pink_gt_2", Constraint: added},
			&m.AlterConstraint{ModelName: "Pony", Name: "pink_gt_2", Constraint: altered},
		),
		ops(&m.AlterConstraint{ModelName: "Pony", Name: "pink_gt_2", Constraint: altered}),
	)

	other := &m.CheckConstraint{Name: "pink_gt_3", Check: "weight > 3", ViolationErrorMessage: "error"}
	assertDoesNotOptimize(t, "",
		ops(
			&m.AlterConstraint{ModelName: "Pony", Name: "pink_gt_2", Constraint: added},
			&m.AlterConstraint{ModelName: "Pony", Name: "pink_gt_3", Constraint: other},
		),
	)
}

// TestOptimizer_AlterRemoveConstraint checks that RemoveConstraint absorbs
// AlterConstraint.
//
// django: tests/migrations/test_optimizer.py OptimizerTests.test_alter_remove_constraint
func TestOptimizer_AlterRemoveConstraint(t *testing.T) {
	assertOptimizesTo(t, "",
		ops(
			&m.AlterConstraint{ModelName: "Pony", Name: "pink_gt_2", Constraint: &m.CheckConstraint{
				Name: "pink_gt_2", Check: "pink > 2",
			}},
			&m.RemoveConstraint{ModelName: "Pony", Name: "pink_gt_2"},
		),
		ops(&m.RemoveConstraint{ModelName: "Pony", Name: "pink_gt_2"}),
	)
}

// TestOptimizer_AddAlterConstraint checks that AddConstraint absorbs a
// following AlterConstraint of the same constraint.
//
// django: tests/migrations/test_optimizer.py OptimizerTests.test_add_alter_constraint
func TestOptimizer_AddAlterConstraint(t *testing.T) {
	constraint := &m.CheckConstraint{Name: "pink_gt_2", Check: "pink > 2"}
	constraintWithError := &m.CheckConstraint{
		Name: "pink_gt_2", Check: "pink > 2", ViolationErrorMessage: "error",
	}
	assertOptimizesTo(t, "",
		ops(
			&m.AddConstraint{ModelName: "Pony", Constraint: constraint},
			&m.AlterConstraint{ModelName: "Pony", Name: "pink_gt_2", Constraint: constraintWithError},
		),
		ops(&m.AddConstraint{ModelName: "Pony", Constraint: constraintWithError}),
	)
}

// TestOptimizer_CreateModelAddIndex checks that CreateModel absorbs AddIndex,
// appending to the indexes it already carries.
//
// django: tests/migrations/test_optimizer.py OptimizerTests.test_create_model_add_index
func TestOptimizer_CreateModelAddIndex(t *testing.T) {
	ageIndex := m.Index{Name: "idx_pony_age", Fields: []m.IndexField{{Column: "age"}}}
	weightIndex := m.Index{Name: "idx_pony_weight", Fields: []m.IndexField{{Column: "weight"}}}
	fields := m.Fields{f("weight", intField()), f("age", intField())}
	assertOptimizesTo(t, "",
		ops(
			model{name: "Pony", fields: fields, options: m.Options{Indexes: []m.Index{ageIndex}}}.op(),
			&m.AddIndex{ModelName: "Pony", Index: weightIndex},
		),
		ops(model{name: "Pony", fields: fields, options: m.Options{
			Indexes: []m.Index{ageIndex, weightIndex},
		}}.op()),
	)
}

// TestOptimizer_CreateModelRemoveIndex checks that CreateModel absorbs
// RemoveIndex.
//
// django: tests/migrations/test_optimizer.py OptimizerTests.test_create_model_remove_index
func TestOptimizer_CreateModelRemoveIndex(t *testing.T) {
	ageIndex := m.Index{Name: "idx_pony_age", Fields: []m.IndexField{{Column: "age"}}}
	weightIndex := m.Index{Name: "idx_pony_weight", Fields: []m.IndexField{{Column: "weight"}}}
	fields := m.Fields{f("weight", intField()), f("age", intField())}
	assertOptimizesTo(t, "",
		ops(
			model{name: "Pony", fields: fields, options: m.Options{
				Indexes: []m.Index{ageIndex, weightIndex},
			}}.op(),
			&m.RemoveIndex{ModelName: "Pony", Name: "idx_pony_age"},
		),
		ops(model{name: "Pony", fields: fields, options: m.Options{
			Indexes: []m.Index{weightIndex},
		}}.op()),
	)
}

// TestOptimizer_CreateModelRenameIndexNoOldFields checks that CreateModel does
// NOT absorb a RenameIndex (CreateModel has no branch for it, so the rename
// blocks the reduction).
//
// django: tests/migrations/test_optimizer.py OptimizerTests.test_create_model_rename_index_no_old_fields
func TestOptimizer_CreateModelRenameIndexNoOldFields(t *testing.T) {
	ageIndex := m.Index{Name: "idx_pony_age", Fields: []m.IndexField{{Column: "age"}}}
	assertDoesNotOptimize(t, "",
		ops(
			model{
				name:    "Pony",
				fields:  m.Fields{f("weight", intField()), f("age", intField())},
				options: m.Options{Indexes: []m.Index{ageIndex}},
			}.op(),
			&m.RenameIndex{ModelName: "Pony", NewName: "idx_pony_age_new", OldName: "idx_pony_age"},
		),
	)
}

// TestOptimizer_CreateModelAddConstraint checks that CreateModel absorbs
// AddConstraint.
//
// django: tests/migrations/test_optimizer.py OptimizerTests.test_create_model_add_constraint
func TestOptimizer_CreateModelAddConstraint(t *testing.T) {
	gtConstraint := &m.CheckConstraint{Name: "pony_weight_gt_0", Check: "weight > 0"}
	fields := m.Fields{f("weight", intField())}
	assertOptimizesTo(t, "",
		ops(
			model{name: "Pony", fields: fields}.op(),
			&m.AddConstraint{ModelName: "Pony", Constraint: gtConstraint},
		),
		ops(model{name: "Pony", fields: fields, options: m.Options{
			Constraints: []m.Constraint{gtConstraint},
		}}.op()),
	)
}

// TestOptimizer_CreateModelAlterConstraint checks that CreateModel absorbs
// AlterConstraint: the altered constraint is dropped and re-appended, so it
// moves to the end of the list.
//
// django: tests/migrations/test_optimizer.py OptimizerTests.test_create_model_alter_constraint
func TestOptimizer_CreateModelAlterConstraint(t *testing.T) {
	original := &m.CheckConstraint{Name: "pony_weight_gt_0", Check: "weight > 0"}
	altered := &m.CheckConstraint{
		Name: "pony_weight_gt_0", Check: "weight > 0", ViolationErrorMessage: "incorrect weight",
	}
	unique := &m.UniqueConstraint{Name: "pony_weight_unique", Fields: []string{"weight"}}
	fields := m.Fields{f("weight", intField())}
	assertOptimizesTo(t, "",
		ops(
			model{name: "Pony", fields: fields, options: m.Options{
				Constraints: []m.Constraint{original, unique},
			}}.op(),
			&m.AlterConstraint{ModelName: "Pony", Name: "pony_weight_gt_0", Constraint: altered},
		),
		ops(model{name: "Pony", fields: fields, options: m.Options{
			Constraints: []m.Constraint{unique, altered},
		}}.op()),
	)
}

// TestOptimizer_CreateModelRemoveConstraint checks that CreateModel absorbs
// RemoveConstraint.
//
// django: tests/migrations/test_optimizer.py OptimizerTests.test_create_model_remove_constraint
func TestOptimizer_CreateModelRemoveConstraint(t *testing.T) {
	check := &m.CheckConstraint{Name: "pony_weight_gt_0", Check: "weight > 0"}
	unique := &m.UniqueConstraint{Name: "pony_weight_unique", Fields: []string{"weight"}}
	fields := m.Fields{f("weight", intField())}
	assertOptimizesTo(t, "",
		ops(
			model{name: "Pony", fields: fields, options: m.Options{
				Constraints: []m.Constraint{check, unique},
			}}.op(),
			&m.RemoveConstraint{ModelName: "Pony", Name: "pony_weight_gt_0"},
		),
		ops(model{name: "Pony", fields: fields, options: m.Options{
			Constraints: []m.Constraint{unique},
		}}.op()),
	)
}

// ---------------------------------------------------------------------------
// gormgate additions (not in Django's test_optimizer.py)
// ---------------------------------------------------------------------------

// TestOptimizer_SpecialOperations covers RunSQL and SeparateDatabaseAndState,
// which gormgate has but test_optimizer.py never exercises directly. They
// inherit Operation.reduce: they block optimization unless one side is
// elidable.
//
// django: db/migrations/operations/base.py Operation.reduce
func TestOptimizer_SpecialOperations(t *testing.T) {
	// RunSQL blocks a CreateModel/DeleteModel pair from collapsing across it.
	assertDoesNotOptimize(t, "",
		ops(
			model{name: "Foo", fields: m.Fields{f("name", charField(255))}}.op(),
			&m.RunSQL{SQL: m.Script("SELECT 1"), ReverseSQL: m.NoSQL},
			&m.DeleteModel{Name: "Foo"},
		),
	)
	assertDoesNotOptimize(t, "",
		ops(
			model{name: "Foo", fields: m.Fields{f("name", charField(255))}}.op(),
			&m.SeparateDatabaseAndState{},
			&m.DeleteModel{Name: "Foo"},
		),
	)
	// An elidable RunSQL is dropped and doesn't block the collapse.
	assertOptimizesTo(t, "",
		ops(
			model{name: "Foo", fields: m.Fields{f("name", charField(255))}}.op(),
			&m.RunSQL{SQL: m.Script("SELECT 1"), ReverseSQL: m.NoSQL, IsElidable: true},
			&m.DeleteModel{Name: "Foo"},
		),
		nil,
	)
}
