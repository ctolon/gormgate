package autodetector

// Port of Django's tests/migrations/test_autodetector.py (AutodetectorTests).
// Every test keeps the Django name; the `// django:` comment names the
// original. Tests whose concepts gormgate deliberately lacks (managers, proxy
// models, MTI/bases, order_with_respect_to, swappable/AUTH_USER_MODEL,
// index_together, contenttypes, generated fields, composite primary keys,
// Python deconstructible objects and validators) are listed in docs/not-applicable.md.

import (
	"testing"

	"github.com/ctolon/gormgate/internal/questioner"
	m "github.com/ctolon/gormgate/migrations"
)

// ---------------------------------------------------------------------------
// shared fixtures (django: AutodetectorTests class attributes)
// ---------------------------------------------------------------------------

func authorEmpty() *m.ModelState {
	return model("testapp", "Author", field("id", autoField()))
}

func authorName() *m.ModelState {
	return model("testapp", "Author", field("id", autoField()), field("name", charField(200)))
}

func authorNameNull() *m.ModelState {
	return model("testapp", "Author", field("id", autoField()), field("name", null(charField(200))))
}

func authorNameLonger() *m.ModelState {
	return model("testapp", "Author", field("id", autoField()), field("name", charField(400)))
}

func authorNameRenamed() *m.ModelState {
	return model("testapp", "Author", field("id", autoField()), field("names", charField(200)))
}

func authorNameDefault() *m.ModelState {
	return model("testapp", "Author", field("id", autoField()),
		field("name", withDefault(charField(200), "Ada Lovelace")))
}

func authorNameDBDefault() *m.ModelState {
	return model("testapp", "Author", field("id", autoField()),
		field("name", withDBDefault(charField(200), "Ada Lovelace")))
}

func authorNameCheckConstraint() *m.ModelState {
	return withOptions(authorName(), m.Options{
		Constraints: []m.Constraint{checkConstraint("name_contains_bob", "name LIKE '%Bob%'")},
	})
}

func authorWithBiography() *m.ModelState {
	return model("testapp", "Author", field("id", autoField()),
		field("name", charField(200)), field("biography", textField()))
}

func authorWithBook() *m.ModelState {
	return model("testapp", "Author", field("id", autoField()),
		field("name", charField(200)), field("book", fk("otherapp.Book")))
}

func authorWithPublisherString() *m.ModelState {
	return model("testapp", "Author", field("id", autoField()),
		field("name", charField(200)), field("publisher_name", charField(200)))
}

func authorWithPublisher() *m.ModelState {
	return model("testapp", "Author", field("id", autoField()),
		field("name", charField(200)), field("publisher", fk("testapp.Publisher")))
}

func publisher() *m.ModelState {
	return model("testapp", "Publisher", field("id", autoField()), field("name", charField(100)))
}

func publisherWithAuthor() *m.ModelState {
	return model("testapp", "Publisher", field("id", autoField()),
		field("author", fk("testapp.Author")), field("name", charField(100)))
}

func publisherWithAardvarkAuthor() *m.ModelState {
	return model("testapp", "Publisher", field("id", autoField()),
		field("author", fk("testapp.Aardvark")), field("name", charField(100)))
}

func publisherWithBook() *m.ModelState {
	return model("testapp", "Publisher", field("id", autoField()),
		field("author", fk("otherapp.Book")), field("name", charField(100)))
}

func otherPony() *m.ModelState {
	return model("otherapp", "Pony", field("id", autoField()))
}

func otherStable() *m.ModelState {
	return model("otherapp", "Stable", field("id", autoField()))
}

func thirdThing() *m.ModelState {
	return model("thirdapp", "Thing", field("id", autoField()))
}

func book() *m.ModelState {
	return model("otherapp", "Book", field("id", autoField()),
		field("author", fk("testapp.Author")), field("title", charField(200)))
}

func bookWithNoAuthorFK() *m.ModelState {
	return model("otherapp", "Book", field("id", autoField()),
		field("author", intField()), field("title", charField(200)))
}

func bookWithNoAuthor() *m.ModelState {
	return model("otherapp", "Book", field("id", autoField()), field("title", charField(200)))
}

func bookIndexes() *m.ModelState {
	return withOptions(book(), m.Options{
		Indexes: []m.Index{index("book_title_author_idx", "author", "title")},
	})
}

func bookUnorderedIndexes() *m.ModelState {
	return withOptions(book(), m.Options{
		Indexes: []m.Index{index("book_author_title_idx", "title", "author")},
	})
}

func bookUniqueTogether() *m.ModelState {
	return withOptions(book(), m.Options{UniqueTogether: [][]string{{"author", "title"}}})
}

func bookUniqueTogether2() *m.ModelState {
	return withOptions(book(), m.Options{UniqueTogether: [][]string{{"title", "author"}}})
}

func bookUniqueTogether3() *m.ModelState {
	ms := model("otherapp", "Book", field("id", autoField()), field("newfield", intField()),
		field("author", fk("testapp.Author")), field("title", charField(200)))
	return withOptions(ms, m.Options{UniqueTogether: [][]string{{"title", "newfield"}}})
}

func bookUniqueTogether4() *m.ModelState {
	ms := model("otherapp", "Book", field("id", autoField()), field("newfield2", intField()),
		field("author", fk("testapp.Author")), field("title", charField(200)))
	return withOptions(ms, m.Options{UniqueTogether: [][]string{{"title", "newfield2"}}})
}

func edition() *m.ModelState {
	return model("thirdapp", "Edition", field("id", autoField()), field("book", fk("otherapp.Book")))
}

func aardvarkTestapp() *m.ModelState {
	return model("testapp", "Aardvark", field("id", autoField()))
}

func aardvarkPKFKAuthor() *m.ModelState {
	return model("testapp", "Aardvark", field("id", pkOf(fk("testapp.Author"))))
}

func knight() *m.ModelState {
	return model("eggs", "Knight", field("id", autoField()))
}

func rabbit() *m.ModelState {
	ms := model("eggs", "Rabbit", field("id", autoField()),
		field("knight", fk("eggs.Knight")), field("parent", fk("eggs.Rabbit")))
	return withOptions(ms, m.Options{
		UniqueTogether: [][]string{{"parent", "knight"}},
		Indexes:        []m.Index{index("rabbit_circular_fk_index", "parent", "knight")},
	})
}

// ---------------------------------------------------------------------------
// models
// ---------------------------------------------------------------------------

// django: tests/migrations/test_autodetector.py AutodetectorTests.test_new_model
//
// Django also asserts the model's managers; gormgate has no managers, so the
// test keeps only the model creation.
func TestAutodetector_NewModel(t *testing.T) {
	changes := getChanges(t, nil, []*m.ModelState{otherPony()}, nil)
	assertNumberMigrations(t, changes, "otherapp", 1)
	assertOperationTypes(t, changes, "otherapp", 0, "CreateModel")
	op := operationAt[*m.CreateModel](t, changes, "otherapp", 0, 0)
	assertEqual(t, "name", op.Name, "Pony")
	// gormgate deviation: the table is always explicit in CreateModel.
	assertEqual(t, "table", op.Table, "otherapp_pony")
}

// django: tests/migrations/test_autodetector.py AutodetectorTests.test_old_model
func TestAutodetector_OldModel(t *testing.T) {
	changes := getChanges(t, []*m.ModelState{authorEmpty()}, nil, nil)
	assertNumberMigrations(t, changes, "testapp", 1)
	assertOperationTypes(t, changes, "testapp", 0, "DeleteModel")
	assertEqual(t, "name", operationAt[*m.DeleteModel](t, changes, "testapp", 0, 0).Name, "Author")
}

// ---------------------------------------------------------------------------
// fields
// ---------------------------------------------------------------------------

// django: tests/migrations/test_autodetector.py AutodetectorTests.test_add_field
func TestAutodetector_AddField(t *testing.T) {
	changes := getChanges(t, []*m.ModelState{authorEmpty()}, []*m.ModelState{authorName()},
		&recordingQuestioner{t: t})
	assertNumberMigrations(t, changes, "testapp", 1)
	assertOperationTypes(t, changes, "testapp", 0, "AddField")
	assertEqual(t, "name", operationAt[*m.AddField](t, changes, "testapp", 0, 0).Name, "name")
}

// django: tests/migrations/test_autodetector.py AutodetectorTests.test_add_not_null_field_with_db_default
func TestAutodetector_AddNotNullFieldWithDBDefault(t *testing.T) {
	q := &recordingQuestioner{t: t, forbidNotNullAddition: true}
	changes := getChanges(t, []*m.ModelState{authorEmpty()}, []*m.ModelState{authorNameDBDefault()}, q)
	assertNumberMigrations(t, changes, "testapp", 1)
	assertOperationTypes(t, changes, "testapp", 0, "AddField")
	op := operationAt[*m.AddField](t, changes, "testapp", 0, 0)
	assertEqual(t, "name", op.Name, "name")
	if op.PreserveDefault != nil {
		t.Fatalf("preserve_default: got %v, want the default (true)", *op.PreserveDefault)
	}
	assertEqual(t, "db_default", op.Field.DBDefault.String(), "Ada Lovelace")
}

// django: tests/migrations/test_autodetector.py
// AutodetectorTests.test_add_date_fields_with_auto_now_not_asking_for_default
//
// Django uses DateField/DateTimeField/TimeField; gormgate has a single Time
// type, so the three fields differ only in name.
func TestAutodetector_AddDateFieldsWithAutoNowNotAskingForDefault(t *testing.T) {
	q := &recordingQuestioner{t: t, forbidNotNullAddition: true}
	after := model("testapp", "Author", field("id", autoField()),
		field("date_of_birth", autoNow(dateTimeField())),
		field("date_time_of_birth", autoNow(dateTimeField())),
		field("time_of_birth", autoNow(dateTimeField())))
	changes := getChanges(t, []*m.ModelState{authorEmpty()}, []*m.ModelState{after}, q)
	assertNumberMigrations(t, changes, "testapp", 1)
	assertOperationTypes(t, changes, "testapp", 0, "AddField", "AddField", "AddField")
	for i := 0; i < 3; i++ {
		op := operationAt[*m.AddField](t, changes, "testapp", 0, i)
		if !op.Field.AutoNow {
			t.Fatalf("op #%d: auto_now not set", i)
		}
	}
}

// django: tests/migrations/test_autodetector.py
// AutodetectorTests.test_add_date_fields_with_auto_now_add_not_asking_for_null_addition
func TestAutodetector_AddDateFieldsWithAutoNowAddNotAskingForNullAddition(t *testing.T) {
	q := &recordingQuestioner{t: t, forbidNotNullAddition: true}
	after := model("testapp", "Author", field("id", autoField()),
		field("date_of_birth", autoNowAd(dateTimeField())),
		field("date_time_of_birth", autoNowAd(dateTimeField())),
		field("time_of_birth", autoNowAd(dateTimeField())))
	changes := getChanges(t, []*m.ModelState{authorEmpty()}, []*m.ModelState{after}, q)
	assertNumberMigrations(t, changes, "testapp", 1)
	assertOperationTypes(t, changes, "testapp", 0, "AddField", "AddField", "AddField")
	for i := 0; i < 3; i++ {
		op := operationAt[*m.AddField](t, changes, "testapp", 0, i)
		if !op.Field.AutoNowAdd {
			t.Fatalf("op #%d: auto_now_add not set", i)
		}
	}
}

// django: tests/migrations/test_autodetector.py
// AutodetectorTests.test_add_date_fields_with_auto_now_add_asking_for_default
func TestAutodetector_AddDateFieldsWithAutoNowAddAskingForDefault(t *testing.T) {
	q := &recordingQuestioner{t: t}
	after := model("testapp", "Author", field("id", autoField()),
		field("date_of_birth", autoNowAd(dateTimeField())),
		field("date_time_of_birth", autoNowAd(dateTimeField())),
		field("time_of_birth", autoNowAd(dateTimeField())))
	changes := getChanges(t, []*m.ModelState{authorEmpty()}, []*m.ModelState{after}, q)
	assertNumberMigrations(t, changes, "testapp", 1)
	assertOperationTypes(t, changes, "testapp", 0, "AddField", "AddField", "AddField")
	for i := 0; i < 3; i++ {
		if !operationAt[*m.AddField](t, changes, "testapp", 0, i).Field.AutoNowAdd {
			t.Fatalf("op #%d: auto_now_add not set", i)
		}
	}
	assertEqual(t, "ask_auto_now_add_addition call count", q.autoNowAddCalls, 3)
}

// django: tests/migrations/test_autodetector.py AutodetectorTests.test_remove_field
func TestAutodetector_RemoveField(t *testing.T) {
	changes := getChanges(t, []*m.ModelState{authorName()}, []*m.ModelState{authorEmpty()}, nil)
	assertNumberMigrations(t, changes, "testapp", 1)
	assertOperationTypes(t, changes, "testapp", 0, "RemoveField")
	assertEqual(t, "name", operationAt[*m.RemoveField](t, changes, "testapp", 0, 0).Name, "name")
}

// django: tests/migrations/test_autodetector.py AutodetectorTests.test_alter_field
func TestAutodetector_AlterField(t *testing.T) {
	changes := getChanges(t, []*m.ModelState{authorName()}, []*m.ModelState{authorNameLonger()}, nil)
	assertNumberMigrations(t, changes, "testapp", 1)
	assertOperationTypes(t, changes, "testapp", 0, "AlterField")
	op := operationAt[*m.AlterField](t, changes, "testapp", 0, 0)
	assertEqual(t, "name", op.Name, "name")
	if op.PreserveDefault != nil {
		t.Fatalf("preserve_default: got %v, want the default (true)", *op.PreserveDefault)
	}
}

// django: tests/migrations/test_autodetector.py AutodetectorTests.test_alter_field_to_not_null_with_default
func TestAutodetector_AlterFieldToNotNullWithDefault(t *testing.T) {
	q := &recordingQuestioner{t: t, forbidNotNullAlteration: true}
	changes := getChanges(t, []*m.ModelState{authorNameNull()}, []*m.ModelState{authorNameDefault()}, q)
	assertNumberMigrations(t, changes, "testapp", 1)
	assertOperationTypes(t, changes, "testapp", 0, "AlterField")
	op := operationAt[*m.AlterField](t, changes, "testapp", 0, 0)
	assertEqual(t, "name", op.Name, "name")
	if op.PreserveDefault != nil {
		t.Fatalf("preserve_default: got %v, want the default (true)", *op.PreserveDefault)
	}
	assertEqual(t, "default", op.Field.Default, any("Ada Lovelace"))
}

// django: tests/migrations/test_autodetector.py AutodetectorTests.test_alter_field_to_not_null_with_db_default
func TestAutodetector_AlterFieldToNotNullWithDBDefault(t *testing.T) {
	q := &recordingQuestioner{t: t, forbidNotNullAlteration: true}
	changes := getChanges(t, []*m.ModelState{authorNameNull()}, []*m.ModelState{authorNameDBDefault()}, q)
	assertNumberMigrations(t, changes, "testapp", 1)
	assertOperationTypes(t, changes, "testapp", 0, "AlterField")
	op := operationAt[*m.AlterField](t, changes, "testapp", 0, 0)
	assertEqual(t, "name", op.Name, "name")
	if op.PreserveDefault != nil {
		t.Fatalf("preserve_default: got %v, want the default (true)", *op.PreserveDefault)
	}
	assertEqual(t, "db_default", op.Field.DBDefault.String(), "Ada Lovelace")
}

// django: tests/migrations/test_autodetector.py AutodetectorTests.test_add_auto_field_does_not_request_default
func TestAutodetector_AddAutoFieldDoesNotRequestDefault(t *testing.T) {
	before := model("testapp", "Author", field("pkfield", pkOf(intField())))
	for name, auto := range map[string]m.Field{
		"AutoField":      autoField(),
		"BigAutoField":   bigAutoField(),
		"SmallAutoField": smallAutoField(),
	} {
		t.Run(name, func(t *testing.T) {
			q := &recordingQuestioner{t: t, forbidNotNullAddition: true}
			pk := intField()
			pk.PrimaryKey = false
			after := model("testapp", "Author", field("id", auto), field("pkfield", pk))
			getChanges(t, []*m.ModelState{before}, []*m.ModelState{after}, q)
			assertEqual(t, "ask_not_null_addition call count", q.notNullAdditionCalls, 0)
		})
	}
}

// django: tests/migrations/test_autodetector.py AutodetectorTests.test_alter_field_to_not_null_without_default
func TestAutodetector_AlterFieldToNotNullWithoutDefault(t *testing.T) {
	q := &recordingQuestioner{t: t, notNullAlterationValue: questioner.NotProvided}
	changes := getChanges(t, []*m.ModelState{authorNameNull()}, []*m.ModelState{authorName()}, q)
	assertEqual(t, "ask_not_null_alteration call count", q.notNullAlterationCalls, 1)
	assertNumberMigrations(t, changes, "testapp", 1)
	assertOperationTypes(t, changes, "testapp", 0, "AlterField")
	op := operationAt[*m.AlterField](t, changes, "testapp", 0, 0)
	assertEqual(t, "name", op.Name, "name")
	if op.PreserveDefault != nil {
		t.Fatalf("preserve_default: got %v, want the default (true)", *op.PreserveDefault)
	}
	// Django keeps models.NOT_PROVIDED as the field's default, i.e. the field
	// is left without one.
	if op.Field.Default != nil {
		t.Fatalf("default: got %v, want none", op.Field.Default)
	}
}

// django: tests/migrations/test_autodetector.py AutodetectorTests.test_alter_field_to_not_null_oneoff_default
func TestAutodetector_AlterFieldToNotNullOneoffDefault(t *testing.T) {
	oneOff := &m.GoExpr{Source: `"Some Name"`}
	q := &recordingQuestioner{t: t, notNullAlterationValue: oneOff}
	changes := getChanges(t, []*m.ModelState{authorNameNull()}, []*m.ModelState{authorName()}, q)
	assertEqual(t, "ask_not_null_alteration call count", q.notNullAlterationCalls, 1)
	assertNumberMigrations(t, changes, "testapp", 1)
	assertOperationTypes(t, changes, "testapp", 0, "AlterField")
	op := operationAt[*m.AlterField](t, changes, "testapp", 0, 0)
	assertEqual(t, "name", op.Name, "name")
	if op.PreserveDefault == nil || *op.PreserveDefault {
		t.Fatalf("preserve_default: got %v, want false", op.PreserveDefault)
	}
	assertEqual(t, "default", op.Field.Default, any(oneOff))
}

// django: tests/migrations/test_autodetector.py AutodetectorTests.test_add_field_with_default
func TestAutodetector_AddFieldWithDefault(t *testing.T) {
	q := &recordingQuestioner{t: t, forbidNotNullAddition: true}
	changes := getChanges(t, []*m.ModelState{authorEmpty()}, []*m.ModelState{authorNameDefault()}, q)
	assertNumberMigrations(t, changes, "testapp", 1)
	assertOperationTypes(t, changes, "testapp", 0, "AddField")
	assertEqual(t, "name", operationAt[*m.AddField](t, changes, "testapp", 0, 0).Name, "name")
}

// django: tests/migrations/test_autodetector.py AutodetectorTests.test_add_non_blank_textfield_and_charfield
func TestAutodetector_AddNonBlankTextfieldAndCharfield(t *testing.T) {
	q := &recordingQuestioner{t: t}
	changes := getChanges(t, []*m.ModelState{authorEmpty()}, []*m.ModelState{authorWithBiography()}, q)
	assertEqual(t, "ask_not_null_addition call count", q.notNullAdditionCalls, 2)
	assertNumberMigrations(t, changes, "testapp", 1)
	assertOperationTypes(t, changes, "testapp", 0, "AddField", "AddField")
}

// django: tests/migrations/test_autodetector.py AutodetectorTests.test_custom_deconstructible
//
// Django compares two instances of a deconstructible object; the Go analogue
// is two distinct *DBDefault pointers holding the same value, which must
// compare equal and produce no change.
func TestAutodetector_CustomDeconstructible(t *testing.T) {
	before := authorNameDBDefault()
	after := authorNameDBDefault()
	if before.Fields[1].Field.DBDefault == after.Fields[1].Field.DBDefault {
		t.Fatal("fixture bug: the two defaults must be distinct pointers")
	}
	changes := getChanges(t, []*m.ModelState{before}, []*m.ModelState{after}, nil)
	assertEqual(t, "number of apps with changes", len(changes), 0)
}

// django: tests/migrations/test_autodetector.py AutodetectorTests.test_deconstruct_type
//
// Django passes an uninstantiated field class as a default; the Go analogue is
// a function value used as a callable default, which deconstructs by name.
func TestAutodetector_DeconstructType(t *testing.T) {
	author := model("testapp", "Author", field("id", autoField()),
		field("name", withDefault(charField(200), defaultName)))
	changes := getChanges(t, nil, []*m.ModelState{author}, nil)
	assertNumberMigrations(t, changes, "testapp", 1)
	assertOperationTypes(t, changes, "testapp", 0, "CreateModel")

	// The same function on both sides is not a change.
	changes = getChanges(t, []*m.ModelState{author}, []*m.ModelState{author.Clone()}, nil)
	assertEqual(t, "number of apps with changes", len(changes), 0)
}

func defaultName() string { return "" }

// django: tests/migrations/test_autodetector.py AutodetectorTests.test_replace_string_with_foreignkey
func TestAutodetector_ReplaceStringWithForeignkey(t *testing.T) {
	changes := getChanges(t,
		[]*m.ModelState{authorWithPublisherString()},
		[]*m.ModelState{authorWithPublisher(), publisher()}, nil)
	assertNumberMigrations(t, changes, "testapp", 1)
	assertOperationTypes(t, changes, "testapp", 0, "CreateModel", "RemoveField", "AddField")
	assertEqual(t, "op0 name", operationAt[*m.CreateModel](t, changes, "testapp", 0, 0).Name, "Publisher")
	assertEqual(t, "op1 name", operationAt[*m.RemoveField](t, changes, "testapp", 0, 1).Name, "publisher_name")
	assertEqual(t, "op2 name", operationAt[*m.AddField](t, changes, "testapp", 0, 2).Name, "publisher")
}

// django: tests/migrations/test_autodetector.py AutodetectorTests.test_foreign_key_removed_before_target_model
func TestAutodetector_ForeignKeyRemovedBeforeTargetModel(t *testing.T) {
	changes := getChanges(t,
		[]*m.ModelState{authorWithPublisher(), publisher()},
		[]*m.ModelState{authorName()}, nil)
	assertNumberMigrations(t, changes, "testapp", 1)
	assertOperationTypes(t, changes, "testapp", 0, "RemoveField", "DeleteModel")
	assertEqual(t, "op0 name", operationAt[*m.RemoveField](t, changes, "testapp", 0, 0).Name, "publisher")
	assertEqual(t, "op1 name", operationAt[*m.DeleteModel](t, changes, "testapp", 0, 1).Name, "Publisher")
}

// django: tests/migrations/test_autodetector.py
// AutodetectorTests.test_non_circular_foreignkey_dependency_removal
func TestAutodetector_NonCircularForeignkeyDependencyRemoval(t *testing.T) {
	changes := getChanges(t,
		[]*m.ModelState{authorWithPublisher(), publisherWithAuthor()}, nil, nil)
	assertNumberMigrations(t, changes, "testapp", 1)
	assertOperationTypes(t, changes, "testapp", 0, "RemoveField", "DeleteModel", "DeleteModel")
	op0 := operationAt[*m.RemoveField](t, changes, "testapp", 0, 0)
	assertEqual(t, "op0 name", op0.Name, "author")
	assertEqual(t, "op0 model_name", op0.ModelName, "publisher")
	assertEqual(t, "op1 name", operationAt[*m.DeleteModel](t, changes, "testapp", 0, 1).Name, "Author")
	assertEqual(t, "op2 name", operationAt[*m.DeleteModel](t, changes, "testapp", 0, 2).Name, "Publisher")
}

// django: tests/migrations/test_autodetector.py AutodetectorTests.test_alter_fk_before_model_deletion
func TestAutodetector_AlterFkBeforeModelDeletion(t *testing.T) {
	changes := getChanges(t,
		[]*m.ModelState{authorName(), publisherWithAuthor()},
		[]*m.ModelState{aardvarkTestapp(), publisherWithAardvarkAuthor()}, nil)
	assertNumberMigrations(t, changes, "testapp", 1)
	assertOperationTypes(t, changes, "testapp", 0, "CreateModel", "AlterField", "DeleteModel")
	assertEqual(t, "op0 name", operationAt[*m.CreateModel](t, changes, "testapp", 0, 0).Name, "Aardvark")
	assertEqual(t, "op1 name", operationAt[*m.AlterField](t, changes, "testapp", 0, 1).Name, "author")
	assertEqual(t, "op2 name", operationAt[*m.DeleteModel](t, changes, "testapp", 0, 2).Name, "Author")
}

// ---------------------------------------------------------------------------
// renames
// ---------------------------------------------------------------------------

// django: tests/migrations/test_autodetector.py AutodetectorTests.test_rename_field
func TestAutodetector_RenameField(t *testing.T) {
	changes := getChanges(t, []*m.ModelState{authorName()}, []*m.ModelState{authorNameRenamed()},
		defaults(questioner.Defaults{Rename: true}))
	assertNumberMigrations(t, changes, "testapp", 1)
	assertOperationTypes(t, changes, "testapp", 0, "RenameField")
	op := operationAt[*m.RenameField](t, changes, "testapp", 0, 0)
	assertEqual(t, "old_name", op.OldName, "name")
	assertEqual(t, "new_name", op.NewName, "names")
}

// django: tests/migrations/test_autodetector.py AutodetectorTests.test_rename_field_foreign_key_to_field
func TestAutodetector_RenameFieldForeignKeyToField(t *testing.T) {
	before := []*m.ModelState{
		model("app", "Foo", field("id", autoField()), field("field", unique(intField()))),
		model("app", "Bar", field("id", autoField()), field("foo", fkToField("app.Foo", "field"))),
	}
	after := []*m.ModelState{
		model("app", "Foo", field("id", autoField()), field("renamed_field", unique(intField()))),
		model("app", "Bar", field("id", autoField()), field("foo", fkToField("app.Foo", "renamed_field"))),
	}
	changes := getChanges(t, before, after, defaults(questioner.Defaults{Rename: true}))
	assertNumberMigrations(t, changes, "app", 1)
	assertOperationTypes(t, changes, "app", 0, "RenameField")
	op := operationAt[*m.RenameField](t, changes, "app", 0, 0)
	assertEqual(t, "old_name", op.OldName, "field")
	assertEqual(t, "new_name", op.NewName, "renamed_field")
}

// django: tests/migrations/test_autodetector.py AutodetectorTests.test_rename_referenced_primary_key
func TestAutodetector_RenameReferencedPrimaryKey(t *testing.T) {
	before := []*m.ModelState{
		model("app", "Foo", field("id", pkOf(charField(100)))),
		model("app", "Bar", field("id", autoField()), field("foo", fk("app.Foo"))),
	}
	after := []*m.ModelState{
		model("app", "Foo", field("renamed_id", pkOf(charField(100)))),
		model("app", "Bar", field("id", autoField()), field("foo", fk("app.Foo"))),
	}
	changes := getChanges(t, before, after, defaults(questioner.Defaults{Rename: true}))
	assertNumberMigrations(t, changes, "app", 1)
	assertOperationTypes(t, changes, "app", 0, "RenameField")
	op := operationAt[*m.RenameField](t, changes, "app", 0, 0)
	assertEqual(t, "old_name", op.OldName, "id")
	assertEqual(t, "new_name", op.NewName, "renamed_id")
}

// django: tests/migrations/test_autodetector.py
// AutodetectorTests.test_rename_field_without_db_column_recreate_constraint
//
// gormgate has no db_column, so every field rename takes this branch.
func TestAutodetector_RenameFieldWithoutDbColumnRecreateConstraint(t *testing.T) {
	before := []*m.ModelState{withOptions(
		model("app", "Foo", field("id", autoField()), field("field", intField())),
		m.Options{Constraints: []m.Constraint{uniqueConstraint("unique_field", "field")}})}
	after := []*m.ModelState{withOptions(
		model("app", "Foo", field("id", autoField()), field("full_field1_name", intField())),
		m.Options{Constraints: []m.Constraint{uniqueConstraint("unique_field", "full_field1_name")}})}
	changes := getChanges(t, before, after, defaults(questioner.Defaults{Rename: true}))
	assertNumberMigrations(t, changes, "app", 1)
	assertOperationTypes(t, changes, "app", 0, "RemoveConstraint", "RenameField", "AddConstraint")
}

// django: tests/migrations/test_autodetector.py AutodetectorTests.test_rename_field_with_renamed_model
//
// gormgate deviation: RenameModel does not rename the table, so the model
// rename is followed by an AlterModelTable for the table that follows the new
// name. Django folds the table rename into RenameModel.
func TestAutodetector_RenameFieldWithRenamedModel(t *testing.T) {
	after := model("testapp", "RenamedAuthor", field("id", autoField()),
		field("renamed_name", charField(200)))
	changes := getChanges(t, []*m.ModelState{authorName()}, []*m.ModelState{after},
		defaults(questioner.Defaults{Rename: true, RenameModel: true}))
	assertNumberMigrations(t, changes, "testapp", 1)
	assertOperationTypes(t, changes, "testapp", 0, "RenameModel", "RenameField", "AlterModelTable")
	rm := operationAt[*m.RenameModel](t, changes, "testapp", 0, 0)
	assertEqual(t, "old_name", rm.OldName, "Author")
	assertEqual(t, "new_name", rm.NewName, "RenamedAuthor")
	rf := operationAt[*m.RenameField](t, changes, "testapp", 0, 1)
	assertEqual(t, "old_name", rf.OldName, "name")
	assertEqual(t, "new_name", rf.NewName, "renamed_name")
	at := operationAt[*m.AlterModelTable](t, changes, "testapp", 0, 2)
	assertEqual(t, "table", at.Table, "testapp_renamedauthor")
}

// django: tests/migrations/test_autodetector.py AutodetectorTests.test_rename_model
//
// The fixtures pin the table so that only the rename is detected, which is the
// situation Django's test describes. The related field in otherapp must not
// produce an AlterField, exactly as in Django.
func TestAutodetector_RenameModel(t *testing.T) {
	authorRenamed := withTable(renamedTo(authorWithBook(), "Writer"), tableFor("testapp", "Author"))
	bookWithAuthorRenamed := model("otherapp", "Book", field("id", autoField()),
		field("author", fk("testapp.Writer")), field("title", charField(200)))
	changes := getChanges(t,
		[]*m.ModelState{authorWithBook(), book()},
		[]*m.ModelState{authorRenamed, bookWithAuthorRenamed},
		defaults(questioner.Defaults{RenameModel: true}))
	assertNumberMigrations(t, changes, "testapp", 1)
	assertOperationTypes(t, changes, "testapp", 0, "RenameModel")
	op := operationAt[*m.RenameModel](t, changes, "testapp", 0, 0)
	assertEqual(t, "old_name", op.OldName, "Author")
	assertEqual(t, "new_name", op.NewName, "Writer")
	// RenameModel handles related fields too: no AlterField in otherapp.
	assertNumberMigrations(t, changes, "otherapp", 0)
}

// django: tests/migrations/test_autodetector.py AutodetectorTests.test_rename_model_case
func TestAutodetector_RenameModelCase(t *testing.T) {
	authorRenamed := &m.ModelState{
		App: "testapp", Name: "author", Table: tableFor("testapp", "Author"),
		Fields: m.Fields{field("id", autoField())},
	}
	changes := getChanges(t,
		[]*m.ModelState{authorEmpty(), book()},
		[]*m.ModelState{authorRenamed, book()},
		defaults(questioner.Defaults{RenameModel: true}))
	assertNumberMigrations(t, changes, "testapp", 0)
	assertNumberMigrations(t, changes, "otherapp", 0)
}

// django: tests/migrations/test_autodetector.py AutodetectorTests.test_rename_model_with_renamed_rel_field
func TestAutodetector_RenameModelWithRenamedRelField(t *testing.T) {
	authorRenamed := withTable(renamedTo(authorWithBook(), "Writer"), tableFor("testapp", "Author"))
	bookRenamedField := model("otherapp", "Book", field("id", autoField()),
		field("writer", fk("testapp.Writer")), field("title", charField(200)))
	changes := getChanges(t,
		[]*m.ModelState{authorWithBook(), book()},
		[]*m.ModelState{authorRenamed, bookRenamedField},
		defaults(questioner.Defaults{Rename: true, RenameModel: true}))
	assertNumberMigrations(t, changes, "testapp", 1)
	assertOperationTypes(t, changes, "testapp", 0, "RenameModel")
	rm := operationAt[*m.RenameModel](t, changes, "testapp", 0, 0)
	assertEqual(t, "old_name", rm.OldName, "Author")
	assertEqual(t, "new_name", rm.NewName, "Writer")

	assertNumberMigrations(t, changes, "otherapp", 1)
	assertOperationTypes(t, changes, "otherapp", 0, "RenameField")
	rf := operationAt[*m.RenameField](t, changes, "otherapp", 0, 0)
	assertEqual(t, "old_name", rf.OldName, "author")
	assertEqual(t, "new_name", rf.NewName, "writer")
}

// django: tests/migrations/test_autodetector.py
// AutodetectorTests.test_rename_model_with_fks_in_different_position
func TestAutodetector_RenameModelWithFksInDifferentPosition(t *testing.T) {
	before := []*m.ModelState{
		model("testapp", "EntityA", field("id", autoField())),
		model("testapp", "EntityB", field("id", autoField()),
			field("some_label", charField(255)), field("entity_a", fk("testapp.EntityA"))),
	}
	after := []*m.ModelState{
		model("testapp", "EntityA", field("id", autoField())),
		withTable(model("testapp", "RenamedEntityB", field("id", autoField()),
			field("entity_a", fk("testapp.EntityA")), field("some_label", charField(255))),
			tableFor("testapp", "EntityB")),
	}
	changes := getChanges(t, before, after, defaults(questioner.Defaults{RenameModel: true}))
	assertNumberMigrations(t, changes, "testapp", 1)
	assertOperationTypes(t, changes, "testapp", 0, "RenameModel")
	op := operationAt[*m.RenameModel](t, changes, "testapp", 0, 0)
	assertEqual(t, "old_name", op.OldName, "EntityB")
	assertEqual(t, "new_name", op.NewName, "RenamedEntityB")
}

// django: tests/migrations/test_autodetector.py
// AutodetectorTests.test_rename_model_reverse_relation_dependencies
func TestAutodetector_RenameModelReverseRelationDependencies(t *testing.T) {
	before := []*m.ModelState{
		model("testapp", "EntityA", field("id", autoField())),
		model("otherapp", "EntityB", field("id", autoField()), field("entity_a", fk("testapp.EntityA"))),
	}
	after := []*m.ModelState{
		withTable(model("testapp", "RenamedEntityA", field("id", autoField())),
			tableFor("testapp", "EntityA")),
		model("otherapp", "EntityB", field("id", autoField()),
			field("entity_a", fk("testapp.RenamedEntityA"))),
	}
	changes := getChanges(t, before, after, defaults(questioner.Defaults{RenameModel: true}))
	assertNumberMigrations(t, changes, "testapp", 1)
	assertMigrationDependencies(t, changes, "testapp", 0, deps(key("otherapp", "__first__")))
	assertOperationTypes(t, changes, "testapp", 0, "RenameModel")
	op := operationAt[*m.RenameModel](t, changes, "testapp", 0, 0)
	assertEqual(t, "old_name", op.OldName, "EntityA")
	assertEqual(t, "new_name", op.NewName, "RenamedEntityA")
}

// ---------------------------------------------------------------------------
// db_table / db_table_comment
// ---------------------------------------------------------------------------

// django: tests/migrations/test_autodetector.py AutodetectorTests.test_alter_db_table_add
//
// gormgate always stores the table explicitly; Django's "no db_table" state is
// the empty table name here.
func TestAutodetector_AlterDbTableAdd(t *testing.T) {
	before := withTable(authorEmpty(), "")
	after := withTable(authorEmpty(), "author_one")
	changes := getChanges(t, []*m.ModelState{before}, []*m.ModelState{after}, nil)
	assertNumberMigrations(t, changes, "testapp", 1)
	assertOperationTypes(t, changes, "testapp", 0, "AlterModelTable")
	op := operationAt[*m.AlterModelTable](t, changes, "testapp", 0, 0)
	assertEqual(t, "name", op.Name, "author")
	assertEqual(t, "table", op.Table, "author_one")
}

// django: tests/migrations/test_autodetector.py AutodetectorTests.test_alter_db_table_change
func TestAutodetector_AlterDbTableChange(t *testing.T) {
	changes := getChanges(t,
		[]*m.ModelState{withTable(authorEmpty(), "author_one")},
		[]*m.ModelState{withTable(authorEmpty(), "author_two")}, nil)
	assertNumberMigrations(t, changes, "testapp", 1)
	assertOperationTypes(t, changes, "testapp", 0, "AlterModelTable")
	op := operationAt[*m.AlterModelTable](t, changes, "testapp", 0, 0)
	assertEqual(t, "name", op.Name, "author")
	assertEqual(t, "table", op.Table, "author_two")
}

// django: tests/migrations/test_autodetector.py AutodetectorTests.test_alter_db_table_remove
func TestAutodetector_AlterDbTableRemove(t *testing.T) {
	changes := getChanges(t,
		[]*m.ModelState{withTable(authorEmpty(), "author_one")},
		[]*m.ModelState{withTable(authorEmpty(), "")}, nil)
	assertNumberMigrations(t, changes, "testapp", 1)
	assertOperationTypes(t, changes, "testapp", 0, "AlterModelTable")
	op := operationAt[*m.AlterModelTable](t, changes, "testapp", 0, 0)
	assertEqual(t, "name", op.Name, "author")
	assertEqual(t, "table", op.Table, "")
}

// django: tests/migrations/test_autodetector.py AutodetectorTests.test_alter_db_table_no_changes
func TestAutodetector_AlterDbTableNoChanges(t *testing.T) {
	changes := getChanges(t,
		[]*m.ModelState{withTable(authorEmpty(), "author_one")},
		[]*m.ModelState{withTable(authorEmpty(), "author_one")}, nil)
	assertEqual(t, "number of apps with changes", len(changes), 0)
}

// django: tests/migrations/test_autodetector.py AutodetectorTests.test_keep_db_table_with_model_change
func TestAutodetector_KeepDbTableWithModelChange(t *testing.T) {
	changes := getChanges(t,
		[]*m.ModelState{withTable(authorEmpty(), "author_one")},
		[]*m.ModelState{withTable(renamedTo(authorEmpty(), "NewAuthor"), "author_one")},
		defaults(questioner.Defaults{RenameModel: true}))
	assertNumberMigrations(t, changes, "testapp", 1)
	assertOperationTypes(t, changes, "testapp", 0, "RenameModel")
	op := operationAt[*m.RenameModel](t, changes, "testapp", 0, 0)
	assertEqual(t, "old_name", op.OldName, "Author")
	assertEqual(t, "new_name", op.NewName, "NewAuthor")
}

// django: tests/migrations/test_autodetector.py AutodetectorTests.test_alter_db_table_with_model_change
func TestAutodetector_AlterDbTableWithModelChange(t *testing.T) {
	changes := getChanges(t,
		[]*m.ModelState{withTable(authorEmpty(), "author_one")},
		[]*m.ModelState{withTable(renamedTo(authorEmpty(), "NewAuthor"), "author_three")},
		defaults(questioner.Defaults{RenameModel: true}))
	assertNumberMigrations(t, changes, "testapp", 1)
	assertOperationTypes(t, changes, "testapp", 0, "RenameModel", "AlterModelTable")
	rm := operationAt[*m.RenameModel](t, changes, "testapp", 0, 0)
	assertEqual(t, "old_name", rm.OldName, "Author")
	assertEqual(t, "new_name", rm.NewName, "NewAuthor")
	at := operationAt[*m.AlterModelTable](t, changes, "testapp", 0, 1)
	assertEqual(t, "name", at.Name, "newauthor")
	assertEqual(t, "table", at.Table, "author_three")
}

// django: tests/migrations/test_autodetector.py AutodetectorTests.test_alter_db_table_comment_add
func TestAutodetector_AlterDbTableCommentAdd(t *testing.T) {
	after := withOptions(authorEmpty(), m.Options{DBTableComment: "Table comment"})
	changes := getChanges(t, []*m.ModelState{authorEmpty()}, []*m.ModelState{after}, nil)
	assertNumberMigrations(t, changes, "testapp", 1)
	assertOperationTypes(t, changes, "testapp", 0, "AlterModelTableComment")
	op := operationAt[*m.AlterModelTableComment](t, changes, "testapp", 0, 0)
	assertEqual(t, "name", op.Name, "author")
	assertEqual(t, "table_comment", op.TableComment, "Table comment")
}

// django: tests/migrations/test_autodetector.py AutodetectorTests.test_alter_db_table_comment_change
func TestAutodetector_AlterDbTableCommentChange(t *testing.T) {
	before := withOptions(authorEmpty(), m.Options{DBTableComment: "Table comment"})
	after := withOptions(authorEmpty(), m.Options{DBTableComment: "New table comment"})
	changes := getChanges(t, []*m.ModelState{before}, []*m.ModelState{after}, nil)
	assertNumberMigrations(t, changes, "testapp", 1)
	assertOperationTypes(t, changes, "testapp", 0, "AlterModelTableComment")
	op := operationAt[*m.AlterModelTableComment](t, changes, "testapp", 0, 0)
	assertEqual(t, "name", op.Name, "author")
	assertEqual(t, "table_comment", op.TableComment, "New table comment")
}

// django: tests/migrations/test_autodetector.py AutodetectorTests.test_alter_db_table_comment_remove
func TestAutodetector_AlterDbTableCommentRemove(t *testing.T) {
	before := withOptions(authorEmpty(), m.Options{DBTableComment: "Table comment"})
	changes := getChanges(t, []*m.ModelState{before}, []*m.ModelState{authorEmpty()}, nil)
	assertNumberMigrations(t, changes, "testapp", 1)
	assertOperationTypes(t, changes, "testapp", 0, "AlterModelTableComment")
	op := operationAt[*m.AlterModelTableComment](t, changes, "testapp", 0, 0)
	assertEqual(t, "name", op.Name, "author")
	assertEqual(t, "table_comment", op.TableComment, "")
}

// django: tests/migrations/test_autodetector.py AutodetectorTests.test_alter_db_table_comment_no_changes
func TestAutodetector_AlterDbTableCommentNoChanges(t *testing.T) {
	before := withOptions(authorEmpty(), m.Options{DBTableComment: "Table comment"})
	changes := getChanges(t, []*m.ModelState{before}, []*m.ModelState{before.Clone()}, nil)
	assertNumberMigrations(t, changes, "testapp", 0)
}

// ---------------------------------------------------------------------------
// managed
// ---------------------------------------------------------------------------

// django: tests/migrations/test_autodetector.py AutodetectorTests.test_unmanaged_create
func TestAutodetector_UnmanagedCreate(t *testing.T) {
	unmanagedModel := withOptions(model("testapp", "AuthorUnmanaged", field("id", autoField())),
		m.Options{Managed: managed(false)})
	changes := getChanges(t, []*m.ModelState{authorEmpty()},
		[]*m.ModelState{authorEmpty(), unmanagedModel}, nil)
	assertNumberMigrations(t, changes, "testapp", 1)
	assertOperationTypes(t, changes, "testapp", 0, "CreateModel")
	op := operationAt[*m.CreateModel](t, changes, "testapp", 0, 0)
	assertEqual(t, "name", op.Name, "AuthorUnmanaged")
	if op.Options.IsManaged() {
		t.Fatal("options: managed should be false")
	}
}

// django: tests/migrations/test_autodetector.py AutodetectorTests.test_unmanaged_delete
func TestAutodetector_UnmanagedDelete(t *testing.T) {
	unmanagedModel := withOptions(model("testapp", "AuthorUnmanaged", field("id", autoField())),
		m.Options{Managed: managed(false)})
	changes := getChanges(t, []*m.ModelState{authorEmpty(), unmanagedModel},
		[]*m.ModelState{authorEmpty()}, nil)
	assertNumberMigrations(t, changes, "testapp", 1)
	assertOperationTypes(t, changes, "testapp", 0, "DeleteModel")
}

// django: tests/migrations/test_autodetector.py AutodetectorTests.test_unmanaged_to_managed
func TestAutodetector_UnmanagedToManaged(t *testing.T) {
	base := model("testapp", "AuthorUnmanaged", field("id", autoField()))
	unmanagedModel := withOptions(base, m.Options{Managed: managed(false)})
	changes := getChanges(t, []*m.ModelState{authorEmpty(), unmanagedModel},
		[]*m.ModelState{authorEmpty(), base}, nil)
	assertNumberMigrations(t, changes, "testapp", 1)
	assertOperationTypes(t, changes, "testapp", 0, "AlterModelOptions")
	op := operationAt[*m.AlterModelOptions](t, changes, "testapp", 0, 0)
	assertEqual(t, "name", op.Name, "authorunmanaged")
	if op.Managed != nil {
		t.Fatalf("options: managed should be unset, got %v", *op.Managed)
	}
}

// django: tests/migrations/test_autodetector.py AutodetectorTests.test_managed_to_unmanaged
func TestAutodetector_ManagedToUnmanaged(t *testing.T) {
	base := model("testapp", "AuthorUnmanaged", field("id", autoField()))
	unmanagedModel := withOptions(base, m.Options{Managed: managed(false)})
	changes := getChanges(t, []*m.ModelState{authorEmpty(), base},
		[]*m.ModelState{authorEmpty(), unmanagedModel}, nil)
	assertNumberMigrations(t, changes, "testapp", 1)
	assertOperationTypes(t, changes, "testapp", 0, "AlterModelOptions")
	op := operationAt[*m.AlterModelOptions](t, changes, "testapp", 0, 0)
	assertEqual(t, "name", op.Name, "authorunmanaged")
	if op.Managed == nil || *op.Managed {
		t.Fatalf("options: managed should be false, got %v", op.Managed)
	}
}

// django: tests/migrations/test_autodetector.py AutodetectorTests.test_unmanaged_custom_pk
func TestAutodetector_UnmanagedCustomPk(t *testing.T) {
	// The default pk field name.
	changes := getChanges(t, nil, []*m.ModelState{authorEmpty(), book()}, nil)
	cm := operationAt[*m.CreateModel](t, changes, "otherapp", 0, 0)
	f, ok := cm.Fields.Get("author")
	if !ok {
		t.Fatal("Book.author not found in CreateModel")
	}
	assertEqual(t, "remote model", f.ForeignKey.To, "testapp.Author")

	// A custom pk field name.
	authorCustomPk := model("testapp", "Author", field("pk_field", pkOf(intField())))
	changes = getChanges(t, nil, []*m.ModelState{authorCustomPk, book()}, nil)
	cm = operationAt[*m.CreateModel](t, changes, "otherapp", 0, 0)
	f, ok = cm.Fields.Get("author")
	if !ok {
		t.Fatal("Book.author not found in CreateModel")
	}
	assertEqual(t, "remote model", f.ForeignKey.To, "testapp.Author")
}

// ---------------------------------------------------------------------------
// many-to-many (gormgate: auto-created join models)
// ---------------------------------------------------------------------------

// django: tests/migrations/test_autodetector.py AutodetectorTests.test_add_many_to_many
//
// gormgate deviation: there is no ManyToManyField. A gorm many2many is an
// auto-created join model, so "adding an m2m" is "adding a model". Like
// Django's test, no default may be asked for.
func TestAutodetector_AddManyToMany(t *testing.T) {
	q := &recordingQuestioner{t: t, forbidNotNullAddition: true}
	join := withOptions(
		model("testapp", "AuthorPublishers",
			field("author_id", pkOf(fk("testapp.Author"))),
			field("publisher_id", pkOf(fk("testapp.Publisher")))),
		m.Options{AutoCreated: true})
	changes := getChanges(t,
		[]*m.ModelState{authorEmpty(), publisher()},
		[]*m.ModelState{authorEmpty(), publisher(), join}, q)
	assertNumberMigrations(t, changes, "testapp", 1)
	assertOperationTypes(t, changes, "testapp", 0, "CreateModel")
	op := operationAt[*m.CreateModel](t, changes, "testapp", 0, 0)
	assertEqual(t, "name", op.Name, "AuthorPublishers")
	if !op.Options.AutoCreated {
		t.Fatal("options: auto_created should be true")
	}
	// Both foreign keys are part of the primary key, so they stay inside
	// CreateModel instead of becoming AddField operations.
	assertEqual(t, "number of fields", len(op.Fields), 2)
}
