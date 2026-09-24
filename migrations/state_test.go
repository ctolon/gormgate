package migrations

import (
	"sort"
	"strings"
	"testing"
)

// django: tests/migrations/test_state.py
//
// Django builds its states from real model classes through
// ModelState.from_model(); gormgate builds ModelState values directly (the
// model -> state step lives in internal/fromgorm), so every test below
// constructs the equivalent ModelState by hand.
//
// Django's ManyToManyField has no gormgate equivalent: a gorm many2many is an
// ordinary join model with two foreign keys. Wherever a Django test uses an
// m2m purely as "a reference to another model", the port uses a foreign key
// and says so.

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func autoField() Field {
	return Field{Type: Int, Size: 64, PrimaryKey: true, AutoIncrement: true}
}

func intField() Field { return Field{Type: Int, Size: 32} }

func textField() Field { return Field{Type: String} }

func charField(size int) Field { return Field{Type: String, Size: size} }

func boolField() Field { return Field{Type: Bool} }

func fkField(to string) Field {
	return Field{Type: Int, Size: 64, ForeignKey: &ForeignKey{To: to, OnDelete: Cascade}}
}

func fkToField(to, toField string) Field {
	return Field{Type: Int, Size: 64, ForeignKey: &ForeignKey{To: to, ToField: toField, OnDelete: Cascade}}
}

func nf(name string, f Field) NamedField { return NamedField{Name: name, Field: f} }

// modelState builds a ModelState; gormgate always carries an explicit table
// name, so it is derived from the app label and model name here.
func modelState(app, name string, fields ...NamedField) *ModelState {
	return &ModelState{
		App:    app,
		Name:   name,
		Table:  app + "_" + strings.ToLower(name),
		Fields: append(Fields(nil), fields...),
	}
}

func mustModelState(t *testing.T, s *ProjectState, app, name string) *ModelState {
	t.Helper()
	ms, err := s.Model(app, name)
	if err != nil {
		t.Fatalf("Model(%q, %q): %v", app, name, err)
	}
	return ms
}

func mustApps(t *testing.T, s *ProjectState) *Apps {
	t.Helper()
	a, err := s.Apps()
	if err != nil {
		t.Fatalf("Apps(): unexpected error %v", err)
	}
	return a
}

func appsError(t *testing.T, s *ProjectState) string {
	t.Helper()
	a, err := s.Apps()
	if err == nil {
		t.Fatalf("Apps(): expected an error, got %d models", len(a.Models()))
	}
	return err.Error()
}

// relationModels mirrors list(ProjectState.relations[target]): the models that
// reference target. Django preserves the insertion order of its cached
// relations dict; gormgate computes them from GetReferences, which is sorted
// by (app, model), so the ports below compare sorted lists.
func relationModels(s *ProjectState, target ModelKey) []ModelKey {
	seen := map[ModelKey]bool{}
	var out []ModelKey
	for _, ref := range GetReferences(s, target, "") {
		if !seen[ref.Model] {
			seen[ref.Model] = true
			out = append(out, ref.Model)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].App != out[j].App {
			return out[i].App < out[j].App
		}
		return out[i].Model < out[j].Model
	})
	return out
}

// relationFields mirrors ProjectState.relations[target][referrer]: the field
// names of referrer that point at target, in declaration order.
func relationFields(s *ProjectState, target, referrer ModelKey) []string {
	var out []string
	for _, ref := range GetReferences(s, target, "") {
		if ref.Model == referrer {
			out = append(out, ref.Name)
		}
	}
	return out
}

func assertModelKeys(t *testing.T, what string, got, want []ModelKey) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s:\n got %v\nwant %v", what, got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("%s:\n got %v\nwant %v", what, got, want)
		}
	}
}

func assertStrings(t *testing.T, what string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s:\n got %v\nwant %v", what, got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("%s:\n got %v\nwant %v", what, got, want)
		}
	}
}

func key(app, model string) ModelKey { return ModelKey{App: app, Model: model} }

// ---------------------------------------------------------------------------
// StateTests
// ---------------------------------------------------------------------------

// TestState_Equality checks that state equality is implemented correctly.
//
// django: tests/migrations/test_state.py StateTests.test_equality
func TestState_Equality(t *testing.T) {
	// Two things that should be equal.
	state := NewProjectState()
	state.AddModel(modelState("migrations", "Tag",
		nf("id", autoField()),
		nf("name", charField(100)),
		nf("hidden", boolField()),
	))
	stateApps := mustApps(t, state) // Fill the apps cache.
	other := state.Clone()

	if !state.Equal(state) {
		t.Fatal("state != itself")
	}
	if !state.Equal(other) {
		t.Fatal("state != its clone")
	}
	if !other.Equal(state) {
		t.Fatal("clone != state")
	}
	// The rendered registries are distinct objects even when the states are
	// equal (Django: assertNotEqual(project_state.apps, other_state.apps)).
	if stateApps == mustApps(t, other) {
		t.Fatal("state.Apps() and clone.Apps() are the same registry")
	}

	// Make a very small change (size 99) and see if that affects it.
	changed := NewProjectState()
	changed.AddModel(modelState("migrations", "Tag",
		nf("id", autoField()),
		nf("name", charField(99)),
		nf("hidden", boolField()),
	))
	if changed.Equal(other) {
		t.Fatal("a CharField size change did not affect state equality")
	}
}

// TestState_DanglingReferencesThrowError checks the error rendering a state
// whose foreign keys point at models that aren't in it.
//
// django: tests/migrations/test_state.py StateTests.test_dangling_references_throw_error
func TestState_DanglingReferencesThrowError(t *testing.T) {
	author := modelState("migrations", "Author", nf("id", autoField()), nf("name", textField()))
	publisher := modelState("migrations", "Publisher", nf("id", autoField()), nf("name", textField()))
	book := modelState("migrations", "Book",
		nf("id", autoField()),
		nf("author", fkField("migrations.Author")),
		nf("publisher", fkField("migrations.Publisher")),
	)
	// Django's Magazine has authors = ManyToManyField(Author); in gormgate the
	// join table is an ordinary model with a foreign key to each side.
	magazineAuthors := modelState("migrations", "Magazine_authors",
		nf("magazine_id", fkField("migrations.Magazine")),
		nf("author_id", fkField("migrations.Author")),
	)
	magazine := modelState("migrations", "Magazine", nf("id", autoField()))

	// A valid ProjectState renders.
	valid := NewProjectState()
	for _, ms := range []*ModelState{author, publisher, book, magazine, magazineAuthors} {
		valid.AddModel(ms.Clone())
	}
	if got := len(mustApps(t, valid).Models()); got != 5 {
		t.Fatalf("rendered models: got %d, want 5", got)
	}

	// Now an invalid one with foreign keys.
	invalid := NewProjectState()
	invalid.AddModel(book.Clone())
	want := "The field migrations.Book.author was declared with a lazy reference to " +
		"'migrations.author', but app 'migrations' doesn't provide model 'author'.\n" +
		"The field migrations.Book.publisher was declared with a lazy reference to " +
		"'migrations.publisher', but app 'migrations' doesn't provide model 'publisher'."
	if got := appsError(t, invalid); got != want {
		t.Fatalf("Apps() error:\n got %q\nwant %q", got, want)
	}

	// And now with multiple models and multiple fields.
	invalid.AddModel(magazineAuthors.Clone())
	want = "The field migrations.Book.author was declared with a lazy reference to " +
		"'migrations.author', but app 'migrations' doesn't provide model 'author'.\n" +
		"The field migrations.Book.publisher was declared with a lazy reference to " +
		"'migrations.publisher', but app 'migrations' doesn't provide model 'publisher'.\n" +
		"The field migrations.Magazine_authors.magazine_id was declared with a lazy reference to " +
		"'migrations.magazine', but app 'migrations' doesn't provide model 'magazine'.\n" +
		"The field migrations.Magazine_authors.author_id was declared with a lazy reference to " +
		"'migrations.author', but app 'migrations' doesn't provide model 'author'."
	if got := appsError(t, invalid); got != want {
		t.Fatalf("Apps() error:\n got %q\nwant %q", got, want)
	}
}

// TestState_DanglingReferenceToUninstalledApp checks the second message
// renderApps produces: a reference into an app that isn't part of the state at
// all.
//
// django: db/migrations/state.py StateApps.__init__ /
// core/checks/model_checks.py _check_lazy_references
func TestState_DanglingReferenceToUninstalledApp(t *testing.T) {
	state := NewProjectState()
	state.AddModel(modelState("migrations", "Book",
		nf("id", autoField()),
		nf("author", fkField("other.Author")),
	))
	want := "The field migrations.Book.author was declared with a lazy reference to " +
		"'other.author', but app 'other' isn't installed."
	if got := appsError(t, state); got != want {
		t.Fatalf("Apps() error:\n got %q\nwant %q", got, want)
	}
}

// TestState_ReferenceMixedCaseAppLabel checks that an app label with mixed
// case resolves.
//
// django: tests/migrations/test_state.py StateTests.test_reference_mixed_case_app_label
func TestState_ReferenceMixedCaseAppLabel(t *testing.T) {
	const app = "MiXedCase_migrations"
	state := NewProjectState()
	state.AddModel(modelState(app, "Author", nf("id", autoField())))
	state.AddModel(modelState(app, "Book", nf("id", autoField()), nf("author", fkField(app+".Author"))))
	// Django's Magazine.authors m2m becomes an explicit join model here.
	state.AddModel(modelState(app, "Magazine_authors",
		nf("magazine_id", fkField(app+".Magazine")),
		nf("author_id", fkField(app+".Author")),
	))
	state.AddModel(modelState(app, "Magazine", nf("id", autoField())))

	if got := len(mustApps(t, state).Models()); got != 4 {
		t.Fatalf("rendered models: got %d, want 4", got)
	}
}

// TestState_RealApps checks that including real apps resolves dangling FK
// errors. Django relies on the contenttypes app always being loaded; gormgate
// takes the real models explicitly through ProjectState.RealModels.
//
// django: tests/migrations/test_state.py StateTests.test_real_apps
func TestState_RealApps(t *testing.T) {
	testModel := modelState("migrations", "TestModel",
		nf("id", autoField()),
		nf("ct", fkField("contenttypes.ContentType")),
	)
	contentType := modelState("contenttypes", "ContentType", nf("id", autoField()))

	// In an empty state it fails.
	bare := NewProjectState()
	bare.AddModel(testModel.Clone())
	if got := appsError(t, bare); !strings.Contains(got, "contenttypes.contenttype") {
		t.Fatalf("Apps() error: got %q, want a lazy reference error for contenttypes", got)
	}

	// Including the real app it succeeds.
	withReal := NewProjectState()
	withReal.RealApps = map[string]bool{"contenttypes": true}
	withReal.RealModels = []*ModelState{contentType}
	withReal.AddModel(testModel.Clone())
	apps := mustApps(t, withReal)
	migrationsModels := 0
	for _, model := range apps.Models() {
		if model.App == "migrations" {
			migrationsModels++
		}
	}
	if migrationsModels != 1 {
		t.Fatalf("models of app 'migrations': got %d, want 1", migrationsModels)
	}
	if _, err := apps.GetModel("contenttypes", "ContentType"); err != nil {
		t.Fatalf("real app model not rendered: %v", err)
	}
}

// TestState_RenderUniqueAppLabels checks that two dotted app names whose last
// part is the same both render.
//
// django: tests/migrations/test_state.py StateTests.test_render_unique_app_labels
func TestState_RenderUniqueAppLabels(t *testing.T) {
	state := NewProjectState()
	state.AddModel(modelState("django.contrib.auth", "A", nf("id", autoField())))
	state.AddModel(modelState("vendor.auth", "B", nf("id", autoField())))

	if got := len(mustApps(t, state).Models()); got != 2 {
		t.Fatalf("rendered models: got %d, want 2", got)
	}
}

// TestState_SelfRelation checks that a model pointing at itself is rendered
// once, and that altering it leaves the old rendered state consistent.
//
// Django's #24513 uses a symmetrical-free ManyToManyField("self"); the
// gormgate equivalent of "a field of A pointing at A" is a self foreign key.
//
// django: tests/migrations/test_state.py StateTests.test_self_relation
func TestState_SelfRelation(t *testing.T) {
	state := NewProjectState()
	state.AddModel(modelState("something", "A",
		nf("id", autoField()),
		nf("to_a", fkField("something.A")),
	))
	modelA := mustApps(t, state).MustModel("something", "A")
	if got := len(modelA.RelatedFields()); got != 1 {
		t.Fatalf("A.RelatedFields(): got %d, want 1", got)
	}
	oldState := state.Clone()

	op := &AlterField{ModelName: "A", Name: "to_a", Field: Field{
		Type: Int, Size: 64, Null: true, ForeignKey: &ForeignKey{To: "something.A", OnDelete: Cascade},
	}}
	if err := op.StateForwards("something", state); err != nil {
		t.Fatal(err)
	}

	modelAOld := mustApps(t, oldState).MustModel("something", "A")
	modelANew := mustApps(t, state).MustModel("something", "A")
	if modelAOld == modelANew {
		t.Fatal("the old and the new rendered model are the same object")
	}
	// Both registries stay self-consistent: each model's self relation points
	// at the model of its own registry, not at a stale copy.
	if fld := modelAOld.Field("to_a"); fld.Remote != modelAOld {
		t.Fatal("old A.to_a does not point at the old A")
	}
	if fld := modelANew.Field("to_a"); fld.Remote != modelANew {
		t.Fatal("new A.to_a does not point at the new A")
	}
	if got := len(modelAOld.RelatedFields()); got != 1 {
		t.Fatalf("old A.RelatedFields(): got %d, want 1", got)
	}
	if got := len(modelANew.RelatedFields()); got != 1 {
		t.Fatalf("new A.RelatedFields(): got %d, want 1", got)
	}
	// The old state still holds the old field definition.
	oldField, _ := mustModelState(t, oldState, "something", "A").Fields.Get("to_a")
	if oldField.Null {
		t.Fatal("altering the new state changed the field of the old state")
	}
}

// TestState_AddRelations checks that adding a relation to an existing model
// re-renders the referenced models too (#24573).
//
// Django's A/B are a concrete-inheritance pair; gormgate has no model
// inheritance, so B holds an explicit foreign key to A instead.
//
// django: tests/migrations/test_state.py StateTests.test_add_relations
func TestState_AddRelations(t *testing.T) {
	state := NewProjectState()
	state.AddModel(modelState("something", "A", nf("id", autoField())))
	state.AddModel(modelState("something", "B", nf("id", autoField()), nf("a_ptr", fkField("something.A"))))
	state.AddModel(modelState("something", "C", nf("id", autoField())))
	mustApps(t, state) // Work with rendered models.

	oldState := state.Clone()
	oldApps := mustApps(t, oldState)
	modelAOld := oldApps.MustModel("something", "A")
	modelBOld := oldApps.MustModel("something", "B")
	modelCOld := oldApps.MustModel("something", "C")
	// The relations between the old models are correct.
	if modelBOld.Field("a_ptr").Remote != modelAOld {
		t.Fatal("old B.a_ptr does not point at the old A")
	}

	op := &AddField{ModelName: "C", Name: "to_a", Field: fkField("something.A")}
	if err := op.StateForwards("something", state); err != nil {
		t.Fatal(err)
	}

	newApps := mustApps(t, state)
	modelANew := newApps.MustModel("something", "A")
	modelBNew := newApps.MustModel("something", "B")
	modelCNew := newApps.MustModel("something", "C")
	// All models have changed.
	if modelAOld == modelANew || modelBOld == modelBNew || modelCOld == modelCNew {
		t.Fatal("expected the rendered models to be re-created")
	}
	// The relations between the old models still hold.
	if modelBOld.Field("a_ptr").Remote != modelAOld {
		t.Fatal("old B.a_ptr no longer points at the old A")
	}
	if got := len(modelAOld.RelatedFields()); got != 1 {
		t.Fatalf("old A.RelatedFields(): got %d, want 1", got)
	}
	// The relations between the new models are correct.
	if modelBNew.Field("a_ptr").Remote != modelANew {
		t.Fatal("new B.a_ptr does not point at the new A")
	}
	if modelCNew.Field("to_a").Remote != modelANew {
		t.Fatal("new C.to_a does not point at the new A")
	}
	related := modelANew.RelatedFields()
	if len(related) != 2 {
		t.Fatalf("new A.RelatedFields(): got %d, want 2", len(related))
	}
}

// TestState_RemoveRelations checks that relations between models are updated
// while the relations of an old state remain (#24225).
//
// django: tests/migrations/test_state.py StateTests.test_remove_relations
func TestState_RemoveRelations(t *testing.T) {
	base := func() *ProjectState {
		s := NewProjectState()
		s.AddModel(modelState("something", "A", nf("id", autoField())))
		s.AddModel(modelState("something", "B", nf("id", autoField()), nf("to_a", fkField("something.A"))))
		return s
	}

	for _, tc := range []struct {
		name string
		op   Operation
	}{
		{"RemoveField", &RemoveField{ModelName: "B", Name: "to_a"}},
		{"DeleteModel", &DeleteModel{Name: "B"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := base()
			if got := len(mustApps(t, state).MustModel("something", "A").RelatedFields()); got != 1 {
				t.Fatalf("A.RelatedFields(): got %d, want 1", got)
			}
			oldState := state.Clone()

			if err := tc.op.StateForwards("something", state); err != nil {
				t.Fatal(err)
			}

			modelAOld := mustApps(t, oldState).MustModel("something", "A")
			modelANew := mustApps(t, state).MustModel("something", "A")
			if modelAOld == modelANew {
				t.Fatal("the old and the new rendered A are the same object")
			}
			if got := len(modelAOld.RelatedFields()); got != 1 {
				t.Fatalf("old A.RelatedFields(): got %d, want 1", got)
			}
			if got := len(modelANew.RelatedFields()); got != 0 {
				t.Fatalf("new A.RelatedFields(): got %d, want 0", got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// StateRelationsTests
//
// Django's base state is User / Comment(text, user FK, comments M2M to self) /
// Post(text, authors M2M to User). gormgate has no m2m field, so the two m2m
// references become a self foreign key on Comment and a foreign key on Post;
// the resulting relations are exactly the ones Django asserts.
// ---------------------------------------------------------------------------

func baseRelationsState() *ProjectState {
	s := NewProjectState()
	s.AddModel(modelState("tests", "User", nf("id", autoField())))
	s.AddModel(modelState("tests", "Comment",
		nf("id", autoField()),
		nf("text", textField()),
		nf("user", fkField("tests.User")),
		nf("parent", fkField("tests.Comment")),
	))
	s.AddModel(modelState("tests", "Post",
		nf("id", autoField()),
		nf("text", textField()),
		nf("author", fkField("tests.User")),
	))
	return s
}

// django: tests/migrations/test_state.py StateRelationsTests.test_add_model
func TestState_Relations_AddModel(t *testing.T) {
	state := baseRelationsState()
	assertModelKeys(t, `relations["tests","user"]`, relationModels(state, key("tests", "user")),
		[]ModelKey{key("tests", "comment"), key("tests", "post")})
	assertModelKeys(t, `relations["tests","comment"]`, relationModels(state, key("tests", "comment")),
		[]ModelKey{key("tests", "comment")})
	assertModelKeys(t, `relations["tests","post"]`, relationModels(state, key("tests", "post")), nil)
}

// django: tests/migrations/test_state.py StateRelationsTests.test_add_model_no_relations
func TestState_Relations_AddModelNoRelations(t *testing.T) {
	state := NewProjectState()
	state.AddModel(modelState("migrations", "Tag", nf("id", autoField())))
	assertModelKeys(t, `relations["migrations","tag"]`,
		relationModels(state, key("migrations", "tag")), nil)
}

// django: tests/migrations/test_state.py StateRelationsTests.test_add_model_other_app
func TestState_Relations_AddModelOtherApp(t *testing.T) {
	state := baseRelationsState()
	assertModelKeys(t, `relations["tests","user"]`, relationModels(state, key("tests", "user")),
		[]ModelKey{key("tests", "comment"), key("tests", "post")})

	state.AddModel(modelState("tests_other", "Comment",
		nf("id", autoField()),
		nf("user", fkField("tests.User")),
	))
	assertModelKeys(t, `relations["tests","user"]`, relationModels(state, key("tests", "user")),
		[]ModelKey{key("tests", "comment"), key("tests", "post"), key("tests_other", "comment")})
}

// django: tests/migrations/test_state.py StateRelationsTests.test_remove_model
func TestState_Relations_RemoveModel(t *testing.T) {
	state := baseRelationsState()
	assertModelKeys(t, `relations["tests","user"]`, relationModels(state, key("tests", "user")),
		[]ModelKey{key("tests", "comment"), key("tests", "post")})
	assertModelKeys(t, `relations["tests","comment"]`, relationModels(state, key("tests", "comment")),
		[]ModelKey{key("tests", "comment")})

	state.RemoveModel("tests", "Comment")
	assertModelKeys(t, `relations["tests","user"] after removing comment`,
		relationModels(state, key("tests", "user")), []ModelKey{key("tests", "post")})
	assertModelKeys(t, `relations["tests","comment"] after removing comment`,
		relationModels(state, key("tests", "comment")), nil)

	state.RemoveModel("tests", "Post")
	assertModelKeys(t, `relations["tests","user"] after removing post`,
		relationModels(state, key("tests", "user")), nil)

	state.RemoveModel("tests", "User")
	if len(state.Models) != 0 {
		t.Fatalf("models left after removing all: %v", state.SortedKeys())
	}
}

// TestState_Relations_RenameModel checks that renaming a model repoints every
// reference to it, including a reference from the renamed model itself.
//
// django: tests/migrations/test_state.py StateRelationsTests.test_rename_model
func TestState_Relations_RenameModel(t *testing.T) {
	state := baseRelationsState()
	assertModelKeys(t, `relations["tests","user"]`, relationModels(state, key("tests", "user")),
		[]ModelKey{key("tests", "comment"), key("tests", "post")})
	assertModelKeys(t, `relations["tests","comment"]`, relationModels(state, key("tests", "comment")),
		[]ModelKey{key("tests", "comment")})
	relatedFields := relationFields(state, key("tests", "user"), key("tests", "comment"))

	state.RenameModel("tests", "Comment", "Opinion")

	// Django asserts [("tests","post"), ("tests","opinion")] in insertion
	// order; gormgate reports references sorted by (app, model).
	assertModelKeys(t, `relations["tests","user"] after rename`,
		relationModels(state, key("tests", "user")),
		[]ModelKey{key("tests", "opinion"), key("tests", "post")})
	assertModelKeys(t, `relations["tests","opinion"] after rename`,
		relationModels(state, key("tests", "opinion")),
		[]ModelKey{key("tests", "opinion")})
	assertModelKeys(t, `relations["tests","comment"] after rename`,
		relationModels(state, key("tests", "comment")), nil)
	assertStrings(t, `relations["tests","user"]["tests","opinion"]`,
		relationFields(state, key("tests", "user"), key("tests", "opinion")), relatedFields)
	// The self reference of the renamed model points at the new name.
	opinion := mustModelState(t, state, "tests", "Opinion")
	parent, _ := opinion.Fields.Get("parent")
	if got, want := parent.ForeignKey.To, "tests.Opinion"; got != want {
		t.Fatalf("Opinion.parent.To: got %q, want %q", got, want)
	}
	// The renamed state renders.
	mustApps(t, state)

	state.RenameModel("tests", "User", "Author")
	assertModelKeys(t, `relations["tests","author"]`, relationModels(state, key("tests", "author")),
		[]ModelKey{key("tests", "opinion"), key("tests", "post")})
	assertModelKeys(t, `relations["tests","user"] after rename`,
		relationModels(state, key("tests", "user")), nil)
	mustApps(t, state)
}

// django: tests/migrations/test_state.py StateRelationsTests.test_rename_model_no_relations
func TestState_Relations_RenameModelNoRelations(t *testing.T) {
	state := baseRelationsState()
	assertModelKeys(t, `relations["tests","user"]`, relationModels(state, key("tests", "user")),
		[]ModelKey{key("tests", "comment"), key("tests", "post")})
	relatedFields := relationFields(state, key("tests", "user"), key("tests", "post"))
	assertModelKeys(t, `relations["tests","post"]`, relationModels(state, key("tests", "post")), nil)

	// Rename a model without incoming relations.
	state.RenameModel("tests", "Post", "Blog")
	assertModelKeys(t, `relations["tests","user"] after rename`,
		relationModels(state, key("tests", "user")),
		[]ModelKey{key("tests", "blog"), key("tests", "comment")})
	assertModelKeys(t, `relations["tests","blog"]`, relationModels(state, key("tests", "blog")), nil)
	assertStrings(t, `relations["tests","user"]["tests","blog"]`,
		relationFields(state, key("tests", "user"), key("tests", "blog")), relatedFields)
}

// django: tests/migrations/test_state.py StateRelationsTests.test_add_field
func TestState_Relations_AddField(t *testing.T) {
	state := baseRelationsState()
	assertModelKeys(t, `relations["tests","post"]`, relationModels(state, key("tests", "post")), nil)

	// Add a self-referential foreign key.
	state.AddField("tests", "Post", "next_post", fkField("tests.Post"), true)
	assertModelKeys(t, `relations["tests","post"]`, relationModels(state, key("tests", "post")),
		[]ModelKey{key("tests", "post")})
	assertStrings(t, `relations["tests","post"]["tests","post"]`,
		relationFields(state, key("tests", "post"), key("tests", "post")), []string{"next_post"})

	// Add a foreign key.
	state.AddField("tests", "Comment", "post", fkField("tests.Post"), true)
	assertModelKeys(t, `relations["tests","post"]`, relationModels(state, key("tests", "post")),
		[]ModelKey{key("tests", "comment"), key("tests", "post")})
	assertStrings(t, `relations["tests","post"]["tests","comment"]`,
		relationFields(state, key("tests", "post"), key("tests", "comment")), []string{"post"})
}

// django: tests/migrations/test_state.py StateRelationsTests.test_remove_field
func TestState_Relations_RemoveField(t *testing.T) {
	state := baseRelationsState()
	assertModelKeys(t, `relations["tests","user"]`, relationModels(state, key("tests", "user")),
		[]ModelKey{key("tests", "comment"), key("tests", "post")})

	state.RemoveField("tests", "Post", "author")
	assertModelKeys(t, `relations["tests","user"]`, relationModels(state, key("tests", "user")),
		[]ModelKey{key("tests", "comment")})

	state.RemoveField("tests", "Comment", "user")
	assertModelKeys(t, `relations["tests","user"]`, relationModels(state, key("tests", "user")), nil)
}

// django: tests/migrations/test_state.py StateRelationsTests.test_remove_field_no_relations
func TestState_Relations_RemoveFieldNoRelations(t *testing.T) {
	state := baseRelationsState()
	assertModelKeys(t, `relations["tests","user"]`, relationModels(state, key("tests", "user")),
		[]ModelKey{key("tests", "comment"), key("tests", "post")})

	// Remove a non-relation field.
	state.RemoveField("tests", "Post", "text")
	assertModelKeys(t, `relations["tests","user"]`, relationModels(state, key("tests", "user")),
		[]ModelKey{key("tests", "comment"), key("tests", "post")})
}

// django: tests/migrations/test_state.py StateRelationsTests.test_rename_field
func TestState_Relations_RenameField(t *testing.T) {
	state := baseRelationsState()
	field, _ := mustModelState(t, state, "tests", "Comment").Fields.Get("user")
	assertStrings(t, `relations["tests","user"]["tests","comment"]`,
		relationFields(state, key("tests", "user"), key("tests", "comment")), []string{"user"})

	if err := state.RenameField("tests", "Comment", "user", "author"); err != nil {
		t.Fatal(err)
	}
	renamed, ok := mustModelState(t, state, "tests", "Comment").Fields.Get("author")
	if !ok {
		t.Fatal("Comment.author missing after RenameField")
	}
	assertStrings(t, `relations["tests","user"]["tests","comment"]`,
		relationFields(state, key("tests", "user"), key("tests", "comment")), []string{"author"})
	if !field.Equal(renamed) {
		t.Fatal("the renamed field is not the field that was renamed")
	}
}

// django: tests/migrations/test_state.py StateRelationsTests.test_rename_field_no_relations
func TestState_Relations_RenameFieldNoRelations(t *testing.T) {
	state := baseRelationsState()
	assertModelKeys(t, `relations["tests","user"]`, relationModels(state, key("tests", "user")),
		[]ModelKey{key("tests", "comment"), key("tests", "post")})

	// Rename a non-relation field.
	if err := state.RenameField("tests", "Post", "text", "description"); err != nil {
		t.Fatal(err)
	}
	assertModelKeys(t, `relations["tests","user"]`, relationModels(state, key("tests", "user")),
		[]ModelKey{key("tests", "comment"), key("tests", "post")})
}

// django: tests/migrations/test_state.py StateRelationsTests.test_alter_field
func TestState_Relations_AlterField(t *testing.T) {
	state := baseRelationsState()
	assertModelKeys(t, `relations["tests","user"]`, relationModels(state, key("tests", "user")),
		[]ModelKey{key("tests", "comment"), key("tests", "post")})

	// Alter a foreign key to a non-relation field.
	state.AlterField("tests", "Comment", "user", intField(), true)
	assertModelKeys(t, `relations["tests","user"]`, relationModels(state, key("tests", "user")),
		[]ModelKey{key("tests", "post")})

	// Alter a non-relation field back to a relation.
	fk := fkField("tests.User")
	state.AlterField("tests", "Comment", "user", fk, true)
	assertModelKeys(t, `relations["tests","user"]`, relationModels(state, key("tests", "user")),
		[]ModelKey{key("tests", "comment"), key("tests", "post")})
	assertStrings(t, `relations["tests","user"]["tests","comment"]`,
		relationFields(state, key("tests", "user"), key("tests", "comment")), []string{"user"})
}

// TestState_Relations_AlterFieldToOtherApp alters a relation so that it points
// at another app's model.
//
// django: tests/migrations/test_state.py StateRelationsTests.test_alter_field_m2m_to_fk
func TestState_Relations_AlterFieldToOtherApp(t *testing.T) {
	state := baseRelationsState()
	state.AddModel(modelState("tests_other", "User_other", nf("id", autoField())))
	assertModelKeys(t, `relations["tests","user"]`, relationModels(state, key("tests", "user")),
		[]ModelKey{key("tests", "comment"), key("tests", "post")})
	assertModelKeys(t, `relations["tests_other","user_other"]`,
		relationModels(state, key("tests_other", "user_other")), nil)

	state.AlterField("tests", "Post", "author", fkField("tests_other.User_other"), true)
	assertModelKeys(t, `relations["tests","user"]`, relationModels(state, key("tests", "user")),
		[]ModelKey{key("tests", "comment")})
	assertModelKeys(t, `relations["tests_other","user_other"]`,
		relationModels(state, key("tests_other", "user_other")), []ModelKey{key("tests", "post")})
	assertStrings(t, `relations["tests_other","user_other"]["tests","post"]`,
		relationFields(state, key("tests_other", "user_other"), key("tests", "post")), []string{"author"})
}

// django: tests/migrations/test_state.py StateRelationsTests.test_many_relations_to_same_model
func TestState_Relations_ManyRelationsToSameModel(t *testing.T) {
	state := baseRelationsState()
	state.AddField("tests", "Comment", "reviewer", fkField("tests.User"), true)
	assertModelKeys(t, `relations["tests","user"]`, relationModels(state, key("tests", "user")),
		[]ModelKey{key("tests", "comment"), key("tests", "post")})
	// Two foreign keys to the same model.
	assertStrings(t, `relations["tests","user"]["tests","comment"]`,
		relationFields(state, key("tests", "user"), key("tests", "comment")),
		[]string{"user", "reviewer"})

	// Rename the second foreign key.
	if err := state.RenameField("tests", "Comment", "reviewer", "supervisor"); err != nil {
		t.Fatal(err)
	}
	assertStrings(t, `relations["tests","user"]["tests","comment"] after rename`,
		relationFields(state, key("tests", "user"), key("tests", "comment")),
		[]string{"user", "supervisor"})

	// Remove the first foreign key.
	state.RemoveField("tests", "Comment", "user")
	assertStrings(t, `relations["tests","user"]["tests","comment"] after removal`,
		relationFields(state, key("tests", "user"), key("tests", "comment")),
		[]string{"supervisor"})
}

// ---------------------------------------------------------------------------
// ProjectState mutators without a dedicated Django test in test_state.py.
// ---------------------------------------------------------------------------

// TestState_RenameFieldReferences checks that renaming a field repoints
// unique_together, indexes, constraints and the to_field of foreign keys
// aimed at it.
//
// django: db/migrations/state.py ProjectState.rename_field
func TestState_RenameFieldReferences(t *testing.T) {
	state := NewProjectState()
	author := modelState("tests", "Author",
		nf("id", autoField()),
		nf("name", charField(100)),
		nf("surname", charField(100)),
	)
	author.Options.UniqueTogether = [][]string{{"name", "surname"}}
	author.Options.Indexes = []Index{{
		Name:    "idx_name",
		Fields:  []IndexField{{Column: "name"}},
		Include: []string{"name"},
	}}
	author.Options.Constraints = []Constraint{
		&UniqueConstraint{Name: "uniq_name", Fields: []string{"name"}, Include: []string{"name"}},
	}
	state.AddModel(author)
	state.AddModel(modelState("tests", "Book",
		nf("id", autoField()),
		nf("author_name", fkToField("tests.Author", "name")),
	))

	if err := state.RenameField("tests", "Author", "name", "first_name"); err != nil {
		t.Fatal(err)
	}

	got := mustModelState(t, state, "tests", "Author")
	assertStrings(t, "unique_together", got.Options.UniqueTogether[0], []string{"first_name", "surname"})
	if c := got.Options.Indexes[0].Fields[0].Column; c != "first_name" {
		t.Fatalf("index column: got %q, want %q", c, "first_name")
	}
	assertStrings(t, "index include", got.Options.Indexes[0].Include, []string{"first_name"})
	uc := got.Options.Constraints[0].(*UniqueConstraint)
	assertStrings(t, "constraint fields", uc.Fields, []string{"first_name"})
	assertStrings(t, "constraint include", uc.Include, []string{"first_name"})
	book, _ := mustModelState(t, state, "tests", "Book").Fields.Get("author_name")
	if book.ForeignKey.ToField != "first_name" {
		t.Fatalf("Book.author_name.ToField: got %q, want %q", book.ForeignKey.ToField, "first_name")
	}
	mustApps(t, state)
}

// TestState_RenameFieldMissing checks the FieldDoesNotExist error.
//
// django: db/migrations/state.py ProjectState.rename_field
func TestState_RenameFieldMissing(t *testing.T) {
	state := baseRelationsState()
	err := state.RenameField("tests", "Comment", "nope", "other")
	if err == nil {
		t.Fatal("expected FieldDoesNotExist")
	}
	if _, ok := err.(*FieldDoesNotExist); !ok {
		t.Fatalf("expected FieldDoesNotExist, got %T", err)
	}
	if got, want := err.Error(), "tests.comment has no field named 'nope'"; got != want {
		t.Fatalf("error:\n got %q\nwant %q", got, want)
	}
}

// TestState_AddFieldPreserveDefault checks that add_field/alter_field drop the
// one-off default when preserve_default is false.
//
// django: db/migrations/state.py ProjectState.add_field / alter_field
func TestState_AddFieldPreserveDefault(t *testing.T) {
	state := NewProjectState()
	state.AddModel(modelState("tests", "Author", nf("id", autoField())))

	withDefault := charField(100)
	withDefault.Default = "anon"
	state.AddField("tests", "Author", "name", withDefault, false)
	got, _ := mustModelState(t, state, "tests", "Author").Fields.Get("name")
	if got.Default != nil {
		t.Fatalf("add_field(preserve_default=False) kept the default %v", got.Default)
	}

	state.AddField("tests", "Author", "nickname", withDefault, true)
	got, _ = mustModelState(t, state, "tests", "Author").Fields.Get("nickname")
	if got.Default != "anon" {
		t.Fatalf("add_field(preserve_default=True) lost the default: %v", got.Default)
	}

	state.AlterField("tests", "Author", "nickname", withDefault, false)
	got, _ = mustModelState(t, state, "tests", "Author").Fields.Get("nickname")
	if got.Default != nil {
		t.Fatalf("alter_field(preserve_default=False) kept the default %v", got.Default)
	}
}

// TestState_ModelOptions checks the index, constraint, unique_together and
// table options mutators.
//
// django: db/migrations/state.py ProjectState.add_index / remove_index /
// rename_index / add_constraint / remove_constraint / alter_constraint /
// alter_model_options / remove_model_options
func TestState_ModelOptions(t *testing.T) {
	state := NewProjectState()
	state.AddModel(modelState("tests", "Author",
		nf("id", autoField()), nf("name", charField(100)), nf("surname", charField(100))))
	ms := mustModelState(t, state, "tests", "Author")

	// Indexes.
	idx := Index{Name: "idx_name", Fields: []IndexField{{Column: "name"}}}
	state.AddIndex("tests", "Author", idx)
	if len(ms.Options.Indexes) != 1 || ms.Options.Indexes[0].Name != "idx_name" {
		t.Fatalf("add_index: %v", ms.Options.Indexes)
	}
	// The stored index is a copy: mutating the argument does not change state.
	idx.Name = "mutated"
	if ms.Options.Indexes[0].Name != "idx_name" {
		t.Fatal("add_index stored the caller's index instead of a clone")
	}
	if _, err := ms.GetIndexByName("idx_name"); err != nil {
		t.Fatal(err)
	}
	state.RenameIndex("tests", "Author", "idx_name", "idx_author_name")
	if ms.Options.Indexes[0].Name != "idx_author_name" {
		t.Fatalf("rename_index: %v", ms.Options.Indexes)
	}
	if _, err := ms.GetIndexByName("idx_name"); err == nil {
		t.Fatal("GetIndexByName still finds the old index name")
	}
	state.RemoveIndex("tests", "Author", "idx_author_name")
	if len(ms.Options.Indexes) != 0 {
		t.Fatalf("remove_index: %v", ms.Options.Indexes)
	}

	// Constraints.
	check := &CheckConstraint{Name: "name_not_empty", Check: "name <> ''"}
	state.AddConstraint("tests", "Author", check)
	if len(ms.Options.Constraints) != 1 {
		t.Fatalf("add_constraint: %v", ms.Options.Constraints)
	}
	if _, err := ms.GetConstraintByName("name_not_empty"); err != nil {
		t.Fatal(err)
	}
	altered := &CheckConstraint{Name: "name_not_empty", Check: "name <> ''", ViolationErrorMessage: "nope"}
	state.AlterConstraint("tests", "Author", "name_not_empty", altered)
	got, err := ms.GetConstraintByName("name_not_empty")
	if err != nil {
		t.Fatal(err)
	}
	if !ConstraintEqual(got, altered) {
		t.Fatalf("alter_constraint: got %#v", got)
	}
	state.RemoveConstraint("tests", "Author", "name_not_empty")
	if len(ms.Options.Constraints) != 0 {
		t.Fatalf("remove_constraint: %v", ms.Options.Constraints)
	}

	// unique_together.
	state.AlterUniqueTogether("tests", "Author", [][]string{{"name", "surname"}, {"name"}})
	if len(ms.Options.UniqueTogether) != 2 {
		t.Fatalf("alter_unique_together: %v", ms.Options.UniqueTogether)
	}
	state.RemoveUniqueTogether("tests", "Author", []string{"name"})
	if len(ms.Options.UniqueTogether) != 1 ||
		strings.Join(ms.Options.UniqueTogether[0], ",") != "name,surname" {
		t.Fatalf("remove_model_options: %v", ms.Options.UniqueTogether)
	}
	state.AlterUniqueTogether("tests", "Author", nil)
	if len(ms.Options.UniqueTogether) != 0 {
		t.Fatalf("alter_unique_together(nil): %v", ms.Options.UniqueTogether)
	}

	// managed / table / table comment.
	state.AlterModelOptions("tests", "Author", Ptr(false), true)
	if ms.Options.IsManaged() {
		t.Fatal("alter_model_options did not set managed")
	}
	state.AlterTable("tests", "Author", "renamed_author")
	if ms.Table != "renamed_author" {
		t.Fatalf("alter_db_table: got %q", ms.Table)
	}
	state.AlterTableComment("tests", "Author", "authors of books")
	if ms.Options.DBTableComment != "authors of books" {
		t.Fatalf("alter_db_table_comment: got %q", ms.Options.DBTableComment)
	}
}

// TestState_Clone checks that a cloned state is fully independent.
//
// django: db/migrations/state.py ProjectState.clone / ModelState.clone
func TestState_Clone(t *testing.T) {
	state := baseRelationsState()
	author := mustModelState(t, state, "tests", "Comment")
	author.Options.UniqueTogether = [][]string{{"text"}}
	author.Options.Indexes = []Index{{Name: "idx_text", Fields: []IndexField{{Column: "text"}}}}
	author.Options.Constraints = []Constraint{&CheckConstraint{Name: "c", Check: "1=1"}}

	clone := state.Clone()
	if !state.Equal(clone) {
		t.Fatal("a clone is not equal to its source")
	}

	// Mutating the clone leaves the source alone.
	clone.AddField("tests", "Comment", "extra", textField(), true)
	clone.AlterUniqueTogether("tests", "Comment", [][]string{{"text", "user"}})
	clone.AddIndex("tests", "Comment", Index{Name: "idx_user", Fields: []IndexField{{Column: "user"}}})
	clone.RemoveConstraint("tests", "Comment", "c")
	if err := clone.RenameField("tests", "Comment", "text", "body"); err != nil {
		t.Fatal(err)
	}

	src := mustModelState(t, state, "tests", "Comment")
	if _, ok := src.Fields.Get("extra"); ok {
		t.Fatal("AddField on the clone changed the source")
	}
	if _, ok := src.Fields.Get("text"); !ok {
		t.Fatal("RenameField on the clone changed the source")
	}
	if len(src.Options.UniqueTogether) != 1 || len(src.Options.UniqueTogether[0]) != 1 {
		t.Fatalf("unique_together of the source changed: %v", src.Options.UniqueTogether)
	}
	if len(src.Options.Indexes) != 1 {
		t.Fatalf("indexes of the source changed: %v", src.Options.Indexes)
	}
	if len(src.Options.Constraints) != 1 {
		t.Fatalf("constraints of the source changed: %v", src.Options.Constraints)
	}
	if state.Equal(clone) {
		t.Fatal("the mutated clone is still equal to its source")
	}
}

// TestState_FieldReferences covers field_references / get_references /
// field_is_referenced.
//
// django: db/migrations/utils.py field_references / get_references /
// field_is_referenced
func TestState_FieldReferences(t *testing.T) {
	state := NewProjectState()
	state.AddModel(modelState("tests", "Author", nf("id", autoField()), nf("name", charField(100))))
	state.AddModel(modelState("tests", "Book",
		nf("id", autoField()),
		nf("author", fkField("tests.Author")),                // to the primary key
		nf("author_name", fkToField("tests.Author", "name")), // to a specific field
		nf("title", charField(100)),                          // not a relation
	))

	if !FieldIsReferenced(state, key("tests", "author"), "name") {
		t.Fatal("Author.name is referenced but FieldIsReferenced says no")
	}
	if FieldIsReferenced(state, key("tests", "book"), "title") {
		t.Fatal("Book.title is not referenced but FieldIsReferenced says yes")
	}

	refs := GetReferences(state, key("tests", "author"), "")
	if len(refs) != 2 {
		t.Fatalf("GetReferences(author): got %d, want 2", len(refs))
	}
	refs = GetReferences(state, key("tests", "author"), "name")
	if len(refs) != 2 {
		// Django documents this false positive: a foreign key without a
		// to_field matches any referenced field name when the referenced
		// field isn't passed in.
		t.Fatalf("GetReferences(author, name): got %d, want 2", len(refs))
	}

	// A non-relation field never references anything.
	title, _ := mustModelState(t, state, "tests", "Book").Fields.Get("title")
	if FieldReferences(key("tests", "book"), title, key("tests", "author"), "") {
		t.Fatal("a non-relation field reported a reference")
	}
	// An unqualified relation resolves against the containing model's app.
	local := fkField("Author")
	if !FieldReferences(key("tests", "book"), local, key("tests", "author"), "") {
		t.Fatal("an unqualified relation did not resolve against the model's app")
	}
	if FieldReferences(key("other", "book"), local, key("tests", "author"), "") {
		t.Fatal("an unqualified relation resolved against the wrong app")
	}
}

// TestState_RenderUnknownToField checks the error for a foreign key whose
// to_field does not exist on the target.
//
// django: db/migrations/state.py StateApps.__init__
func TestState_RenderUnknownToField(t *testing.T) {
	state := NewProjectState()
	state.AddModel(modelState("tests", "Author", nf("id", autoField())))
	state.AddModel(modelState("tests", "Book",
		nf("id", autoField()),
		nf("author_name", fkToField("tests.Author", "name")),
	))
	want := "The field tests.Book.author_name references 'tests.author.name', which does not exist."
	if got := appsError(t, state); got != want {
		t.Fatalf("Apps() error:\n got %q\nwant %q", got, want)
	}
}

// TestState_RenderNoSinglePK checks the error for a foreign key to a model
// without a single-column primary key.
//
// django: db/migrations/state.py StateApps.__init__
func TestState_RenderNoSinglePK(t *testing.T) {
	state := NewProjectState()
	state.AddModel(modelState("tests", "Membership",
		nf("user_id", Field{Type: Int, Size: 64, PrimaryKey: true}),
		nf("group_id", Field{Type: Int, Size: 64, PrimaryKey: true}),
	))
	state.AddModel(modelState("tests", "Log", nf("id", autoField()), nf("membership", fkField("tests.Membership"))))
	want := "The field tests.Log.membership references 'tests.membership', which has no single-column primary key."
	if got := appsError(t, state); got != want {
		t.Fatalf("Apps() error:\n got %q\nwant %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// ModelStateTests
// ---------------------------------------------------------------------------

// TestModelState_SanityIndexName checks that an index without a name is
// rejected. Django renders the offending index as "<Index: fields=['field']>";
// gormgate renders the Go value, so only the fixed text is asserted.
//
// django: tests/migrations/test_state.py ModelStateTests.test_sanity_index_name
func TestModelState_SanityIndexName(t *testing.T) {
	ms := modelState("app", "Model", nf("field", intField()))
	ms.Options.Indexes = []Index{{Fields: []IndexField{{Column: "field"}}}}

	err := ms.Validate()
	if err == nil {
		t.Fatal("expected an error for an index without a name")
	}
	const prefix = "indexes passed to ModelState require a name attribute."
	if !strings.HasPrefix(err.Error(), prefix) || !strings.HasSuffix(err.Error(), "doesn't have one") {
		t.Fatalf("error: got %q, want %q ... %q", err.Error(), prefix, "doesn't have one")
	}

	// The same check runs through CreateModel.
	state := NewProjectState()
	op := &CreateModel{Name: "Model", Table: "app_model",
		Fields:  Fields{nf("field", intField())},
		Options: Options{Indexes: []Index{{Fields: []IndexField{{Column: "field"}}}}}}
	if err := op.StateForwards("app", state); err == nil {
		t.Fatal("CreateModel accepted an index without a name")
	} else if _, ok := err.(*ValueError); !ok {
		t.Fatalf("CreateModel: expected ValueError, got %T", err)
	}
}

// TestModelState_DuplicateFields checks the duplicate-field check.
//
// django: db/migrations/state.py ModelState.__init__ (_check_for_duplicates)
func TestModelState_DuplicateFields(t *testing.T) {
	ms := modelState("app", "Model", nf("field", intField()), nf("field", charField(1)))
	err := ms.Validate()
	if err == nil {
		t.Fatal("expected an error for a duplicate field name")
	}
	if got, want := err.Error(), "found duplicate value field in CreateModel fields argument"; got != want {
		t.Fatalf("error:\n got %q\nwant %q", got, want)
	}
}

// TestModelState_FieldsImmutability checks that rendering a model state
// doesn't alter its internal fields.
//
// django: tests/migrations/test_state.py ModelStateTests.test_fields_immutability
func TestModelState_FieldsImmutability(t *testing.T) {
	state := NewProjectState()
	state.AddModel(modelState("app", "Model", nf("name", charField(1))))
	model := mustApps(t, state).MustModel("app", "Model")

	// Mutating the rendered field must not touch the state.
	model.Field("name").Field.Size = 255
	got, _ := mustModelState(t, state, "app", "Model").Fields.Get("name")
	if got.Size != 1 {
		t.Fatalf("rendering altered the state field: size %d, want 1", got.Size)
	}
}

// TestModelState_FieldsOrderingEquality checks that two model states with the
// same fields in a different order are equal.
//
// django: tests/migrations/test_state.py ModelStateTests.test_fields_ordering_equality
func TestModelState_FieldsOrderingEquality(t *testing.T) {
	state := modelState("migrations", "Tag",
		nf("id", autoField()),
		nf("name", charField(100)),
		nf("hidden", boolField()),
	)
	reordered := modelState("migrations", "Tag",
		nf("id", autoField()),
		// Purposely re-ordered.
		nf("hidden", boolField()),
		nf("name", charField(100)),
	)
	if !state.Equal(reordered) {
		t.Fatal("re-ordered model states are not equal")
	}
	if !reordered.Equal(state) {
		t.Fatal("model state equality is not symmetric")
	}

	different := modelState("migrations", "Tag",
		nf("id", autoField()),
		nf("hidden", boolField()),
		nf("name", charField(99)),
	)
	if state.Equal(different) {
		t.Fatal("model states with a different field are equal")
	}
}

// TestModelState_GetField checks ModelState.get_field.
//
// django: db/migrations/state.py ModelState.get_field
func TestModelState_GetField(t *testing.T) {
	ms := modelState("app", "Model", nf("name", charField(1)))
	if _, err := ms.GetField("name"); err != nil {
		t.Fatal(err)
	}
	_, err := ms.GetField("nope")
	if err == nil {
		t.Fatal("expected FieldDoesNotExist")
	}
	if got, want := err.Error(), "app.model has no field named 'nope'"; got != want {
		t.Fatalf("error:\n got %q\nwant %q", got, want)
	}
}
