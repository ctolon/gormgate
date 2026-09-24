package autodetector

// Port of Django's tests/migrations/test_autodetector.py: indexes,
// constraints, unique_together, cross-app dependencies, arrange_for_graph /
// _trim_to_apps / parse_number, and MigrationSuggestNameTests.

import (
	"strings"
	"testing"

	"github.com/ctolon/gormgate/internal/graph"
	"github.com/ctolon/gormgate/internal/questioner"
	m "github.com/ctolon/gormgate/migrations"
)

func getChangesGraph(t *testing.T, before, after []*m.ModelState, g *graph.Graph) map[string][]*m.Migration {
	t.Helper()
	ch, err := detect(projectState(before...), projectState(after...), nil, g)
	if err != nil {
		t.Fatalf("detectChanges: %v", err)
	}
	return ch
}

// ---------------------------------------------------------------------------
// indexes
// ---------------------------------------------------------------------------

// django: tests/migrations/test_autodetector.py AutodetectorTests.test_create_model_with_indexes
func TestAutodetector_CreateModelWithIndexes(t *testing.T) {
	addedIndex := index("create_model_with_indexes_idx", "name")
	author := withOptions(
		model("otherapp", "Author", field("id", autoField()), field("name", charField(200))),
		m.Options{Indexes: []m.Index{addedIndex}})
	changes := getChanges(t, nil, []*m.ModelState{author}, nil)
	assertNumberMigrations(t, changes, "otherapp", 1)
	assertEqual(t, "number of operations", len(changes["otherapp"][0].Operations), 1)
	assertOperationTypes(t, changes, "otherapp", 0, "CreateModel")
	op := operationAt[*m.CreateModel](t, changes, "otherapp", 0, 0)
	assertEqual(t, "name", op.Name, "Author")
	if len(op.Options.Indexes) != 1 || !op.Options.Indexes[0].Equal(addedIndex) {
		t.Fatalf("options.indexes: got %v, want [%v]", op.Options.Indexes, addedIndex)
	}
}

// django: tests/migrations/test_autodetector.py AutodetectorTests.test_add_indexes
func TestAutodetector_AddIndexes(t *testing.T) {
	changes := getChanges(t,
		[]*m.ModelState{authorEmpty(), book()},
		[]*m.ModelState{authorEmpty(), bookIndexes()}, nil)
	assertNumberMigrations(t, changes, "otherapp", 1)
	assertOperationTypes(t, changes, "otherapp", 0, "AddIndex")
	op := operationAt[*m.AddIndex](t, changes, "otherapp", 0, 0)
	assertEqual(t, "model_name", op.ModelName, "book")
	if !op.Index.Equal(index("book_title_author_idx", "author", "title")) {
		t.Fatalf("index: got %v", op.Index)
	}
}

// django: tests/migrations/test_autodetector.py AutodetectorTests.test_remove_indexes
func TestAutodetector_RemoveIndexes(t *testing.T) {
	changes := getChanges(t,
		[]*m.ModelState{authorEmpty(), bookIndexes()},
		[]*m.ModelState{authorEmpty(), book()}, nil)
	assertNumberMigrations(t, changes, "otherapp", 1)
	assertOperationTypes(t, changes, "otherapp", 0, "RemoveIndex")
	op := operationAt[*m.RemoveIndex](t, changes, "otherapp", 0, 0)
	assertEqual(t, "model_name", op.ModelName, "book")
	assertEqual(t, "name", op.Name, "book_title_author_idx")
}

// django: tests/migrations/test_autodetector.py AutodetectorTests.test_rename_indexes
func TestAutodetector_RenameIndexes(t *testing.T) {
	bookRenamedIndexes := withOptions(book(), m.Options{
		Indexes: []m.Index{index("renamed_book_title_author_idx", "author", "title")},
	})
	changes := getChanges(t,
		[]*m.ModelState{authorEmpty(), bookIndexes()},
		[]*m.ModelState{authorEmpty(), bookRenamedIndexes}, nil)
	assertNumberMigrations(t, changes, "otherapp", 1)
	assertOperationTypes(t, changes, "otherapp", 0, "RenameIndex")
	op := operationAt[*m.RenameIndex](t, changes, "otherapp", 0, 0)
	assertEqual(t, "model_name", op.ModelName, "book")
	assertEqual(t, "old_name", op.OldName, "book_title_author_idx")
	assertEqual(t, "new_name", op.NewName, "renamed_book_title_author_idx")
}

// django: tests/migrations/test_autodetector.py AutodetectorTests.test_order_fields_indexes
func TestAutodetector_OrderFieldsIndexes(t *testing.T) {
	changes := getChanges(t,
		[]*m.ModelState{authorEmpty(), bookIndexes()},
		[]*m.ModelState{authorEmpty(), bookUnorderedIndexes()}, nil)
	assertNumberMigrations(t, changes, "otherapp", 1)
	assertOperationTypes(t, changes, "otherapp", 0, "RemoveIndex", "AddIndex")
	rm := operationAt[*m.RemoveIndex](t, changes, "otherapp", 0, 0)
	assertEqual(t, "model_name", rm.ModelName, "book")
	assertEqual(t, "name", rm.Name, "book_title_author_idx")
	add := operationAt[*m.AddIndex](t, changes, "otherapp", 0, 1)
	assertEqual(t, "model_name", add.ModelName, "book")
	if !add.Index.Equal(index("book_author_title_idx", "title", "author")) {
		t.Fatalf("index: got %v", add.Index)
	}
}

// django: tests/migrations/test_autodetector.py AutodetectorTests.test_add_index_with_new_model
func TestAutodetector_AddIndexWithNewModel(t *testing.T) {
	bookWithIndexTitleAndPony := withOptions(
		model("otherapp", "Book", field("id", autoField()), field("title", charField(200)),
			field("pony", fk("otherapp.Pony"))),
		m.Options{Indexes: []m.Index{index("index_title_pony", "title", "pony")}})
	changes := getChanges(t,
		[]*m.ModelState{bookWithNoAuthor()},
		[]*m.ModelState{bookWithIndexTitleAndPony, otherPony()}, nil)
	assertNumberMigrations(t, changes, "otherapp", 1)
	assertOperationTypes(t, changes, "otherapp", 0, "CreateModel", "AddField", "AddIndex")
}

// django: tests/migrations/test_autodetector.py AutodetectorTests.test_remove_field_with_model_options
func TestAutodetector_RemoveFieldWithModelOptions(t *testing.T) {
	dog := withOptions(
		model("testapp", "Dog", field("name", charField(100)), field("animal", fk("testapp.Animal"))),
		m.Options{
			Indexes:     []m.Index{index("animal_name_idx", "animal", "name")},
			Constraints: []m.Constraint{uniqueConstraint("animal_name_uniq", "animal", "name")},
		})
	before := []*m.ModelState{model("testapp", "Animal"), dog}
	changes := getChanges(t, before, nil, nil)
	assertNumberMigrations(t, changes, "testapp", 1)
	assertOperationTypes(t, changes, "testapp", 0,
		"RemoveIndex", "RemoveConstraint", "RemoveField", "DeleteModel", "DeleteModel")
}

// django: tests/migrations/test_autodetector.py
// AutodetectorTests.test_remove_field_with_remove_index_or_constraint_dependency
func TestAutodetector_RemoveFieldWithRemoveIndexOrConstraintDependency(t *testing.T) {
	modelState := withOptions(
		model("testapp", "Model", field("date", autoNow(dateTimeField())),
			field("category", null(fk("testapp.Category")))),
		m.Options{
			Constraints: []m.Constraint{uniqueConstraint("unique_category_for_date", "date", "category")},
		})
	before := []*m.ModelState{model("testapp", "Category"), modelState}
	after := []*m.ModelState{model("testapp", "Model", field("date", autoNow(dateTimeField())))}
	changes := getChanges(t, before, after, nil)
	assertNumberMigrations(t, changes, "testapp", 1)
	assertOperationTypes(t, changes, "testapp", 0, "RemoveConstraint", "RemoveField", "DeleteModel")
}

// ---------------------------------------------------------------------------
// constraints
// ---------------------------------------------------------------------------

// django: tests/migrations/test_autodetector.py AutodetectorTests.test_create_model_with_check_constraint
func TestAutodetector_CreateModelWithCheckConstraint(t *testing.T) {
	constraint := checkConstraint("name_contains_bob", "name LIKE '%Bob%'")
	author := withOptions(
		model("otherapp", "Author", field("id", autoField()), field("name", charField(200))),
		m.Options{Constraints: []m.Constraint{constraint}})
	changes := getChanges(t, nil, []*m.ModelState{author}, nil)
	assertNumberMigrations(t, changes, "otherapp", 1)
	assertEqual(t, "number of operations", len(changes["otherapp"][0].Operations), 1)
	assertOperationTypes(t, changes, "otherapp", 0, "CreateModel")
	op := operationAt[*m.CreateModel](t, changes, "otherapp", 0, 0)
	assertEqual(t, "name", op.Name, "Author")
	if len(op.Options.Constraints) != 1 || !m.ConstraintEqual(op.Options.Constraints[0], constraint) {
		t.Fatalf("options.constraints: got %v", op.Options.Constraints)
	}
}

// django: tests/migrations/test_autodetector.py AutodetectorTests.test_add_constraints
func TestAutodetector_AddConstraints(t *testing.T) {
	changes := getChanges(t,
		[]*m.ModelState{authorName()}, []*m.ModelState{authorNameCheckConstraint()}, nil)
	assertNumberMigrations(t, changes, "testapp", 1)
	assertOperationTypes(t, changes, "testapp", 0, "AddConstraint")
	op := operationAt[*m.AddConstraint](t, changes, "testapp", 0, 0)
	assertEqual(t, "model_name", op.ModelName, "author")
	if !m.ConstraintEqual(op.Constraint, checkConstraint("name_contains_bob", "name LIKE '%Bob%'")) {
		t.Fatalf("constraint: got %v", op.Constraint)
	}
}

// django: tests/migrations/test_autodetector.py AutodetectorTests.test_add_constraints_with_new_model
func TestAutodetector_AddConstraintsWithNewModel(t *testing.T) {
	bookWithUniqueTitleAndPony := withOptions(
		model("otherapp", "Book", field("id", autoField()), field("title", charField(200)),
			field("pony", fk("otherapp.Pony"))),
		m.Options{Constraints: []m.Constraint{uniqueConstraint("unique_title_pony", "title", "pony")}})
	changes := getChanges(t,
		[]*m.ModelState{bookWithNoAuthor()},
		[]*m.ModelState{bookWithUniqueTitleAndPony, otherPony()}, nil)
	assertNumberMigrations(t, changes, "otherapp", 1)
	assertOperationTypes(t, changes, "otherapp", 0, "CreateModel", "AddField", "AddConstraint")
}

// django: tests/migrations/test_autodetector.py AutodetectorTests.test_alter_constraint
//
// Django distinguishes violation_error_code and violation_error_message;
// gormgate keeps only ViolationErrorMessage, which is likewise state-only and
// therefore produces AlterConstraint rather than a drop/recreate.
func TestAutodetector_AlterConstraint(t *testing.T) {
	bookConstraint := checkConstraint("title_contains_title", "title LIKE '%title%'")
	bookAltered := checkConstraint("title_contains_title", "title LIKE '%title%'")
	bookAltered.ViolationErrorMessage = "Title doesn't contain title"
	authorAltered := checkConstraint("name_contains_bob", "name LIKE '%Bob%'")
	authorAltered.ViolationErrorMessage = "Name doesn't contain Bob"

	bookCheck := withOptions(book(), m.Options{Constraints: []m.Constraint{bookConstraint}})
	bookCheckAltered := withOptions(book(), m.Options{Constraints: []m.Constraint{bookAltered}})
	authorAlteredState := withOptions(authorName(), m.Options{Constraints: []m.Constraint{authorAltered}})

	changes := getChanges(t,
		[]*m.ModelState{authorNameCheckConstraint(), bookCheck},
		[]*m.ModelState{authorAlteredState, bookCheckAltered}, nil)

	assertNumberMigrations(t, changes, "testapp", 1)
	assertOperationTypes(t, changes, "testapp", 0, "AlterConstraint")
	op := operationAt[*m.AlterConstraint](t, changes, "testapp", 0, 0)
	assertEqual(t, "model_name", op.ModelName, "author")
	assertEqual(t, "name", op.Name, "name_contains_bob")
	if !m.ConstraintEqual(op.Constraint, authorAltered) {
		t.Fatalf("constraint: got %v", op.Constraint)
	}

	assertNumberMigrations(t, changes, "otherapp", 1)
	assertOperationTypes(t, changes, "otherapp", 0, "AlterConstraint")
	bop := operationAt[*m.AlterConstraint](t, changes, "otherapp", 0, 0)
	assertEqual(t, "model_name", bop.ModelName, "book")
	assertEqual(t, "name", bop.Name, "title_contains_title")
	if !m.ConstraintEqual(bop.Constraint, bookAltered) {
		t.Fatalf("constraint: got %v", bop.Constraint)
	}
	assertMigrationDependencies(t, changes, "otherapp", 0, deps(key("testapp", "auto_1")))
}

// django: tests/migrations/test_autodetector.py AutodetectorTests.test_remove_constraints
func TestAutodetector_RemoveConstraints(t *testing.T) {
	changes := getChanges(t,
		[]*m.ModelState{authorNameCheckConstraint()}, []*m.ModelState{authorName()}, nil)
	assertNumberMigrations(t, changes, "testapp", 1)
	assertOperationTypes(t, changes, "testapp", 0, "RemoveConstraint")
	op := operationAt[*m.RemoveConstraint](t, changes, "testapp", 0, 0)
	assertEqual(t, "model_name", op.ModelName, "author")
	assertEqual(t, "name", op.Name, "name_contains_bob")
}

// django: tests/migrations/test_autodetector.py AutodetectorTests.test_constraint_dropped_and_recreated
func TestAutodetector_ConstraintDroppedAndRecreated(t *testing.T) {
	altered := checkConstraint("name_contains_bob", "name LIKE '%bob%'")
	lowercased := withOptions(authorName(), m.Options{Constraints: []m.Constraint{altered}})
	changes := getChanges(t,
		[]*m.ModelState{authorNameCheckConstraint()}, []*m.ModelState{lowercased}, nil)
	assertNumberMigrations(t, changes, "testapp", 1)
	assertOperationTypes(t, changes, "testapp", 0, "RemoveConstraint", "AddConstraint")
	rm := operationAt[*m.RemoveConstraint](t, changes, "testapp", 0, 0)
	assertEqual(t, "model_name", rm.ModelName, "author")
	assertEqual(t, "name", rm.Name, "name_contains_bob")
	add := operationAt[*m.AddConstraint](t, changes, "testapp", 0, 1)
	assertEqual(t, "model_name", add.ModelName, "author")
	if !m.ConstraintEqual(add.Constraint, altered) {
		t.Fatalf("constraint: got %v", add.Constraint)
	}
}

// ---------------------------------------------------------------------------
// unique_together
// ---------------------------------------------------------------------------

// django: tests/migrations/test_autodetector.py AutodetectorTests.test_empty_unique_together
func TestAutodetector_EmptyUniqueTogether(t *testing.T) {
	notSpecified := model("a", "model", field("id", autoField()))
	// Django distinguishes "not specified", None and the empty set; in Go a
	// nil slice and an empty non-nil slice are the two representable forms.
	none := withOptions(notSpecified, m.Options{UniqueTogether: nil})
	empty := withOptions(notSpecified, m.Options{UniqueTogether: [][]string{}})

	cases := []struct {
		from, to *m.ModelState
		msg      string
	}{
		{notSpecified, notSpecified, `"not specified" to "not specified"`},
		{notSpecified, none, `"not specified" to "None"`},
		{notSpecified, empty, `"not specified" to "empty"`},
		{none, notSpecified, `"None" to "not specified"`},
		{none, none, `"None" to "None"`},
		{none, empty, `"None" to "empty"`},
		{empty, notSpecified, `"empty" to "not specified"`},
		{empty, none, `"empty" to "None"`},
		{empty, empty, `"empty" to "empty"`},
	}
	for _, c := range cases {
		changes := getChanges(t, []*m.ModelState{c.from}, []*m.ModelState{c.to}, nil)
		if len(changes) > 0 {
			t.Fatalf("Created operation(s) %s from %s",
				strings.Join(opTypes(changes["a"][0].Operations), ", "), c.msg)
		}
	}
}

// django: tests/migrations/test_autodetector.py AutodetectorTests.test_add_unique_together
func TestAutodetector_AddUniqueTogether(t *testing.T) {
	changes := getChanges(t,
		[]*m.ModelState{authorEmpty(), book()},
		[]*m.ModelState{authorEmpty(), bookUniqueTogether()}, nil)
	assertNumberMigrations(t, changes, "otherapp", 1)
	assertOperationTypes(t, changes, "otherapp", 0, "AlterUniqueTogether")
	op := operationAt[*m.AlterUniqueTogether](t, changes, "otherapp", 0, 0)
	assertEqual(t, "name", op.Name, "book")
	assertUniqueTogether(t, op.UniqueTogether, [][]string{{"author", "title"}})
}

// django: tests/migrations/test_autodetector.py AutodetectorTests.test_remove_unique_together
func TestAutodetector_RemoveUniqueTogether(t *testing.T) {
	changes := getChanges(t,
		[]*m.ModelState{authorEmpty(), bookUniqueTogether()},
		[]*m.ModelState{authorEmpty(), book()}, nil)
	assertNumberMigrations(t, changes, "otherapp", 1)
	assertOperationTypes(t, changes, "otherapp", 0, "AlterUniqueTogether")
	op := operationAt[*m.AlterUniqueTogether](t, changes, "otherapp", 0, 0)
	assertEqual(t, "name", op.Name, "book")
	assertUniqueTogether(t, op.UniqueTogether, nil)
}

// django: tests/migrations/test_autodetector.py AutodetectorTests.test_unique_together_remove_fk
func TestAutodetector_UniqueTogetherRemoveFk(t *testing.T) {
	changes := getChanges(t,
		[]*m.ModelState{authorEmpty(), bookUniqueTogether()},
		[]*m.ModelState{authorEmpty(), bookWithNoAuthor()}, nil)
	assertNumberMigrations(t, changes, "otherapp", 1)
	assertOperationTypes(t, changes, "otherapp", 0, "AlterUniqueTogether", "RemoveField")
	ut := operationAt[*m.AlterUniqueTogether](t, changes, "otherapp", 0, 0)
	assertEqual(t, "name", ut.Name, "book")
	assertUniqueTogether(t, ut.UniqueTogether, nil)
	rf := operationAt[*m.RemoveField](t, changes, "otherapp", 0, 1)
	assertEqual(t, "model_name", rf.ModelName, "book")
	assertEqual(t, "name", rf.Name, "author")
}

// django: tests/migrations/test_autodetector.py AutodetectorTests.test_unique_together_no_changes
func TestAutodetector_UniqueTogetherNoChanges(t *testing.T) {
	changes := getChanges(t,
		[]*m.ModelState{authorEmpty(), bookUniqueTogether()},
		[]*m.ModelState{authorEmpty(), bookUniqueTogether()}, nil)
	assertEqual(t, "number of apps with changes", len(changes), 0)
}

// django: tests/migrations/test_autodetector.py AutodetectorTests.test_unique_together_ordering
func TestAutodetector_UniqueTogetherOrdering(t *testing.T) {
	changes := getChanges(t,
		[]*m.ModelState{authorEmpty(), bookUniqueTogether()},
		[]*m.ModelState{authorEmpty(), bookUniqueTogether2()}, nil)
	assertNumberMigrations(t, changes, "otherapp", 1)
	assertOperationTypes(t, changes, "otherapp", 0, "AlterUniqueTogether")
	op := operationAt[*m.AlterUniqueTogether](t, changes, "otherapp", 0, 0)
	assertEqual(t, "name", op.Name, "book")
	assertUniqueTogether(t, op.UniqueTogether, [][]string{{"title", "author"}})
}

// django: tests/migrations/test_autodetector.py AutodetectorTests.test_add_field_and_unique_together
func TestAutodetector_AddFieldAndUniqueTogether(t *testing.T) {
	changes := getChanges(t,
		[]*m.ModelState{authorEmpty(), book()},
		[]*m.ModelState{authorEmpty(), bookUniqueTogether3()}, nil)
	assertNumberMigrations(t, changes, "otherapp", 1)
	assertOperationTypes(t, changes, "otherapp", 0, "AddField", "AlterUniqueTogether")
	op := operationAt[*m.AlterUniqueTogether](t, changes, "otherapp", 0, 1)
	assertEqual(t, "name", op.Name, "book")
	assertUniqueTogether(t, op.UniqueTogether, [][]string{{"title", "newfield"}})
}

// django: tests/migrations/test_autodetector.py AutodetectorTests.test_create_model_and_unique_together
func TestAutodetector_CreateModelAndUniqueTogether(t *testing.T) {
	author := model("otherapp", "Author", field("id", autoField()), field("name", charField(200)))
	bookWithAuthor := withOptions(
		model("otherapp", "Book", field("id", autoField()),
			field("author", fk("otherapp.Author")), field("title", charField(200))),
		m.Options{UniqueTogether: [][]string{{"title", "author"}}})
	changes := getChanges(t,
		[]*m.ModelState{bookWithNoAuthor()},
		[]*m.ModelState{author, bookWithAuthor}, nil)
	assertNumberMigrations(t, changes, "otherapp", 1)
	assertEqual(t, "number of operations", len(changes["otherapp"][0].Operations), 3)
	assertOperationTypes(t, changes, "otherapp", 0, "CreateModel", "AddField", "AlterUniqueTogether")
}

// django: tests/migrations/test_autodetector.py AutodetectorTests.test_remove_field_and_unique_together
func TestAutodetector_RemoveFieldAndUniqueTogether(t *testing.T) {
	changes := getChanges(t,
		[]*m.ModelState{authorEmpty(), bookUniqueTogether3()},
		[]*m.ModelState{authorEmpty(), bookUniqueTogether()}, nil)
	assertNumberMigrations(t, changes, "otherapp", 1)
	assertOperationTypes(t, changes, "otherapp", 0, "AlterUniqueTogether", "RemoveField")
	ut := operationAt[*m.AlterUniqueTogether](t, changes, "otherapp", 0, 0)
	assertEqual(t, "name", ut.Name, "book")
	assertUniqueTogether(t, ut.UniqueTogether, [][]string{{"author", "title"}})
	rf := operationAt[*m.RemoveField](t, changes, "otherapp", 0, 1)
	assertEqual(t, "model_name", rf.ModelName, "book")
	assertEqual(t, "name", rf.Name, "newfield")
}

// django: tests/migrations/test_autodetector.py AutodetectorTests.test_alter_field_and_unique_together
//
// Django's fixture drops db_index from `age`; gormgate has no per-field index
// flag, so `age` widens and `name` becomes unique instead. The point of the
// test — field alterations happen between the two AlterUniqueTogether
// operations — is unchanged.
func TestAutodetector_AlterFieldAndUniqueTogether(t *testing.T) {
	initialAuthor := withOptions(
		model("testapp", "Author", field("id", autoField()), field("name", charField(200)),
			field("age", intField())),
		m.Options{UniqueTogether: [][]string{{"name"}}})
	reversed := withOptions(
		model("testapp", "Author", field("id", autoField()), field("name", unique(charField(200))),
			field("age", m.Field{Type: m.Int, Size: 64})),
		m.Options{UniqueTogether: [][]string{{"age"}}})
	changes := getChanges(t, []*m.ModelState{initialAuthor}, []*m.ModelState{reversed}, nil)
	assertNumberMigrations(t, changes, "testapp", 1)
	assertOperationTypes(t, changes, "testapp", 0,
		"AlterUniqueTogether", "AlterField", "AlterField", "AlterUniqueTogether")
	first := operationAt[*m.AlterUniqueTogether](t, changes, "testapp", 0, 0)
	assertEqual(t, "name", first.Name, "author")
	assertUniqueTogether(t, first.UniqueTogether, nil)
	assertEqual(t, "op1 name", operationAt[*m.AlterField](t, changes, "testapp", 0, 1).Name, "age")
	assertEqual(t, "op2 name", operationAt[*m.AlterField](t, changes, "testapp", 0, 2).Name, "name")
	last := operationAt[*m.AlterUniqueTogether](t, changes, "testapp", 0, 3)
	assertEqual(t, "name", last.Name, "author")
	assertUniqueTogether(t, last.UniqueTogether, [][]string{{"age"}})
}

// django: tests/migrations/test_autodetector.py
// AutodetectorTests.test_partly_alter_unique_together_increase
func TestAutodetector_PartlyAlterUniqueTogetherIncrease(t *testing.T) {
	base := model("testapp", "Author", field("id", autoField()),
		field("name", charField(200)), field("age", intField()))
	initial := withOptions(base, m.Options{UniqueTogether: [][]string{{"name"}}})
	after := withOptions(base, m.Options{UniqueTogether: [][]string{{"name"}, {"age"}}})
	changes := getChanges(t, []*m.ModelState{initial}, []*m.ModelState{after}, nil)
	assertNumberMigrations(t, changes, "testapp", 1)
	assertOperationTypes(t, changes, "testapp", 0, "AlterUniqueTogether")
	op := operationAt[*m.AlterUniqueTogether](t, changes, "testapp", 0, 0)
	assertEqual(t, "name", op.Name, "author")
	assertUniqueTogether(t, op.UniqueTogether, [][]string{{"name"}, {"age"}})
}

// django: tests/migrations/test_autodetector.py
// AutodetectorTests.test_partly_alter_unique_together_decrease
func TestAutodetector_PartlyAlterUniqueTogetherDecrease(t *testing.T) {
	base := model("testapp", "Author", field("id", autoField()),
		field("name", charField(200)), field("age", intField()))
	initial := withOptions(base, m.Options{UniqueTogether: [][]string{{"name"}, {"age"}}})
	after := withOptions(base, m.Options{UniqueTogether: [][]string{{"name"}}})
	changes := getChanges(t, []*m.ModelState{initial}, []*m.ModelState{after}, nil)
	assertNumberMigrations(t, changes, "testapp", 1)
	assertOperationTypes(t, changes, "testapp", 0, "AlterUniqueTogether")
	op := operationAt[*m.AlterUniqueTogether](t, changes, "testapp", 0, 0)
	assertEqual(t, "name", op.Name, "author")
	assertUniqueTogether(t, op.UniqueTogether, [][]string{{"name"}})
}

// django: tests/migrations/test_autodetector.py AutodetectorTests.test_rename_field_and_unique_together
func TestAutodetector_RenameFieldAndUniqueTogether(t *testing.T) {
	changes := getChanges(t,
		[]*m.ModelState{authorEmpty(), bookUniqueTogether3()},
		[]*m.ModelState{authorEmpty(), bookUniqueTogether4()},
		defaults(questioner.Defaults{Rename: true}))
	assertNumberMigrations(t, changes, "otherapp", 1)
	assertOperationTypes(t, changes, "otherapp", 0, "RenameField", "AlterUniqueTogether")
	op := operationAt[*m.AlterUniqueTogether](t, changes, "otherapp", 0, 1)
	assertEqual(t, "name", op.Name, "book")
	assertUniqueTogether(t, op.UniqueTogether, [][]string{{"title", "newfield2"}})
}

// ---------------------------------------------------------------------------
// dependencies
// ---------------------------------------------------------------------------

// django: tests/migrations/test_autodetector.py AutodetectorTests.test_fk_dependency
func TestAutodetector_FkDependency(t *testing.T) {
	changes := getChanges(t, nil, []*m.ModelState{authorName(), book(), edition()}, nil)
	assertNumberMigrations(t, changes, "testapp", 1)
	assertOperationTypes(t, changes, "testapp", 0, "CreateModel")
	assertEqual(t, "name", operationAt[*m.CreateModel](t, changes, "testapp", 0, 0).Name, "Author")
	assertMigrationDependencies(t, changes, "testapp", 0, nil)

	assertNumberMigrations(t, changes, "otherapp", 1)
	assertOperationTypes(t, changes, "otherapp", 0, "CreateModel")
	assertEqual(t, "name", operationAt[*m.CreateModel](t, changes, "otherapp", 0, 0).Name, "Book")
	assertMigrationDependencies(t, changes, "otherapp", 0, deps(key("testapp", "auto_1")))

	assertNumberMigrations(t, changes, "thirdapp", 1)
	assertOperationTypes(t, changes, "thirdapp", 0, "CreateModel")
	assertEqual(t, "name", operationAt[*m.CreateModel](t, changes, "thirdapp", 0, 0).Name, "Edition")
	assertMigrationDependencies(t, changes, "thirdapp", 0, deps(key("otherapp", "auto_1")))
}

// django: tests/migrations/test_autodetector.py AutodetectorTests.test_same_app_no_fk_dependency
func TestAutodetector_SameAppNoFkDependency(t *testing.T) {
	changes := getChanges(t, nil, []*m.ModelState{authorWithPublisher(), publisher()}, nil)
	assertNumberMigrations(t, changes, "testapp", 1)
	assertOperationTypes(t, changes, "testapp", 0, "CreateModel", "CreateModel")
	assertEqual(t, "op0", operationAt[*m.CreateModel](t, changes, "testapp", 0, 0).Name, "Publisher")
	assertEqual(t, "op1", operationAt[*m.CreateModel](t, changes, "testapp", 0, 1).Name, "Author")
	assertMigrationDependencies(t, changes, "testapp", 0, nil)
}

// django: tests/migrations/test_autodetector.py AutodetectorTests.test_circular_fk_dependency
func TestAutodetector_CircularFkDependency(t *testing.T) {
	changes := getChanges(t, nil,
		[]*m.ModelState{authorWithBook(), book(), publisherWithBook()}, nil)
	assertNumberMigrations(t, changes, "testapp", 1)
	assertOperationTypes(t, changes, "testapp", 0, "CreateModel", "CreateModel")
	assertEqual(t, "op0", operationAt[*m.CreateModel](t, changes, "testapp", 0, 0).Name, "Author")
	assertEqual(t, "op1", operationAt[*m.CreateModel](t, changes, "testapp", 0, 1).Name, "Publisher")
	assertMigrationDependencies(t, changes, "testapp", 0, deps(key("otherapp", "auto_1")))

	assertNumberMigrations(t, changes, "otherapp", 2)
	assertOperationTypes(t, changes, "otherapp", 0, "CreateModel")
	assertOperationTypes(t, changes, "otherapp", 1, "AddField")
	assertMigrationDependencies(t, changes, "otherapp", 0, nil)
	assertMigrationDependencies(t, changes, "otherapp", 1,
		deps(key("otherapp", "auto_1"), key("testapp", "auto_1")))

	// Both split migrations should be `initial`.
	if !changes["otherapp"][0].IsInitial() || !changes["otherapp"][1].IsInitial() {
		t.Fatal("both split otherapp migrations should be initial")
	}
}

// django: tests/migrations/test_autodetector.py AutodetectorTests.test_same_app_circular_fk_dependency
func TestAutodetector_SameAppCircularFkDependency(t *testing.T) {
	changes := getChanges(t, nil,
		[]*m.ModelState{authorWithPublisher(), publisherWithAuthor()}, nil)
	assertNumberMigrations(t, changes, "testapp", 1)
	assertOperationTypes(t, changes, "testapp", 0, "CreateModel", "CreateModel", "AddField")
	assertEqual(t, "op0", operationAt[*m.CreateModel](t, changes, "testapp", 0, 0).Name, "Author")
	assertEqual(t, "op1", operationAt[*m.CreateModel](t, changes, "testapp", 0, 1).Name, "Publisher")
	assertEqual(t, "op2", operationAt[*m.AddField](t, changes, "testapp", 0, 2).Name, "publisher")
	assertMigrationDependencies(t, changes, "testapp", 0, nil)
}

// django: tests/migrations/test_autodetector.py
// AutodetectorTests.test_same_app_circular_fk_dependency_with_unique_together_and_indexes
func TestAutodetector_SameAppCircularFkDependencyWithUniqueTogetherAndIndexes(t *testing.T) {
	changes := getChanges(t, nil, []*m.ModelState{knight(), rabbit()}, nil)
	assertNumberMigrations(t, changes, "eggs", 1)
	assertOperationTypes(t, changes, "eggs", 0, "CreateModel", "CreateModel")
	knightOp := operationAt[*m.CreateModel](t, changes, "eggs", 0, 0)
	if len(knightOp.Options.UniqueTogether) != 0 {
		t.Fatalf("Knight's CreateModel must not carry unique_together, got %v",
			knightOp.Options.UniqueTogether)
	}
	assertMigrationDependencies(t, changes, "eggs", 0, nil)
}

// django: tests/migrations/test_autodetector.py AutodetectorTests.test_pk_fk_included
func TestAutodetector_PkFkIncluded(t *testing.T) {
	changes := getChanges(t, nil, []*m.ModelState{aardvarkPKFKAuthor(), authorName()}, nil)
	assertNumberMigrations(t, changes, "testapp", 1)
	assertOperationTypes(t, changes, "testapp", 0, "CreateModel", "CreateModel")
	assertEqual(t, "op0", operationAt[*m.CreateModel](t, changes, "testapp", 0, 0).Name, "Author")
	assertEqual(t, "op1", operationAt[*m.CreateModel](t, changes, "testapp", 0, 1).Name, "Aardvark")
}

// django: tests/migrations/test_autodetector.py AutodetectorTests.test_first_dependency
func TestAutodetector_FirstDependency(t *testing.T) {
	bookMigrationsFk := model("otherapp", "Book", field("id", autoField()),
		field("author", fk("migrations.UnmigratedModel")), field("title", charField(200)))
	// An empty graph stands for the loader graph of a project whose
	// "migrations" app has no migrations yet.
	changes := getChangesGraph(t, nil, []*m.ModelState{bookMigrationsFk}, graph.New())
	assertNumberMigrations(t, changes, "otherapp", 1)
	assertOperationTypes(t, changes, "otherapp", 0, "CreateModel")
	assertEqual(t, "name", operationAt[*m.CreateModel](t, changes, "otherapp", 0, 0).Name, "Book")
	assertMigrationDependencies(t, changes, "otherapp", 0, deps(key("migrations", "__first__")))
}

// django: tests/migrations/test_autodetector.py AutodetectorTests.test_last_dependency
func TestAutodetector_LastDependency(t *testing.T) {
	bookMigrationsFk := model("otherapp", "Book", field("id", autoField()),
		field("author", fk("migrations.UnmigratedModel")), field("title", charField(200)))
	g := graph.New()
	g.AddNode(key("migrations", "0001_initial"), nil)
	g.AddNode(key("migrations", "0002_second"), nil)
	if err := g.AddDependency("migrations.0002_second",
		key("migrations", "0002_second"), key("migrations", "0001_initial"), true); err != nil {
		t.Fatal(err)
	}
	changes := getChangesGraph(t, nil, []*m.ModelState{bookMigrationsFk}, g)
	assertNumberMigrations(t, changes, "otherapp", 1)
	assertOperationTypes(t, changes, "otherapp", 0, "CreateModel")
	assertEqual(t, "name", operationAt[*m.CreateModel](t, changes, "otherapp", 0, 0).Name, "Book")
	assertMigrationDependencies(t, changes, "otherapp", 0, deps(key("migrations", "0002_second")))
}

// django: tests/migrations/test_autodetector.py AutodetectorTests.test_fk_dependency_other_app
func TestAutodetector_FkDependencyOtherApp(t *testing.T) {
	changes := getChanges(t,
		[]*m.ModelState{authorName(), book()},
		[]*m.ModelState{authorWithBook(), book()}, nil)
	assertNumberMigrations(t, changes, "testapp", 1)
	assertOperationTypes(t, changes, "testapp", 0, "AddField")
	assertEqual(t, "name", operationAt[*m.AddField](t, changes, "testapp", 0, 0).Name, "book")
	assertMigrationDependencies(t, changes, "testapp", 0, deps(key("otherapp", "__first__")))
}

// django: tests/migrations/test_autodetector.py
// AutodetectorTests.test_alter_field_to_fk_dependency_other_app
func TestAutodetector_AlterFieldToFkDependencyOtherApp(t *testing.T) {
	changes := getChanges(t,
		[]*m.ModelState{authorEmpty(), bookWithNoAuthorFK()},
		[]*m.ModelState{authorEmpty(), book()}, nil)
	assertNumberMigrations(t, changes, "otherapp", 1)
	assertOperationTypes(t, changes, "otherapp", 0, "AlterField")
	assertMigrationDependencies(t, changes, "otherapp", 0, deps(key("testapp", "__first__")))
}

// django: tests/migrations/test_autodetector.py
// AutodetectorTests.test_circular_dependency_mixed_addcreate
func TestAutodetector_CircularDependencyMixedAddcreate(t *testing.T) {
	address := model("a", "Address", field("id", autoField()), field("country", fk("b.DeliveryCountry")))
	person := model("a", "Person", field("id", autoField()))
	apackage := model("b", "APackage", field("id", autoField()), field("person", fk("a.Person")))
	country := model("b", "DeliveryCountry", field("id", autoField()))
	changes := getChanges(t, nil, []*m.ModelState{address, person, apackage, country}, nil)
	assertNumberMigrations(t, changes, "a", 2)
	assertNumberMigrations(t, changes, "b", 1)
	assertOperationTypes(t, changes, "a", 0, "CreateModel", "CreateModel")
	assertOperationTypes(t, changes, "a", 1, "AddField")
	assertOperationTypes(t, changes, "b", 0, "CreateModel", "CreateModel")
}

// ---------------------------------------------------------------------------
// arrange_for_graph / _trim_to_apps / parse_number
// ---------------------------------------------------------------------------

// django: tests/migrations/test_autodetector.py AutodetectorTests.test_arrange_for_graph
func TestAutodetector_ArrangeForGraph(t *testing.T) {
	g := graph.New()
	g.AddNode(key("testapp", "0001_initial"), nil)
	g.AddNode(key("testapp", "0002_foobar"), nil)
	g.AddNode(key("otherapp", "0001_initial"), nil)
	mustDep(t, g, "testapp.0002_foobar", key("testapp", "0002_foobar"), key("testapp", "0001_initial"))
	mustDep(t, g, "testapp.0002_foobar", key("testapp", "0002_foobar"), key("otherapp", "0001_initial"))

	before := projectState(publisher(), otherPony())
	after := projectState(authorEmpty(), publisher(), otherPony(), otherStable())
	ad := New(before, after, defaults(questioner.Defaults{}))
	changes, err := ad.detectChanges(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	changes, err = ad.ArrangeForGraph(changes, g, "")
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, "testapp name", changes["testapp"][0].Name, "0003_author")
	assertMigrationDependencies(t, changes, "testapp", 0, deps(key("testapp", "0002_foobar")))
	assertEqual(t, "otherapp name", changes["otherapp"][0].Name, "0002_stable")
	assertMigrationDependencies(t, changes, "otherapp", 0, deps(key("otherapp", "0001_initial")))
}

func mustDep(t *testing.T, g *graph.Graph, mig string, child, parent m.Key) {
	t.Helper()
	if err := g.AddDependency(mig, child, parent, false); err != nil {
		t.Fatal(err)
	}
}

// django: tests/migrations/test_autodetector.py
// AutodetectorTests.test_arrange_for_graph_with_multiple_initial
func TestAutodetector_ArrangeForGraphWithMultipleInitial(t *testing.T) {
	attribution := model("otherapp", "Attribution", field("id", autoField()),
		field("author", fk("testapp.Author")), field("book", fk("otherapp.Book")))
	before := projectState()
	after := projectState(authorWithBook(), book(), attribution)
	ad := New(before, after, defaults(questioner.Defaults{Initial: true}))
	changes, err := ad.detectChanges(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	changes, err = ad.ArrangeForGraph(changes, graph.New(), "")
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, "otherapp[0] name", changes["otherapp"][0].Name, "0001_initial")
	assertMigrationDependencies(t, changes, "otherapp", 0, nil)
	assertEqual(t, "otherapp[1] name", changes["otherapp"][1].Name, "0002_initial")
	assertMigrationDependencies(t, changes, "otherapp", 1,
		deps(key("testapp", "0001_initial"), key("otherapp", "0001_initial")))
	assertEqual(t, "testapp[0] name", changes["testapp"][0].Name, "0001_initial")
	assertMigrationDependencies(t, changes, "testapp", 0, deps(key("otherapp", "0001_initial")))
}

// django: tests/migrations/test_autodetector.py AutodetectorTests.test_trim_apps
func TestAutodetector_TrimApps(t *testing.T) {
	before := projectState()
	after := projectState(authorEmpty(), otherPony(), otherStable(), thirdThing())
	ad := New(before, after, defaults(questioner.Defaults{Initial: true}))
	changes, err := ad.detectChanges(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	changes, err = ad.ArrangeForGraph(changes, graph.New(), "")
	if err != nil {
		t.Fatal(err)
	}
	changes["testapp"][0].Dependencies = append(changes["testapp"][0].Dependencies,
		key("otherapp", "0001_initial"))
	changes = trimChangesToApps(changes, []string{"testapp"})
	assertEqual(t, "testapp name", changes["testapp"][0].Name, "0001_initial")
	assertEqual(t, "otherapp name", changes["otherapp"][0].Name, "0001_initial")
	if _, ok := changes["thirdapp"]; ok {
		t.Fatalf("thirdapp should have been trimmed:\n%s", reprChanges(changes, true))
	}
}

// django: tests/migrations/test_autodetector.py AutodetectorTests.test_custom_migration_name
func TestAutodetector_CustomMigrationName(t *testing.T) {
	g := graph.New()
	g.AddNode(key("testapp", "0001_initial"), nil)
	g.AddNode(key("testapp", "0002_foobar"), nil)
	g.AddNode(key("otherapp", "0001_initial"), nil)
	mustDep(t, g, "testapp.0002_foobar", key("testapp", "0002_foobar"), key("testapp", "0001_initial"))

	before := projectState()
	after := projectState(authorEmpty(), otherPony(), otherStable())
	ad := New(before, after, defaults(questioner.Defaults{}))
	changes, err := ad.detectChanges(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	changes, err = ad.ArrangeForGraph(changes, g, "custom_name")
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, "testapp name", changes["testapp"][0].Name, "0003_custom_name")
	assertMigrationDependencies(t, changes, "testapp", 0, deps(key("testapp", "0002_foobar")))
	assertEqual(t, "otherapp name", changes["otherapp"][0].Name, "0002_custom_name")
	assertMigrationDependencies(t, changes, "otherapp", 0, deps(key("otherapp", "0001_initial")))
}

// django: tests/migrations/test_autodetector.py AutodetectorTests.test_parse_number
func TestAutodetector_ParseNumber(t *testing.T) {
	cases := []struct {
		name string
		want int
		ok   bool
	}{
		{"no_number", 0, false},
		{"0001_initial", 1, true},
		{"0002_model3", 2, true},
		{"0002_auto_20380101_1112", 2, true},
		{"0002_squashed_0003", 3, true},
		{"0002_model2_squashed_0003_other4", 3, true},
		{"0002_squashed_0003_squashed_0004", 4, true},
		{"0002_model2_squashed_0003_other4_squashed_0005_other6", 5, true},
		{"0002_custom_name_20380101_1112_squashed_0003_model", 3, true},
		{"2_squashed_4", 4, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := ParseNumber(c.name)
			if ok != c.ok || (ok && got != c.want) {
				t.Fatalf("ParseNumber(%q) = (%d, %v), want (%d, %v)", c.name, got, ok, c.want, c.ok)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// deterministic ordering (gormgate-specific: Go map iteration is randomized)
// ---------------------------------------------------------------------------

// TestAutodetector_DeterministicOrdering runs the same detection repeatedly and
// requires byte-identical results. gormgate's states are Go maps, whose
// iteration order is randomized on every run, so every ordering decision must
// come from an explicit sort.
func TestAutodetector_DeterministicOrdering(t *testing.T) {
	build := func() (before, after []*m.ModelState) {
		before = []*m.ModelState{authorEmpty(), bookUniqueTogether3(), publisher()}
		after = []*m.ModelState{
			authorWithPublisher(), publisherWithAuthor(), bookUniqueTogether4(),
			edition(), otherStable(), thirdThing(),
			withOptions(book(), m.Options{
				Indexes:     []m.Index{index("book_title_author_idx", "author", "title")},
				Constraints: []m.Constraint{uniqueConstraint("book_title_uniq", "title")},
			}),
		}
		return
	}
	var want string
	for i := 0; i < 50; i++ {
		before, after := build()
		ad := New(projectState(before...), projectState(after...), defaults(questioner.Defaults{Rename: true, Initial: true}))
		changes, err := ad.detectChanges(nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		changes, err = ad.ArrangeForGraph(changes, graph.New(), "")
		if err != nil {
			t.Fatal(err)
		}
		got := reprChanges(changes, true)
		if i == 0 {
			want = got
			continue
		}
		if got != want {
			t.Fatalf("run %d differs from run 0:\n--- run 0 ---\n%s\n--- run %d ---\n%s", i, want, i, got)
		}
	}
	if want == "" {
		t.Fatal("fixture bug: no changes detected")
	}
}

// ---------------------------------------------------------------------------
// MigrationSuggestNameTests
// ---------------------------------------------------------------------------

func suggest(t *testing.T, mig *m.Migration) string {
	t.Helper()
	old := m.MigrationNameTimestamp
	m.MigrationNameTimestamp = func() string { return "20380101_1112" }
	defer func() { m.MigrationNameTimestamp = old }()
	return mig.SuggestName()
}

// django: tests/migrations/test_autodetector.py MigrationSuggestNameTests.test_no_operations
func TestMigrationSuggestName_NoOperations(t *testing.T) {
	got := suggest(t, &m.Migration{App: "test_app", Name: "some_migration"})
	if !strings.HasPrefix(got, "auto_") {
		t.Fatalf("suggest_name() = %q, want an auto_ name", got)
	}
}

// django: tests/migrations/test_autodetector.py MigrationSuggestNameTests.test_no_operations_initial
func TestMigrationSuggestName_NoOperationsInitial(t *testing.T) {
	got := suggest(t, &m.Migration{App: "test_app", Name: "some_migration", Initial: m.Ptr(true)})
	assertEqual(t, "suggest_name", got, "initial")
}

// django: tests/migrations/test_autodetector.py MigrationSuggestNameTests.test_single_operation
func TestMigrationSuggestName_SingleOperation(t *testing.T) {
	got := suggest(t, &m.Migration{App: "test_app", Name: "0001_initial",
		Operations: []m.Operation{&m.CreateModel{Name: "Person", Table: "person"}}})
	assertEqual(t, "suggest_name", got, "person")

	got = suggest(t, &m.Migration{App: "test_app", Name: "0002_initial",
		Operations: []m.Operation{&m.DeleteModel{Name: "Person"}}})
	assertEqual(t, "suggest_name", got, "delete_person")
}

// django: tests/migrations/test_autodetector.py MigrationSuggestNameTests.test_single_operation_long_name
func TestMigrationSuggestName_SingleOperationLongName(t *testing.T) {
	name := strings.Repeat("A", 53)
	got := suggest(t, &m.Migration{App: "test_app", Name: "some_migration",
		Operations: []m.Operation{&m.CreateModel{Name: name, Table: "t"}}})
	assertEqual(t, "suggest_name", got, strings.Repeat("a", 53))
}

// django: tests/migrations/test_autodetector.py MigrationSuggestNameTests.test_two_operations
func TestMigrationSuggestName_TwoOperations(t *testing.T) {
	got := suggest(t, &m.Migration{App: "test_app", Name: "some_migration", Operations: []m.Operation{
		&m.CreateModel{Name: "Person", Table: "person"},
		&m.DeleteModel{Name: "Animal"},
	}})
	assertEqual(t, "suggest_name", got, "person_delete_animal")
}

// django: tests/migrations/test_autodetector.py MigrationSuggestNameTests.test_two_create_models
func TestMigrationSuggestName_TwoCreateModels(t *testing.T) {
	got := suggest(t, &m.Migration{App: "test_app", Name: "0001_initial", Operations: []m.Operation{
		&m.CreateModel{Name: "Person", Table: "person"},
		&m.CreateModel{Name: "Animal", Table: "animal"},
	}})
	assertEqual(t, "suggest_name", got, "person_animal")
}

// django: tests/migrations/test_autodetector.py
// MigrationSuggestNameTests.test_two_create_models_with_initial_true
func TestMigrationSuggestName_TwoCreateModelsWithInitialTrue(t *testing.T) {
	got := suggest(t, &m.Migration{App: "test_app", Name: "0001_initial", Initial: m.Ptr(true),
		Operations: []m.Operation{
			&m.CreateModel{Name: "Person", Table: "person"},
			&m.CreateModel{Name: "Animal", Table: "animal"},
		}})
	assertEqual(t, "suggest_name", got, "initial")
}

// django: tests/migrations/test_autodetector.py MigrationSuggestNameTests.test_many_operations_suffix
func TestMigrationSuggestName_ManyOperationsSuffix(t *testing.T) {
	got := suggest(t, &m.Migration{App: "test_app", Name: "some_migration", Operations: []m.Operation{
		&m.CreateModel{Name: "Person1", Table: "p1"},
		&m.CreateModel{Name: "Person2", Table: "p2"},
		&m.CreateModel{Name: "Person3", Table: "p3"},
		&m.DeleteModel{Name: "Person4"},
		&m.DeleteModel{Name: "Person5"},
	}})
	assertEqual(t, "suggest_name", got, "person1_person2_person3_delete_person4_and_more")
}

// django: tests/migrations/test_autodetector.py
// MigrationSuggestNameTests.test_operation_with_no_suggested_name
func TestMigrationSuggestName_OperationWithNoSuggestedName(t *testing.T) {
	got := suggest(t, &m.Migration{App: "test_app", Name: "some_migration", Operations: []m.Operation{
		&m.CreateModel{Name: "Person", Table: "person"},
		&m.RunSQL{SQL: m.Script("SELECT 1 FROM person;")},
	}})
	if !strings.HasPrefix(got, "auto_") {
		t.Fatalf("suggest_name() = %q, want an auto_ name", got)
	}
}

// django: tests/migrations/test_autodetector.py
// MigrationSuggestNameTests.test_operation_with_invalid_chars_in_suggested_name
func TestMigrationSuggestName_OperationWithInvalidCharsInSuggestedName(t *testing.T) {
	got := suggest(t, &m.Migration{App: "test_app", Name: "some_migration", Operations: []m.Operation{
		&m.AddConstraint{ModelName: "Person",
			Constraint: uniqueConstraint("person.name-*~unique!", "name")},
	}})
	assertEqual(t, "suggest_name", got, "person_person_name_unique_")
}

// django: tests/migrations/test_autodetector.py MigrationSuggestNameTests.test_none_name
func TestMigrationSuggestName_NoneName(t *testing.T) {
	got := suggest(t, &m.Migration{App: "test_app", Name: "0001_initial",
		Operations: []m.Operation{&m.RunSQL{SQL: m.Script("SELECT 1 FROM person;")}}})
	if !strings.HasPrefix(got, "auto_") {
		t.Fatalf("suggest_name() = %q, want an auto_ name", got)
	}
}

// django: tests/migrations/test_autodetector.py
// MigrationSuggestNameTests.test_none_name_with_initial_true
func TestMigrationSuggestName_NoneNameWithInitialTrue(t *testing.T) {
	got := suggest(t, &m.Migration{App: "test_app", Name: "0001_initial", Initial: m.Ptr(true),
		Operations: []m.Operation{&m.RunSQL{SQL: m.Script("SELECT 1 FROM person;")}}})
	assertEqual(t, "suggest_name", got, "initial")
}

// django: tests/migrations/test_autodetector.py MigrationSuggestNameTests.test_auto
func TestMigrationSuggestName_Auto(t *testing.T) {
	got := suggest(t, &m.Migration{App: "test_app", Name: "0001_initial"})
	if !strings.HasPrefix(got, "auto_") {
		t.Fatalf("suggest_name() = %q, want an auto_ name", got)
	}
}
