package fromgorm

import (
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	m "github.com/ctolon/gormgate/migrations"
)

type User struct {
	ID        uint
	Name      string `gorm:"size:100;not null;index"`
	Email     string `gorm:"uniqueIndex:idx_email"`
	Age       int8   `gorm:"default:18;check:age >= 0"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt
	Posts     []Post
	Languages []Language `gorm:"many2many:user_languages;"`
}

type Post struct {
	ID       uint
	Title    string `gorm:"size:200"`
	UserID   uint
	Body     string  `gorm:"type:text;comment:the body"`
	Score    float64 `gorm:"precision:10;scale:2"`
	Editor   *User   `gorm:"constraint:OnDelete:SET NULL;"`
	EditorID *uint
}

type Language struct {
	Code string `gorm:"primaryKey;size:5"`
	Name string `gorm:"default:(-)"`
}

func TestProjectState(t *testing.T) {
	st, err := ProjectState([]App{{Label: "blog", Models: []any{&User{}, &Post{}, &Language{}}}}, Config{})
	if err != nil {
		t.Fatal(err)
	}
	keys := st.SortedKeys()
	if len(keys) != 4 {
		t.Fatalf("models: %v", keys)
	}
	u := st.Models[m.ModelKey{App: "blog", Model: "user"}]
	if u.Table != "users" {
		t.Errorf("table %q", u.Table)
	}
	if got := u.Fields.Names(); len(got) != 7 || got[0] != "id" || got[6] != "deleted_at" {
		t.Errorf("fields %v", got)
	}
	id, _ := u.Fields.Get("id")
	if !id.PrimaryKey || !id.AutoIncrement || id.Type != m.Uint || id.Size != 64 {
		t.Errorf("id %+v", id)
	}
	name, _ := u.Fields.Get("name")
	if name.Null || name.Size != 100 || name.Tags["INDEX"] != "INDEX" {
		t.Errorf("name %+v", name)
	}
	age, _ := u.Fields.Get("age")
	if age.DBDefault == nil || age.DBDefault.Value != int64(18) || age.Size != 8 {
		t.Errorf("age %+v", age)
	}
	created, _ := u.Fields.Get("created_at")
	if !created.AutoNowAdd || created.Type != m.Time {
		t.Errorf("created %+v", created)
	}
	var ixNames []string
	for _, ix := range u.Options.Indexes {
		ixNames = append(ixNames, ix.Name)
	}
	if len(ixNames) != 2 || ixNames[0] != "idx_users_name" || ixNames[1] != "idx_email" {
		t.Errorf("indexes %v", ixNames)
	}
	if len(u.Options.Constraints) != 1 || u.Options.Constraints[0].ConstraintName() != "chk_users_age" {
		t.Errorf("constraints %#v", u.Options.Constraints)
	}
	p := st.Models[m.ModelKey{App: "blog", Model: "post"}]
	uid, _ := p.Fields.Get("user_id")
	if uid.ForeignKey == nil || uid.ForeignKey.To != "blog.user" || uid.ForeignKey.Name != "fk_users_posts" {
		t.Errorf("user_id fk %+v", uid.ForeignKey)
	}
	eid, _ := p.Fields.Get("editor_id")
	if eid.ForeignKey == nil || eid.ForeignKey.OnDelete != "SET NULL" || eid.ForeignKey.Name != "fk_posts_editor" {
		t.Errorf("editor_id fk %+v", eid.ForeignKey)
	}
	body, _ := p.Fields.Get("body")
	if body.Type != "text" || body.Comment != "the body" {
		t.Errorf("body %+v", body)
	}
	jt := st.Models[m.ModelKey{App: "blog", Model: "user_languages"}]
	if jt == nil || !jt.Options.AutoCreated || jt.Table != "user_languages" {
		t.Fatalf("join table %+v", jt)
	}
	if got := jt.Fields.Names(); len(got) != 2 || got[0] != "user_id" || got[1] != "language_code" {
		t.Errorf("join fields %v", got)
	}
	lc, _ := jt.Fields.Get("language_code")
	if !lc.PrimaryKey || lc.ForeignKey == nil || lc.ForeignKey.To != "blog.language" || lc.Size != 5 {
		t.Errorf("language_code %+v %+v", lc, lc.ForeignKey)
	}
	lang := st.Models[m.ModelKey{App: "blog", Model: "language"}]
	ln, _ := lang.Fields.Get("name")
	if ln.DBDefault != nil {
		t.Errorf("default (-) must not produce a db default: %+v", ln.DBDefault)
	}
	if _, err := st.Apps(); err != nil {
		t.Fatal(err)
	}
}

// metaModel declares Meta options from slices it keeps, so a test can see
// whether the state shares them.
type metaModel struct {
	ID    uint
	Title string `gorm:"size:200"`
	Slug  string `gorm:"size:200"`
}

var metaModelMeta = m.Meta{
	UniqueTogether: [][]string{{"title", "slug"}},
	Indexes:        []m.Index{{Name: "ix_meta_title", Fields: m.Columns("title")}},
}

func (metaModel) MigrationMeta() m.Meta { return metaModelMeta }

// TestMetaOptionsAreCloned checks that the state does not share the slices a
// model's MigrationMeta hands out. ProjectState.RenameField rewrites
// unique_together and index entries in place, which would otherwise reach
// back into the application's own value.
func TestMetaOptionsAreCloned(t *testing.T) {
	st, err := ProjectState([]App{{Label: "blog", Models: []any{&metaModel{}}}}, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.RenameField("blog", "metaModel", "slug", "permalink"); err != nil {
		t.Fatal(err)
	}
	if got := metaModelMeta.UniqueTogether[0][1]; got != "slug" {
		t.Errorf("MigrationMeta's unique_together was rewritten: %q", got)
	}
	if got := metaModelMeta.Indexes[0].Fields[0].Column; got != "title" {
		t.Errorf("MigrationMeta's index was rewritten: %q", got)
	}
}

// tableOwner and tableView share a table. The second is unmanaged, which is
// how a database view, or the nearest thing to a Django proxy model, is
// mapped: gormgate tracks it so that migrations know it exists, but never
// creates or alters its table.
type tableOwner struct {
	ID   uint   `gorm:"primaryKey"`
	Name string `gorm:"size:50"`
}

func (tableOwner) TableName() string { return "shared_table" }

type tableView struct {
	ID uint `gorm:"primaryKey"`
}

func (tableView) TableName() string { return "shared_table" }

func (tableView) MigrationMeta() m.Meta { return m.Meta{Managed: m.Ptr(false)} }

type secondOwner struct {
	ID uint `gorm:"primaryKey"`
}

func (secondOwner) TableName() string { return "shared_table" }

// TestUnmanagedModelMayShareATable pins the rule Django applies: only
// managed models own a table, so an unmanaged one may name a table another
// model already owns, while two managed models may not.
func TestUnmanagedModelMayShareATable(t *testing.T) {
	t.Run("unmanaged is allowed", func(t *testing.T) {
		state, err := ProjectState([]App{{Label: "app", Models: []any{&tableOwner{}, &tableView{}}}}, Config{})
		if err != nil {
			t.Fatalf("an unmanaged model sharing a table must be accepted: %v", err)
		}
		view := state.Models[m.ModelKey{App: "app", Model: "tableview"}]
		if view == nil {
			t.Fatal("the unmanaged model is missing from the state")
		}
		if view.Options.IsManaged() {
			t.Error("the unmanaged model came back managed")
		}
	})

	t.Run("two managed models are not", func(t *testing.T) {
		_, err := ProjectState([]App{{Label: "app", Models: []any{&tableOwner{}, &secondOwner{}}}}, Config{})
		if err == nil {
			t.Fatal("two managed models sharing a table must be rejected")
		}
		if !strings.Contains(err.Error(), "use the same table 'shared_table'") {
			t.Errorf("error does not name the clash: %v", err)
		}
	})
}

// cfkBarn and cfkStall are gorm's own composite foreign key: the child
// carries both columns of the parent's composite primary key, and gorm's
// AutoMigrate builds one table-level FOREIGN KEY over the pair.
type cfkBarn struct {
	TenantID uint   `gorm:"primaryKey"`
	Code     string `gorm:"primaryKey;size:20"`
	Label    string `gorm:"size:50"`
}

func (cfkBarn) TableName() string { return "cfk_barns" }

type cfkStall struct {
	ID           uint `gorm:"primaryKey"`
	BarnTenantID uint
	BarnCode     string  `gorm:"size:20"`
	Barn         cfkBarn `gorm:"foreignKey:BarnTenantID,BarnCode;references:TenantID,Code;constraint:OnDelete:CASCADE"`
}

func (cfkStall) TableName() string { return "cfk_stalls" }

func TestCompositeForeignKey(t *testing.T) {
	st, err := ProjectState([]App{{Label: "barnyard", Models: []any{&cfkBarn{}, &cfkStall{}}}}, Config{})
	if err != nil {
		t.Fatal(err)
	}
	stall := st.Models[m.ModelKey{App: "barnyard", Model: "cfkstall"}]
	if stall == nil {
		t.Fatal("the child model is missing")
	}
	// The key spans two columns, so it is not on a field.
	for _, f := range stall.Fields {
		if f.Field.ForeignKey != nil {
			t.Errorf("field %s carries a foreign key: %+v", f.Name, f.Field.ForeignKey)
		}
	}
	if len(stall.Options.Constraints) != 1 {
		t.Fatalf("constraints = %#v", stall.Options.Constraints)
	}
	fk, ok := stall.Options.Constraints[0].(*m.ForeignKeyConstraint)
	if !ok {
		t.Fatalf("constraint is %T", stall.Options.Constraints[0])
	}
	// The name is gorm's own, so the DDL matches AutoMigrate's byte for byte.
	if fk.Name != "fk_cfk_stalls_barn" {
		t.Errorf("name = %q", fk.Name)
	}
	if got := strings.Join(fk.Fields, ","); got != "barn_tenant_id,barn_code" {
		t.Errorf("fields = %q", got)
	}
	if fk.To != "barnyard.cfkbarn" {
		t.Errorf("to = %q", fk.To)
	}
	if got := strings.Join(fk.ToFields, ","); got != "tenant_id,code" {
		t.Errorf("to fields = %q", got)
	}
	if fk.OnDelete != m.Cascade {
		t.Errorf("on delete = %q", fk.OnDelete)
	}
	if _, err := st.Apps(); err != nil {
		t.Fatal(err)
	}
}
