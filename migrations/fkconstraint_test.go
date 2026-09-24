package migrations

import (
	"strings"
	"testing"
)

// The composite foreign key constraint has no Django counterpart, so these
// tests have no Django test to mirror: they pin gormgate's own behaviour.

func fkc() *ForeignKeyConstraint {
	return &ForeignKeyConstraint{
		Name:     "fk_stall_barn",
		Fields:   []string{"barn_tenant_id", "barn_code"},
		To:       "Barn",
		ToFields: []string{"tenant_id", "code"},
		OnDelete: Cascade,
	}
}

func stallState() *ModelState {
	return &ModelState{App: "tests", Name: "Stall", Table: "tests_stall",
		Fields: Fields{
			{Name: "id", Field: Field{Type: Int, Size: 64, PrimaryKey: true}},
			{Name: "barn_tenant_id", Field: Field{Type: Int, Size: 64, Null: true}},
			{Name: "barn_code", Field: Field{Type: String, Size: 20, Null: true}},
		},
		Options: Options{Constraints: []Constraint{fkc()}},
	}
}

func barnState() *ModelState {
	return &ModelState{App: "tests", Name: "Barn", Table: "tests_barn",
		Fields: Fields{
			{Name: "tenant_id", Field: Field{Type: Int, Size: 64, PrimaryKey: true}},
			{Name: "code", Field: Field{Type: String, Size: 20, PrimaryKey: true}},
		},
	}
}

func TestForeignKeyConstraint_CloneIsDeep(t *testing.T) {
	c := fkc()
	cp := c.cloneConstraint().(*ForeignKeyConstraint)
	if !ConstraintEqual(c, cp) {
		t.Fatal("a clone must compare equal")
	}
	cp.Fields[0] = "other"
	cp.ToFields[0] = "other"
	if c.Fields[0] != "barn_tenant_id" || c.ToFields[0] != "tenant_id" {
		t.Errorf("the clone shares its slices with the original: %+v", c)
	}
	if ConstraintEqual(c, cp) {
		t.Error("constraints over different columns must not compare equal")
	}
	// A constraint of another kind with the same name is a different thing.
	if ConstraintEqual(c, &UniqueConstraint{Name: c.Name, Fields: c.Fields}) {
		t.Error("a unique constraint must not equal a foreign key constraint")
	}
}

func TestForeignKeyConstraint_Deconstruct(t *testing.T) {
	kv := deconstructStruct(fkc())
	got := map[string]any{}
	var keys []string
	for _, x := range kv {
		got[x.Key] = x.Value
		keys = append(keys, x.Key)
	}
	// Only the non-zero attributes are listed, in declaration order.
	if want := "Name,Fields,To,ToFields,OnDelete"; strings.Join(keys, ",") != want {
		t.Errorf("deconstruction keys = %v, want %s", keys, want)
	}
	if got["To"] != "Barn" {
		t.Errorf("To = %v", got["To"])
	}
	if got["OnDelete"] != Cascade {
		t.Errorf("OnDelete = %v", got["OnDelete"])
	}
	// ConstraintEqualIgnoringNonDB must not lose a database attribute: a
	// foreign key has no state-only one, so it is plain equality.
	other := fkc()
	other.OnDelete = SetNull
	if ConstraintEqualIgnoringNonDB(fkc(), other) {
		t.Error("a different OnDelete must be a database difference")
	}
}

func TestForeignKeyConstraint_StateQualifiesAndRepoints(t *testing.T) {
	s := NewProjectState()
	s.AddModel(barnState())
	s.AddModel(stallState())
	stall := s.Models[ModelKey{"tests", "stall"}]
	if got := stall.Options.Constraints[0].(*ForeignKeyConstraint).To; got != "tests.Barn" {
		t.Fatalf("AddModel did not qualify the target: %q", got)
	}
	// Renaming the referenced model repoints the constraint.
	s.RenameModel("tests", "Barn", "Stable")
	if got := stall.Options.Constraints[0].(*ForeignKeyConstraint).To; got != "tests.Stable" {
		t.Errorf("after RenameModel To = %q", got)
	}
	// Renaming a referenced column rewrites ToFields; renaming a local one
	// rewrites Fields.
	if err := s.RenameField("tests", "Stable", "code", "label"); err != nil {
		t.Fatal(err)
	}
	if err := s.RenameField("tests", "Stall", "barn_code", "stable_label"); err != nil {
		t.Fatal(err)
	}
	c := stall.Options.Constraints[0].(*ForeignKeyConstraint)
	if c.ToFields[1] != "label" {
		t.Errorf("ToFields = %v", c.ToFields)
	}
	if c.Fields[1] != "stable_label" {
		t.Errorf("Fields = %v", c.Fields)
	}
}

func TestForeignKeyConstraint_AddAlterRemove(t *testing.T) {
	s := NewProjectState()
	s.AddModel(barnState())
	bare := stallState()
	bare.Options.Constraints = nil
	s.AddModel(bare)
	stall := s.Models[ModelKey{"tests", "stall"}]

	s.AddConstraint("tests", "Stall", fkc())
	c, err := stall.GetConstraintByName("fk_stall_barn")
	if err != nil {
		t.Fatal(err)
	}
	if got := c.(*ForeignKeyConstraint).To; got != "tests.Barn" {
		t.Errorf("AddConstraint did not qualify the target: %q", got)
	}
	// The state owns its copy: mutating the argument must not reach it.
	arg := fkc()
	s.AlterConstraint("tests", "Stall", "fk_stall_barn", arg)
	arg.Fields[0] = "mutated"
	c, _ = stall.GetConstraintByName("fk_stall_barn")
	if c.(*ForeignKeyConstraint).Fields[0] != "barn_tenant_id" {
		t.Error("AlterConstraint stored the caller's slice")
	}
	s.RemoveConstraint("tests", "Stall", "fk_stall_barn")
	if len(stall.Options.Constraints) != 0 {
		t.Errorf("after RemoveConstraint: %v", stall.Options.Constraints)
	}
}

func TestForeignKeyConstraint_RendersWithItsTarget(t *testing.T) {
	s := NewProjectState()
	s.AddModel(barnState())
	s.AddModel(stallState())
	apps, err := s.Apps()
	if err != nil {
		t.Fatal(err)
	}
	stall := apps.MustModel("tests", "Stall")
	remote, err := stall.RelatedModel("tests.Barn")
	if err != nil {
		t.Fatal(err)
	}
	if remote.Table != "tests_barn" {
		t.Errorf("RelatedModel = %s", remote.Table)
	}
	if _, err := stall.RelatedModel("tests.Nothing"); err == nil {
		t.Error("an unknown target must be an error")
	}
}

func TestForeignKeyConstraint_UnresolvableTargetIsReported(t *testing.T) {
	s := NewProjectState()
	s.AddModel(stallState())
	_, err := s.Apps()
	if err == nil {
		t.Fatal("a constraint pointing at a model that does not exist must not render")
	}
	if !strings.Contains(err.Error(), "fk_stall_barn") || !strings.Contains(err.Error(), "tests.barn") {
		t.Errorf("error = %v", err)
	}
	// A column the target does not have is reported too.
	s = NewProjectState()
	s.AddModel(barnState())
	bad := stallState()
	bad.Options.Constraints = []Constraint{&ForeignKeyConstraint{
		Name: "fk_stall_barn", Fields: []string{"barn_tenant_id", "barn_code"},
		To: "Barn", ToFields: []string{"tenant_id", "nope"},
	}}
	s.AddModel(bad)
	if _, err := s.Apps(); err == nil || !strings.Contains(err.Error(), "tests.barn.nope") {
		t.Errorf("error = %v", err)
	}
}

func TestForeignKeyConstraint_Validate(t *testing.T) {
	for _, tc := range []struct {
		name string
		c    *ForeignKeyConstraint
		want string
	}{
		{"no fields", &ForeignKeyConstraint{Name: "fk", To: "Barn"}, "has no fields"},
		{"length mismatch", &ForeignKeyConstraint{Name: "fk", Fields: []string{"a", "b"}, To: "Barn", ToFields: []string{"x"}},
			"constrains 2 field(s) but references 1"},
		{"no target", &ForeignKeyConstraint{Name: "fk", Fields: []string{"a"}, ToFields: []string{"x"}}, "has no target model"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ms := stallState()
			ms.Options.Constraints = []Constraint{tc.c}
			err := ms.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Validate = %v, want one containing %q", err, tc.want)
			}
		})
	}
}
