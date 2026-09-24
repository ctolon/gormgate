package management

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gorm.io/gorm"
	"gorm.io/gorm/schema"

	"github.com/ctolon/gormgate/backends/base"
	m "github.com/ctolon/gormgate/migrations"
)

// oneOffNamer is gorm's own naming strategy with a single method answering
// differently, which is the shape a drift really has: a project that passes
// a strategy with one setting changed to gorm.Open and forgets to pass it
// to gormgate.
type oneOffNamer struct{ schema.Namer }

func (n oneOffNamer) IndexName(table, column string) string {
	return "ix_" + table + "_" + column
}

// delegatingNamer is a second type that answers every probe exactly as
// schema.NamingStrategy does, so that comparing by identity would call it a
// drift and comparing by behaviour does not.
type delegatingNamer struct{ inner schema.NamingStrategy }

func (n delegatingNamer) TableName(t string) string     { return n.inner.TableName(t) }
func (n delegatingNamer) SchemaName(t string) string    { return n.inner.SchemaName(t) }
func (n delegatingNamer) ColumnName(t, c string) string { return n.inner.ColumnName(t, c) }
func (n delegatingNamer) JoinTableName(t string) string { return n.inner.JoinTableName(t) }
func (n delegatingNamer) RelationshipFKName(r schema.Relationship) string {
	return n.inner.RelationshipFKName(r)
}
func (n delegatingNamer) CheckerName(t, c string) string { return n.inner.CheckerName(t, c) }
func (n delegatingNamer) IndexName(t, c string) string   { return n.inner.IndexName(t, c) }
func (n delegatingNamer) UniqueName(t, c string) string  { return n.inner.UniqueName(t, c) }

// TestNamerDriftReportsTheProbeThatDisagrees pins that the comparison is by
// behaviour and names the first input the two strategies answer differently,
// with both answers.
func TestNamerDriftReportsTheProbeThatDisagrees(t *testing.T) {
	ours := schema.NamingStrategy{}
	msg, ok := namerDrift("default", ours, oneOffNamer{Namer: ours})
	if !ok {
		t.Fatal("a strategy that renames indexes differently was not reported")
	}
	if msg.Level != LevelError || msg.ID != "gormgate.E002" || msg.Obj != "default" {
		t.Errorf("message = %+v", msg)
	}
	for _, want := range []string{`IndexName("posts", "title")`, `"idx_posts_title"`, `"ix_posts_title"`} {
		if !strings.Contains(msg.Msg, want) {
			t.Errorf("message %q does not contain %q", msg.Msg, want)
		}
	}
	// Only the probes before the one that disagrees were compared, so no
	// other call is named.
	if strings.Contains(msg.Msg, "UniqueName") {
		t.Errorf("the message names a probe after the first disagreement: %q", msg.Msg)
	}
	if msg.Hint == "" {
		t.Error("the drift is reported without a hint")
	}
}

// TestNamerDriftAcceptsAnotherTypeThatAgrees pins that two strategies that
// are not the same value, and not even the same type, are accepted when
// they answer every probe alike.
func TestNamerDriftAcceptsAnotherTypeThatAgrees(t *testing.T) {
	if msg, ok := namerDrift("default", schema.NamingStrategy{}, delegatingNamer{}); ok {
		t.Errorf("two strategies that agree on every probe were reported as a drift: %s", msg)
	}
	// The same settings in two copies of one type agree too.
	a := schema.NamingStrategy{TablePrefix: "t_", SingularTable: true}
	b := schema.NamingStrategy{TablePrefix: "t_", SingularTable: true}
	if msg, ok := namerDrift("default", a, b); ok {
		t.Errorf("two equal strategies were reported as a drift: %s", msg)
	}
	// A settings difference that shows in the table name is a drift.
	if _, ok := namerDrift("default", a, schema.NamingStrategy{SingularTable: true}); !ok {
		t.Error("a different TablePrefix was not reported")
	}
}

// TestNamerDriftWithoutAConnectionNamer says nothing when the connection
// carries no naming strategy at all; there is nothing to compare with.
func TestNamerDriftWithoutAConnectionNamer(t *testing.T) {
	if _, ok := namerDrift("default", schema.NamingStrategy{}, nil); ok {
		t.Error("a connection without a naming strategy was reported as a drift")
	}
}

// compatModel renders one model state into the *m.Model the checks walk.
func compatModel(t *testing.T, ms *m.ModelState) *m.Model {
	t.Helper()
	st := m.NewProjectState()
	st.Models[ms.Key()] = ms
	apps, err := st.Apps()
	if err != nil {
		t.Fatalf("rendering %s: %v", ms.Name, err)
	}
	return apps.MustModel(ms.App, ms.Name)
}

// clickhouseLike is the Features value of a backend whose editor refuses
// what it cannot express instead of skipping it: no foreign keys, no unique
// constraints, and only indexes of an explicit type.
var clickhouseLike = base.Features{
	RefusesUnsupportedObjects: true,
	NoPlainIndexes:            true,
	SupportsComments:          true,
}

// skippingLike is a backend that has none of those either, but whose editor
// leaves the objects out silently, which is what every backend but
// ClickHouse does.
var skippingLike = base.Features{SupportsComments: true}

// refusingModel declares one of everything such a backend cannot do.
func refusingModel() *m.ModelState {
	return &m.ModelState{App: "blog", Name: "Post", Table: "posts", Fields: m.Fields{
		{Name: "id", Field: m.Field{Type: m.Int, PrimaryKey: true}},
		{Name: "slug", Field: m.Field{Type: m.String, Unique: true}},
	}, Options: m.Options{
		UniqueTogether: [][]string{{"slug", "id"}},
		Indexes:        []m.Index{{Name: "idx_posts_slug", Fields: m.Columns("slug")}},
		Constraints: []m.Constraint{
			&m.UniqueConstraint{Name: "uniq_posts_slug", Fields: []string{"slug"}, Condition: "id > 0"},
			&m.ForeignKeyConstraint{Name: "fk_posts_author", Fields: []string{"id"}, To: "blog.Post", ToFields: []string{"id"}},
		},
	}}
}

// TestBackendRefusesHard pins that a backend whose editor raises
// NotSupportedError is reported as an error, once per thing it refuses.
func TestBackendRefusesHard(t *testing.T) {
	b := &base.Backend{Vendor: "clickhouse", DisplayName: "ClickHouse", Features: clickhouseLike}
	msgs := modelBackendMessages("default", b, compatModel(t, refusingModel()))
	if len(msgs) == 0 {
		t.Fatal("a backend that refuses everything reported nothing")
	}
	for _, msg := range msgs {
		if msg.Level != LevelError || msg.ID != "gormgate.E003" || msg.Obj != "blog.Post" {
			t.Errorf("message = %+v", msg)
		}
		if !strings.Contains(msg.Msg, "database 'default' (ClickHouse) refuses this model") {
			t.Errorf("message %q does not say the database refuses the model", msg.Msg)
		}
	}
	joined := strings.Join(msgStrings(msgs), "\n")
	for _, want := range []string{
		`has no unique constraints, but column "slug" is unique`,
		`has no unique constraints, but the model declares unique_together (slug, id)`,
		`has no unique constraints, but constraint "uniq_posts_slug" is one`,
		`has no foreign keys, but constraint "fk_posts_author" is one`,
		`only has indexes of an explicit type, but index "idx_posts_slug" sets no Index.Type`,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("the refusals do not mention %q:\n%s", want, joined)
		}
	}
}

// TestBackendSkipsSilently pins that the same model on a backend that skips
// instead of refusing is reported as warnings, worded so that the user
// knows the object will be absent rather than the command failing.
func TestBackendSkipsSilently(t *testing.T) {
	b := &base.Backend{Vendor: "mysql", DisplayName: "MySQL", Features: skippingLike}
	msgs := modelBackendMessages("default", b, compatModel(t, refusingModel()))
	if len(msgs) == 0 {
		t.Fatal("a backend that skips everything reported nothing")
	}
	for _, msg := range msgs {
		if msg.Level != LevelWarning || msg.ID != "gormgate.W001" || msg.Obj != "blog.Post" {
			t.Errorf("message = %+v", msg)
		}
		if !strings.Contains(msg.Msg, "will not be created") {
			t.Errorf("message %q does not say the object will be absent", msg.Msg)
		}
		if !strings.Contains(msg.Hint, "migrate succeeds") {
			t.Errorf("hint %q does not say the command still succeeds", msg.Hint)
		}
	}
	joined := strings.Join(msgStrings(msgs), "\n")
	for _, want := range []string{
		`unique constraint "uniq_posts_slug" will not be created: MySQL has no partial unique constraints`,
		`foreign key constraint "fk_posts_author" will not be created`,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("the skips do not mention %q:\n%s", want, joined)
		}
	}
	// A field-level unique and a unique_together are *not* among them: the
	// base editor renders both whatever SupportsUniqueConstraints says.
	if strings.Contains(joined, "unique_together") {
		t.Errorf("unique_together was reported as silently skipped:\n%s", joined)
	}
}

// TestBackendAcceptsEverything says nothing about a model a backend can
// create in full.
func TestBackendAcceptsEverything(t *testing.T) {
	f := base.Features{
		SupportsForeignKeys: true, SupportsUniqueConstraints: true, SupportsComments: true,
		SupportsPartialIndexes: true, SupportsExpressionIndexes: true, SupportsCoveringIndexes: true,
		SupportsDeferrableUniqueConstraints: true, SupportsNullsDistinctUniqueConstraints: true,
		SupportsIndexColumnOrdering: true, SupportsTableCheckConstraints: true,
	}
	b := &base.Backend{Vendor: "postgresql", DisplayName: "PostgreSQL", Features: f}
	if msgs := modelBackendMessages("default", b, compatModel(t, refusingModel())); len(msgs) > 0 {
		t.Errorf("a backend that can do everything reported %s", strings.Join(msgStrings(msgs), "\n"))
	}
}

// TestBackendAlterLimit reports the clustered integer primary key TiDB will
// never let the project retype, as a warning of its own.
func TestBackendAlterLimit(t *testing.T) {
	ms := &m.ModelState{App: "blog", Name: "Post", Table: "posts", Fields: m.Fields{
		{Name: "id", Field: m.Field{Type: m.Int, PrimaryKey: true}},
	}}
	b := &base.Backend{Vendor: "tidb", DisplayName: "TiDB", Features: base.Features{
		SupportsForeignKeys: true, SupportsUniqueConstraints: true, SupportsComments: true,
		SupportsTableCheckConstraints: true, CannotAlterIntegerPrimaryKeyType: true,
	}}
	msgs := modelBackendMessages("default", b, compatModel(t, ms))
	if len(msgs) != 1 || msgs[0].ID != "gormgate.W002" || msgs[0].Level != LevelWarning {
		t.Fatalf("messages = %s", strings.Join(msgStrings(msgs), "\n"))
	}
	if !strings.Contains(msgs[0].Msg, `the type of primary key column "id" can never be changed`) {
		t.Errorf("message = %q", msgs[0].Msg)
	}
	b.Features.CannotAlterIntegerPrimaryKeyType = false
	if msgs := modelBackendMessages("default", b, compatModel(t, ms)); len(msgs) != 0 {
		t.Errorf("a backend that can retype a primary key reported %s", strings.Join(msgStrings(msgs), "\n"))
	}
}

// TestOracleOnUpdateIsRefused pins the one hard refusal that is not
// ClickHouse's: a foreign key with an ON UPDATE action on a backend whose
// foreign keys have none.
func TestOracleOnUpdateIsRefused(t *testing.T) {
	ms := &m.ModelState{App: "blog", Name: "Post", Table: "posts", Fields: m.Fields{
		{Name: "id", Field: m.Field{Type: m.Int, PrimaryKey: true}},
	}, Options: m.Options{Constraints: []m.Constraint{
		&m.ForeignKeyConstraint{Name: "fk_posts_self", Fields: []string{"id"}, To: "blog.Post",
			ToFields: []string{"id"}, OnUpdate: m.Cascade},
	}}}
	b := &base.Backend{Vendor: "oracle", DisplayName: "Oracle", Features: base.Features{
		SupportsForeignKeys: true, SupportsUniqueConstraints: true, SupportsComments: true,
		SupportsTableCheckConstraints: true, NoForeignKeyOnUpdate: true,
	}}
	msgs := modelBackendMessages("default", b, compatModel(t, ms))
	if len(msgs) != 1 || msgs[0].ID != "gormgate.E003" {
		t.Fatalf("messages = %s", strings.Join(msgStrings(msgs), "\n"))
	}
	if !strings.Contains(msgs[0].Msg, `Oracle foreign keys have no ON UPDATE action, but constraint "fk_posts_self" declares OnUpdate "CASCADE"`) {
		t.Errorf("message = %q", msgs[0].Msg)
	}
}

func msgStrings(msgs []CheckMessage) []string {
	out := make([]string, len(msgs))
	for i, msg := range msgs {
		out[i] = msg.String()
	}
	return out
}

// inventoryProject builds a project with something for the inventory to
// find: a migrations directory with two migration files, a command
// directory and a database alias.
func inventoryProject(t *testing.T) (*Project, string) {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "blog", "migrations")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"migrations.go", "0001_initial.go", "0002_post_slug.go", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	p := testProject()
	p.Apps[0].MigrationsDir = dir
	p.CommandDir = filepath.Join(root, "cmd", "gorm-gate")
	p.Databases = map[string]func() (*gorm.DB, error){"default": nil}
	return p, root
}

// TestInventoryOnlyWhenTheTagIsNamed pins the rule that keeps a bare check
// byte-identical to Django's: the inventory is information, not a problem,
// so it is reported only when --tag compatibility asks for it.
func TestInventoryOnlyWhenTheTagIsNamed(t *testing.T) {
	p, _ := inventoryProject(t)
	ctx, _, _ := checkContext(p)
	if msgs := inventoryCheck(ctx, checkOptions{}); len(msgs) != 0 {
		t.Fatalf("a bare check reported the inventory: %s", strings.Join(msgStrings(msgs), "\n"))
	}
	if msgs := inventoryCheck(ctx, checkOptions{Tags: []string{TagModels}}); len(msgs) != 0 {
		t.Fatalf("--tag models reported the inventory: %s", strings.Join(msgStrings(msgs), "\n"))
	}
	msgs := inventoryCheck(ctx, checkOptions{Tags: []string{TagCompatibility}})
	if len(msgs) != 1 || msgs[0].Level != LevelInfo || msgs[0].ID != "gormgate.I001" {
		t.Fatalf("messages = %s", strings.Join(msgStrings(msgs), "\n"))
	}
	for _, want := range []string{
		"2 migration files",
		LinkFileName,
		"the 'gormgate_migrations' table of database 'default'",
	} {
		if !strings.Contains(msgs[0].Msg, want) {
			t.Errorf("the inventory does not mention %q:\n%s", want, msgs[0].Msg)
		}
	}
	if !strings.Contains(msgs[0].Hint, "plain gorm") {
		t.Errorf("the inventory does not say the models need no change: %q", msgs[0].Hint)
	}
}

// TestBareCheckStaysSilent is the invariant the parity harness compares
// byte for byte: a healthy project prints Django's footer on stdout and
// nothing else, whatever the compatibility checks would have to say.
func TestBareCheckStaysSilent(t *testing.T) {
	p, _ := inventoryProject(t)
	env, out, errOut := testEnv("")
	if code := runIn(env, p, "check"); code != 0 {
		t.Fatalf("exit status %d: %s", code, errOut.String())
	}
	if got, want := out.String(), "System check identified no issues (0 silenced).\n"; got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
	if errOut.Len() != 0 {
		t.Errorf("stderr = %q", errOut.String())
	}
}

// TestCompatibilityTagIsSilenceable pins that Settings.SilencedChecks
// silences the compatibility checks like any other.
func TestCompatibilityTagIsSilenceable(t *testing.T) {
	p, _ := inventoryProject(t)
	p.SilencedChecks = []string{"gormgate.I001"}
	env, out, errOut := testEnv("")
	code := runIn(env, p, "check", "--tag", "compatibility")
	if code != 0 {
		t.Fatalf("exit status %d: %s", code, errOut.String())
	}
	if got, want := out.String(), "System check identified no issues (1 silenced).\n"; got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
	if errOut.Len() != 0 {
		t.Errorf("stderr = %q", errOut.String())
	}
}

// TestCompatibilityTagReportsTheInventory runs the tag through the command
// and pins where the report lands: an info message is not an issue Django
// would fail on, so it is printed and the command still passes.
func TestCompatibilityTagReportsTheInventory(t *testing.T) {
	p, _ := inventoryProject(t)
	env, _, errOut := testEnv("")
	if code := runIn(env, p, "check", "--tag", "compatibility"); code != 0 {
		t.Fatalf("exit status %d: %s", code, errOut.String())
	}
	// The inventory is about the project, not a model, so it has no object
	// and renders as "?" -- the same as the other project-wide check.
	if !strings.Contains(errOut.String(), "INFOS:\n?: (gormgate.I001)") {
		t.Errorf("stderr = %q", errOut.String())
	}
	if !strings.Contains(errOut.String(), "System check identified 1 issue (0 silenced).") {
		t.Errorf("the footer does not count the inventory:\n%s", errOut.String())
	}
	// The hint must name only what was listed.
	if !strings.Contains(errOut.String(), "delete the files listed above and drop the table") {
		t.Errorf("the hint does not match the listing:\n%s", errOut.String())
	}
}

// TestInventoryHintWithoutFiles pins the case the wording used to get
// wrong: a project that has generated no migration owns only the table, so
// the hint must not tell the reader to delete files it did not list.
func TestInventoryHintWithoutFiles(t *testing.T) {
	p, _ := inventoryProject(t)
	for _, a := range p.Apps {
		a.MigrationsDir = ""
	}
	p.CommandDir = ""
	env, _, errOut := testEnv("")
	if code := runIn(env, p, "check", "--tag", "compatibility"); code != 0 {
		t.Fatalf("exit status %d: %s", code, errOut.String())
	}
	if strings.Contains(errOut.String(), "files listed above") {
		t.Errorf("the hint names files although none were listed:\n%s", errOut.String())
	}
	if !strings.Contains(errOut.String(), "drop the table, and they keep working") {
		t.Errorf("the hint does not name the table:\n%s", errOut.String())
	}
}
