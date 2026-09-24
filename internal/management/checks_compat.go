package management

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"gorm.io/gorm/schema"

	"github.com/ctolon/gormgate/backends/base"
	"github.com/ctolon/gormgate/internal/recorder"
	m "github.com/ctolon/gormgate/migrations"
)

// compatibilityChecks is the whole compatibility tag.
//
// The per-database parts need a connection, so -- exactly like the
// database checks -- they only run against the aliases --database names; a
// bare check never opens a connection.  A connection that cannot be opened
// is not reported here: databaseChecks already reports it as gormgate.E001,
// and reporting it twice would print it twice.
//
// There is no Django function behind this: Django's checks framework has a
// compatibility tag, but no check of this kind to port.
func compatibilityChecks(c *Context, o checkOptions) []CheckMessage {
	var out []CheckMessage
	for _, alias := range o.Databases {
		conn, err := c.Project.Conn(alias)
		if err != nil {
			continue
		}
		if msg, ok := namerDriftCheck(c.Project, conn); ok {
			out = append(out, msg)
		}
		out = append(out, backendRefusalChecks(c.Project, conn, o.AppLabels)...)
	}
	out = append(out, inventoryCheck(c, o)...)
	return out
}

// namerProbe is one input the two naming strategies are compared on: a
// label naming the call, and the call itself.
type namerProbe struct {
	call string
	run  func(schema.Namer) string
}

// namerProbes cover every method of schema.Namer that gormgate's migrations
// depend on: the table of a model, the column of a field, the join table of
// a many2many, and the generated names of a foreign key, a check
// constraint, an index and a unique constraint.  A naming strategy that
// answers all of them the way the application's does names every object a
// migration touches the way the application expects.
//
// The inputs are spelled in both a single-word and a multi-word form,
// because the two differ in exactly the settings that usually drift
// (SingularTable, NoLowerCase, TablePrefix, NameReplacer).
var namerProbes = []namerProbe{
	{`TableName("Post")`, func(n schema.Namer) string { return n.TableName("Post") }},
	{`TableName("UserProfile")`, func(n schema.Namer) string { return n.TableName("UserProfile") }},
	{`ColumnName("posts", "CreatedAt")`, func(n schema.Namer) string { return n.ColumnName("posts", "CreatedAt") }},
	{`JoinTableName("PostTags")`, func(n schema.Namer) string { return n.JoinTableName("PostTags") }},
	{`RelationshipFKName(posts.Author)`, func(n schema.Namer) string {
		return n.RelationshipFKName(schema.Relationship{Name: "Author", Schema: &schema.Schema{Table: "posts"}})
	}},
	{`CheckerName("posts", "rating")`, func(n schema.Namer) string { return n.CheckerName("posts", "rating") }},
	{`IndexName("posts", "title")`, func(n schema.Namer) string { return n.IndexName("posts", "title") }},
	{`UniqueName("posts", "slug")`, func(n schema.Namer) string { return n.UniqueName("posts", "slug") }},
}

// probeNamer runs one probe, turning a panic into a result of its own: a
// naming strategy that cannot answer a probe the other one answers has
// already drifted, and a check must not take the command down with it.
func probeNamer(p namerProbe, n schema.Namer) (out string) {
	defer func() {
		if r := recover(); r != nil {
			out = fmt.Sprintf("<panic: %v>", r)
		}
	}()
	return p.run(n)
}

// projectNamer is the naming strategy the project's models are read with,
// with the default fromgorm applies made explicit so that a project leaving
// Settings.NamingStrategy unset is compared against what it really uses.
func projectNamer(p *Project) schema.Namer {
	if p.Namer == nil {
		return schema.NamingStrategy{}
	}
	return p.Namer
}

// namerDriftCheck compares Settings.NamingStrategy with the naming strategy
// of the connection's gorm handle, by behaviour rather than by identity:
// two strategies of different types, or two copies of one type, are the
// same for gormgate exactly when they answer every probe alike.
//
// The consequence of a drift is not a failed migration but a schema the
// application cannot see -- gormgate creates the tables, columns and
// constraints under names nothing queries -- so it is an error.
//
// There is no Django function behind this; see TagCompatibility.
func namerDriftCheck(p *Project, conn *base.Conn) (CheckMessage, bool) {
	return namerDrift(conn.Alias(), projectNamer(p), conn.DB().NamingStrategy)
}

// namerDrift is namerDriftCheck over two naming strategies, split out so
// that it can be exercised without a server.
func namerDrift(alias string, ours, theirs schema.Namer) (CheckMessage, bool) {
	if theirs == nil {
		return CheckMessage{}, false
	}
	for _, probe := range namerProbes {
		got, want := probeNamer(probe, ours), probeNamer(probe, theirs)
		if got == want {
			continue
		}
		return CheckMessage{
			Level: LevelError,
			Obj:   alias,
			ID:    "gormgate.E002",
			Msg: fmt.Sprintf("Settings.NamingStrategy is not the naming strategy of the gorm handle of database '%s': "+
				"%s is %q for Settings.NamingStrategy and %q for the connection", alias, probe.call, got, want),
			Hint: "Pass the same schema.Namer to gorm.Open and to gormgate.Settings.NamingStrategy. " +
				"Otherwise gormgate migrates tables, columns and constraints under names the application never uses.",
		}, true
	}
	return CheckMessage{}, false
}

// backendRefusalChecks reports, for one database, what the models declare
// and that database will not do.
//
// The two outcomes are kept apart because the code keeps them apart:
//
//   - Where Features.RefusesUnsupportedObjects is set, the backend's own
//     schema editor raises migrations.NotSupportedError for an object it
//     cannot express and the migration stops (backends/clickhouse: checkField,
//     checkModel, checkConstraint and CreateIndexSQL).  Oracle does the same
//     for a foreign key with an ON UPDATE action, which
//     Features.NoForeignKeyOnUpdate marks (backends/oracle/schema.go).  That
//     is an error.
//
//   - Everywhere else the base editor consults the same Features and skips
//     the object silently, so the schema simply does not have it
//     (backends/base/editor.go: TableSQL, CreateModel, ConstraintSQL,
//     uniqueSupported, deferModelIndexes, CreateIndexSQL).  That is a
//     warning: the command succeeds and the object is absent.
//
// A model the backend refuses outright is not also reported for what would
// have been skipped inside it: the table is not created at all.
//
// There is no Django function behind this; see TagCompatibility.
func backendRefusalChecks(p *Project, conn *base.Conn, labels []string) []CheckMessage {
	st, err := p.CurrentState()
	if err != nil {
		// A project whose models do not convert is reported by the model
		// checks; there is nothing to compare against a backend here.
		return nil
	}
	apps, err := st.Apps()
	if err != nil {
		return nil
	}
	var out []CheckMessage
	for _, model := range apps.Models() {
		if !model.Managed() {
			continue
		}
		if len(labels) > 0 && !slices.Contains(labels, model.App) {
			continue
		}
		if !conn.AllowMigrate(model.App, m.Hints{Model: model, ModelName: strings.ToLower(model.Name)}) {
			continue
		}
		out = append(out, modelBackendMessages(conn.Alias(), conn.Backend, model)...)
	}
	return out
}

// modelBackendMessages is backendRefusalChecks for one model, split out so
// that it can be exercised against a Features value without a server.
func modelBackendMessages(alias string, b *base.Backend, model *m.Model) []CheckMessage {
	var out []CheckMessage
	where := fmt.Sprintf("database '%s' (%s)", alias, b.DisplayName)
	if refusals := backendRefuses(&b.Features, b.DisplayName, model); len(refusals) > 0 {
		for _, why := range refusals {
			out = append(out, CheckMessage{
				Level: LevelError,
				Obj:   model.Label(),
				ID:    "gormgate.E003",
				Msg:   fmt.Sprintf("%s refuses this model: %s", where, why),
				Hint:  "The migration stops with this error. Change the model, or keep it off this database with a router.",
			})
		}
		return out
	}
	for _, what := range backendSkips(&b.Features, b.DisplayName, model) {
		out = append(out, CheckMessage{
			Level: LevelWarning,
			Obj:   model.Label(),
			ID:    "gormgate.W001",
			Msg:   fmt.Sprintf("%s: %s", where, what),
			Hint:  "migrate succeeds; the object is simply absent from the schema of this database.",
		})
	}
	if what, ok := backendAlterLimit(b, model); ok {
		out = append(out, CheckMessage{
			Level: LevelWarning,
			Obj:   model.Label(),
			ID:    "gormgate.W002",
			Msg:   fmt.Sprintf("%s: %s", where, what),
			Hint:  "The model migrates as it stands; a later migration that makes this change is what fails.",
		})
	}
	return out
}

// backendRefuses lists what this backend's schema editor raises a
// migrations.NotSupportedError for, given what the model declares.
func backendRefuses(f *base.Features, name string, model *m.Model) []string {
	var out []string
	opts := model.Options()
	for _, fld := range model.Fields {
		fk := fld.Field.ForeignKey
		// backends/oracle/schema.go: an ON UPDATE action is refused rather
		// than dropped, because gormgate will not emulate it with a trigger.
		if fk != nil && fk.OnUpdate != "" && f.NoForeignKeyOnUpdate {
			out = append(out, fmt.Sprintf("%s foreign keys have no ON UPDATE action, but column %q declares OnUpdate %q",
				name, fld.Column, fk.OnUpdate))
		}
		if !f.RefusesUnsupportedObjects {
			continue
		}
		if fk != nil && !f.SupportsForeignKeys {
			out = append(out, fmt.Sprintf("%s has no foreign keys, but column %q declares one", name, fld.Column))
		}
		if fld.Field.Unique && !fld.Field.PrimaryKey && !f.SupportsUniqueConstraints {
			out = append(out, fmt.Sprintf("%s has no unique constraints, but column %q is unique", name, fld.Column))
		}
	}
	for _, c := range opts.Constraints {
		if x, ok := c.(*m.ForeignKeyConstraint); ok && x.OnUpdate != "" && f.NoForeignKeyOnUpdate {
			out = append(out, fmt.Sprintf("%s foreign keys have no ON UPDATE action, but constraint %q declares OnUpdate %q",
				name, x.Name, x.OnUpdate))
		}
	}
	if !f.RefusesUnsupportedObjects {
		return out
	}
	if !f.SupportsUniqueConstraints {
		for _, ut := range opts.UniqueTogether {
			out = append(out, fmt.Sprintf("%s has no unique constraints, but the model declares unique_together (%s)",
				name, strings.Join(ut, ", ")))
		}
	}
	for _, c := range opts.Constraints {
		switch x := c.(type) {
		case *m.UniqueConstraint:
			if !f.SupportsUniqueConstraints {
				out = append(out, fmt.Sprintf("%s has no unique constraints, but constraint %q is one", name, x.Name))
			}
		case *m.ForeignKeyConstraint:
			if !f.SupportsForeignKeys {
				out = append(out, fmt.Sprintf("%s has no foreign keys, but constraint %q is one", name, x.Name))
			}
		}
	}
	for _, ix := range opts.Indexes {
		out = append(out, indexRefusals(f, name, ix)...)
	}
	return out
}

// indexRefusals is backendRefuses for one index, and follows
// backends/clickhouse/clickhouse.go CreateIndexSQL case by case.
func indexRefusals(f *base.Features, name string, ix m.Index) []string {
	var out []string
	if ix.Unique && !f.SupportsUniqueConstraints {
		out = append(out, fmt.Sprintf("%s has no unique indexes, but index %q is unique", name, ix.Name))
	}
	if f.NoPlainIndexes && ix.Type == "" {
		out = append(out, fmt.Sprintf("%s only has indexes of an explicit type, but index %q sets no Index.Type", name, ix.Name))
	}
	if ix.Where != "" && !f.SupportsPartialIndexes {
		out = append(out, fmt.Sprintf("%s has no partial indexes, but index %q has a condition", name, ix.Name))
	}
	if len(ix.Include) > 0 && !f.SupportsCoveringIndexes {
		out = append(out, fmt.Sprintf("%s has no covering indexes, but index %q has INCLUDE columns", name, ix.Name))
	}
	if !f.NoPlainIndexes {
		return out
	}
	if ix.Class != "" {
		out = append(out, fmt.Sprintf("%s has no index class, but index %q sets one", name, ix.Name))
	}
	if ix.Comment != "" {
		out = append(out, fmt.Sprintf("%s has no index comments, but index %q sets one", name, ix.Name))
	}
	if slices.ContainsFunc(ix.Fields, func(c m.IndexField) bool { return c.Sort != "" || c.Collate != "" || c.Length != 0 }) {
		out = append(out, fmt.Sprintf("%s cannot order, collate or truncate the columns of an index, but index %q does", name, ix.Name))
	}
	return out
}

// backendSkips lists what the base schema editor leaves out of the schema
// of this backend without saying anything.  Every entry follows the place
// in backends/base/editor.go that consults the flag it is keyed on.
func backendSkips(f *base.Features, name string, model *m.Model) []string {
	var out []string
	for _, fld := range model.Fields {
		// TableSQL renders a field's foreign key only when
		// SupportsForeignKeys, and AddField does the same.
		if fld.Field.ForeignKey != nil && !f.SupportsForeignKeys {
			out = append(out, fmt.Sprintf("the foreign key on column %q will not be created: %s has no foreign keys", fld.Column, name))
		}
		// CreateModel sets comments only when SupportsComments, and
		// ColumnSQL inlines them only when SupportsCommentsInline.
		if fld.Field.Comment != "" && !f.SupportsComments {
			out = append(out, fmt.Sprintf("the comment on column %q will not be set: %s has no column comments", fld.Column, name))
		}
		// ColumnSQL drops NOT NULL from a string column where the empty
		// string is stored as NULL, because the column could not hold one.
		if !fld.Field.Null && !fld.Field.PrimaryKey && f.InterpretsEmptyStringsAsNulls &&
			(fld.Field.Type == m.String || fld.Field.Type == m.Bytes) {
			out = append(out, fmt.Sprintf("column %q is declared NOT NULL but will be nullable: %s stores the empty string as NULL", fld.Column, name))
		}
	}
	opts := model.Options()
	if opts.DBTableComment != "" && !f.SupportsComments {
		out = append(out, fmt.Sprintf("the table comment will not be set: %s has no table comments", name))
	}
	for _, ix := range opts.Indexes {
		out = append(out, indexSkips(f, name, ix)...)
	}
	for _, c := range opts.Constraints {
		out = append(out, constraintSkips(f, name, c)...)
	}
	return out
}

// indexSkips is what deferModelIndexes and CreateIndexSQL leave out of one
// index.
func indexSkips(f *base.Features, name string, ix m.Index) []string {
	// deferModelIndexes, AddIndex and RemoveIndex all return early on an
	// expression index the backend has no expressions for: the index is
	// never created, so nothing else about it matters.
	if ix.HasExpressions() && !f.SupportsExpressionIndexes {
		return []string{fmt.Sprintf("index %q will not be created: %s has no expression indexes", ix.Name, name)}
	}
	var out []string
	if ix.Where != "" && !f.SupportsPartialIndexes {
		out = append(out, fmt.Sprintf("index %q will be created without its condition, over every row: %s has no partial indexes", ix.Name, name))
	}
	if len(ix.Include) > 0 && !f.SupportsCoveringIndexes {
		out = append(out, fmt.Sprintf("index %q will be created without its INCLUDE columns: %s has no covering indexes", ix.Name, name))
	}
	if !f.SupportsIndexColumnOrdering && slices.ContainsFunc(ix.Fields, func(c m.IndexField) bool { return c.Sort != "" }) {
		out = append(out, fmt.Sprintf("index %q will be created without its column ordering: %s cannot order the columns of an index", ix.Name, name))
	}
	return out
}

// constraintSkips is what ConstraintSQL and CreateConstraintSQL leave out
// of one table constraint.
func constraintSkips(f *base.Features, name string, c m.Constraint) []string {
	switch x := c.(type) {
	case *m.CheckConstraint:
		if !f.SupportsTableCheckConstraints {
			return []string{fmt.Sprintf("check constraint %q will not be created: %s has no table check constraints", x.Name, name)}
		}
	case *m.ForeignKeyConstraint:
		if !f.SupportsForeignKeys {
			return []string{fmt.Sprintf("foreign key constraint %q will not be created: %s has no foreign keys", x.Name, name)}
		}
	case *m.UniqueConstraint:
		// Editor.uniqueSupported: a unique constraint the backend cannot
		// express in full is dropped whole, not weakened.
		var why string
		switch {
		case x.Condition != "" && !f.SupportsPartialIndexes:
			why = "has no partial unique constraints"
		case x.Deferrable != "" && !f.SupportsDeferrableUniqueConstraints:
			why = "has no deferrable unique constraints"
		case len(x.Include) > 0 && !f.SupportsCoveringIndexes:
			why = "has no covering indexes"
		case x.NullsDistinct != nil && !f.SupportsNullsDistinctUniqueConstraints:
			why = "cannot say whether NULLs are distinct in a unique constraint"
		default:
			return nil
		}
		return []string{fmt.Sprintf("unique constraint %q will not be created: %s %s", x.Name, name, why)}
	}
	return nil
}

// backendAlterLimit reports a model that this backend will migrate as it
// stands but will refuse to change later.  It is neither of the two
// outcomes backendRefusalChecks distinguishes -- nothing is refused and
// nothing is missing today -- which is why it carries an identifier of its
// own.
//
// backends/tidb/tidb.go AlterColumnTypeSQL is the one such limit gormgate
// knows about: the clustered integer primary key is the physical row key,
// and MODIFY rejects a change to its type.
func backendAlterLimit(b *base.Backend, model *m.Model) (string, bool) {
	if !b.Features.CannotAlterIntegerPrimaryKeyType {
		return "", false
	}
	pk := model.PK()
	if len(pk) != 1 || (pk[0].Field.Type != m.Int && pk[0].Field.Type != m.Uint) {
		return "", false
	}
	return fmt.Sprintf("the type of primary key column %q can never be changed: %s stores the table by its clustered integer primary key",
		pk[0].Column, b.DisplayName), true
}

// migrationFileName matches the migration files loader.migrationFiles
// counts, so that the inventory counts the same files the loader loads.
var migrationFileName = regexp.MustCompile(`^\d{4}_\w*\.go$`)

// countMigrations counts the migration files in an app's migrations
// directory.  A directory that is not there yet has none.
func countMigrations(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() && migrationFileName.MatchString(e.Name()) {
			n++
		}
	}
	return n
}

// inventoryCheck tells the user what of their project is gormgate's: the
// migrations directories, the file that links them into the gorm-gate
// command, and the table that records what has been applied.  It is the
// answer to "what would I have to undo?", and the answer is short, because
// the model structs are plain gorm and are not on the list.
//
// It is reported only when --tag compatibility names the tag, for two
// reasons: it is not a problem, and a bare check must keep printing
// Django's "System check identified no issues (0 silenced)." and nothing
// else (itest/parity_test.go compares that byte for byte).
//
// There is no Django function behind this; see TagCompatibility.
func inventoryCheck(c *Context, o checkOptions) []CheckMessage {
	if !slices.Contains(o.Tags, TagCompatibility) {
		return nil
	}
	p := c.Project
	var files, tables []string
	for _, a := range p.Apps {
		if len(o.AppLabels) > 0 && !slices.Contains(o.AppLabels, a.Label) {
			continue
		}
		if a.Disabled || a.MigrationsDir == "" {
			continue
		}
		files = append(files, fmt.Sprintf("%s (%s, %s)", relPath(c.Env, a.MigrationsDir), a.Label, migrationCount(countMigrations(a.MigrationsDir))))
	}
	if p.CommandDir != "" {
		files = append(files, relPath(c.Env, filepath.Join(p.CommandDir, LinkFileName)))
	}
	for _, alias := range p.DatabaseAliases() {
		tables = append(tables, fmt.Sprintf("the '%s' table of database '%s'", recorder.Table, alias))
	}
	lines := append(files, tables...)
	if len(lines) == 0 {
		return nil
	}
	// The hint names only what is actually listed: a project that has not
	// generated a migration yet has nothing to delete but the table.
	remove := "drop the table"
	if len(tables) > 1 {
		remove = "drop the tables"
	}
	if len(files) > 0 {
		remove = "delete the files listed above and " + remove
	}
	return []CheckMessage{{
		Level: LevelInfo,
		ID:    "gormgate.I001",
		Msg:   "gormgate owns these, and nothing else, in this project:\n\t  " + strings.Join(lines, "\n\t  "),
		Hint: "Your model structs are plain gorm, so nothing about them has to change to stop using gormgate: " +
			remove + ", and they keep working.",
	}}
}

// migrationCount words the file count of one app.
func migrationCount(n int) string {
	if n == 1 {
		return "1 migration file"
	}
	return fmt.Sprintf("%d migration files", n)
}
