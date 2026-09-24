// Package clickhouse is the ClickHouse backend (gorm.io/driver/clickhouse,
// dialector "clickhouse").
//
// Django has no ClickHouse backend, so this package has no Django original
// to port literally: it implements the subset of the migration operations
// ClickHouse can perform and raises migrations.NotSupportedError, with a
// message naming the operation, the model and the reason, for everything
// else. It never silently ignores an operation.
//
// The supported subset is:
//
//   - CreateModel / DeleteModel (table engine from
//     migrations.Options.ClickHouse, CHECK constraints and data-skipping
//     indexes included),
//   - AlterModelTable (RENAME TABLE) and AlterModelTableComment,
//   - AddField / RemoveField / AlterField (ALTER TABLE ... MODIFY COLUMN,
//     RENAME COLUMN) for columns outside the table's key expressions,
//   - AddIndex / RemoveIndex for data-skipping indexes (Index.Type, e.g.
//     "minmax", is required),
//   - AddConstraint / RemoveConstraint for CHECK constraints,
//   - RunSQL and RunGo.
//
// Not supported, and reported as an error: foreign keys, unique fields,
// unique indexes, UniqueConstraint, AlterUniqueTogether, RenameIndex, and
// any alteration of a column that is part of the ORDER BY / PRIMARY KEY /
// PARTITION BY expressions of the table.
//
// Two properties of a column have no meaning on ClickHouse and are
// therefore accepted but not translated into DDL, exactly as the gorm
// ClickHouse driver does: NULL / NOT NULL (nullability is part of the
// column type, Nullable(T), and the driver never generates it) and
// AUTO_INCREMENT (there are no sequences; the migrations recorder computes
// max(id) + 1 itself). Features.NoColumnNullability reports the first.
package clickhouse

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/ctolon/gormgate/backends/base"
	m "github.com/ctolon/gormgate/migrations"
)

// DefaultEngine is the table engine used when a model carries no
// migrations.Options.ClickHouse (the engine of the gorm driver's
// DefaultTableEngineOpts).
const DefaultEngine = "MergeTree()"

// DefaultGranularity is the GRANULARITY of a data-skipping index whose
// Index.Option does not name one (the gorm driver's DefaultGranularity).
const DefaultGranularity = 3

// MinVersion is the oldest server this backend works with: lightweight
// DELETE (used by the migrations recorder to unapply a migration) was added
// in 22.8.
var MinVersion = [2]int{22, 8}

// Features are the ClickHouse server's real capabilities.
//
// ClickHouse has no transactions (an implicit transaction covers a single
// INSERT only), no foreign keys, no unique constraints or unique indexes,
// and no ordinary indexes: its only indexes are data-skipping indexes,
// which need an explicit type.
var Features = base.Features{
	SupportsTransactions:      false,
	CanRollbackDDL:            false,
	SupportsCombinedAlters:    false,
	SupportsForeignKeys:       false,
	SupportsUniqueConstraints: false,
	CanCreateInlineFK:         false,
	// Column and table comments are part of the DDL; a column comment is
	// written inline in CREATE TABLE / ADD COLUMN, like the gorm driver.
	SupportsComments:       true,
	SupportsCommentsInline: true,
	// Data-skipping indexes have no WHERE clause and no INCLUDE columns,
	// but they can be built over an expression.
	SupportsPartialIndexes:                 false,
	SupportsExpressionIndexes:              true,
	SupportsCoveringIndexes:                false,
	SupportsDeferrableUniqueConstraints:    false,
	SupportsNullsDistinctUniqueConstraints: false,
	SupportsIndexColumnOrdering:            false,
	SupportsTableCheckConstraints:          true,
	CanRenameIndex:                         false,
	InterpretsEmptyStringsAsNulls:          false,
	AllowsMultipleConstraintsOnSameFields:  false,
	IgnoresTableNameCase:                   false,
	RefusesUnsupportedObjects:              true,
	NoPlainIndexes:                         true,
	NoColumnNullability:                    true,
	NoCastsToSizedStrings:                  true,
	// The migrations table: FixedString pads, there is no auto-increment
	// to fill an id in, and a DELETE is a mutation that the next statement
	// would otherwise not see.
	PadsSizedStrings:        true,
	NoAutoIncrementOnInsert: true,
}

// Backend is the ClickHouse backend definition.
var Backend = &base.Backend{
	Vendor:                 "clickhouse",
	RecorderDeleteSettings: " SETTINGS mutations_sync = 2",
	DisplayName:            "ClickHouse",
	Features:               Features,
	// ClickHouse identifiers are limited by the file name length of the
	// underlying storage, which is far beyond any generated name.
	MaxNameLength:       0,
	NewIntrospection:    func(c *base.Conn) base.Introspection { return &Introspection{Conn: c} },
	SequenceResetSQL:    SequenceResetSQL,
	Ops:                 Ops{},
	InitConnectionState: InitConnectionState,
}

func init() {
	Backend.NewEditor = func(c *base.Conn, collect, atomic bool) base.SchemaEditor {
		return NewEditor(c, collect, atomic)
	}
	base.Register(base.Detector{
		Name:         "clickhouse",
		Dialector:    "clickhouse",
		VersionQuery: "SELECT version()",
		Check:        checkVersion,
		Backend:      Backend,
	})
}

var versionRe = regexp.MustCompile(`^(\d+)\.(\d+)`)

// checkVersion rejects servers older than MinVersion.
func checkVersion(_ *base.Conn, version string) error {
	mt := versionRe.FindStringSubmatch(version)
	if mt == nil {
		return fmt.Errorf("gormgate: cannot read the ClickHouse server version from %q", version)
	}
	major, _ := strconv.Atoi(mt[1])
	minor, _ := strconv.Atoi(mt[2])
	if major < MinVersion[0] || (major == MinVersion[0] && minor < MinVersion[1]) {
		return fmt.Errorf("gormgate: ClickHouse %s is too old; %d.%d or newer is required (lightweight DELETE)",
			version, MinVersion[0], MinVersion[1])
	}
	return nil
}

// InitConnectionState prepares the pinned migration session.
//
// It makes mutations synchronous: ALTER TABLE ... UPDATE / DELETE /
// MATERIALIZE COLUMN and the data rewrite of MODIFY COLUMN are
// asynchronous by default, so a migration (or the RunGo code of one) could
// not read back what it just wrote.
//
// It re-enables gorm's default transaction: the gorm ClickHouse driver
// sends an INSERT as a prepared batch, and the clickhouse-go
// standard-library driver only flushes that batch when the surrounding
// transaction commits (its Begin and Commit exchange nothing with the
// server; Commit calls batch.Send). Without it the INSERT of a RunGo
// historical model would never reach the server and would leave the
// connection waiting for data blocks.
//
// Finally it completes the UPDATE and DELETE statements the driver builds
// for a query without a condition: ClickHouse spells them ALTER TABLE ...
// UPDATE / DELETE, and both need a WHERE clause, while
// migrations.Objects.Update and Delete over every row (a nil condition)
// produce none.
func InitConnectionState(_ *base.Conn, session *gorm.DB) (*gorm.DB, error) {
	if err := session.Exec("SET mutations_sync = 2").Error; err != nil {
		return nil, err
	}
	s := session.Session(&gorm.Session{})
	s.Config.SkipDefaultTransaction = false
	builders := make(map[string]clause.ClauseBuilder, len(s.ClauseBuilders))
	for name, build := range s.ClauseBuilders {
		builders[name] = build
	}
	// The clauses of an update are UPDATE, SET, WHERE and those of a delete
	// DELETE, WHERE, so the missing condition is appended to the last
	// clause before it.
	for _, name := range []string{"SET", "DELETE"} {
		build := builders[name]
		if build == nil {
			continue
		}
		builders[name] = func(c clause.Clause, b clause.Builder) {
			build(c, b)
			if stmt, ok := b.(*gorm.Statement); ok {
				if _, ok := stmt.Clauses["WHERE"]; !ok {
					b.WriteString(" WHERE 1")
				}
			}
		}
	}
	s.Config.ClauseBuilders = builders
	return s, nil
}

// Ops are the connection.ops helpers.
type Ops struct{}

// QuoteName quotes an identifier with backticks, like the gorm driver's
// Dialector.QuoteTo.
func (Ops) QuoteName(name string) string {
	if len(name) >= 2 && strings.HasPrefix(name, "`") && strings.HasSuffix(name, "`") {
		return name
	}
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}

// QuoteValue renders a literal. ClickHouse reads a doubled single quote
// inside a string literal as an escaped quote, which is what gorm's
// logger.ExplainSQL produces for the driver.
func (Ops) QuoteValue(v any) (string, error) {
	return base.StandardQuoteValue(v, base.QuoteValueOptions{
		True: "true", False: "false", TimeFormat: "2006-01-02 15:04:05.999",
		Bytes: func(b []byte) string {
			return base.QuoteString(string(b))
		},
	})
}

// SplitOptions are ClickHouse's lexical rules: backslash escapes inside
// string literals and backtick-quoted identifiers, and no dollar quoting.
var SplitOptions = base.SplitOptions{BackslashEscapes: true, BacktickQuotes: true}

// PrepareSQLScript splits a script into statements.
func (Ops) PrepareSQLScript(script string) []string { return base.SplitSQL(script, SplitOptions) }

// StartTransactionSQL returns no statement: ClickHouse has no transactions
// covering DDL, so sqlmigrate never wraps its output (the command only does
// so when the backend can roll back DDL).
func (Ops) StartTransactionSQL() string { return "" }

// EndTransactionSQL returns no statement.
func (Ops) EndTransactionSQL() string { return "" }

// SequenceResetSQL returns no statements: ClickHouse has no sequences, so
// sqlsequencereset prints nothing.
func SequenceResetSQL(*base.Conn, base.Style, []*m.Model) ([]string, error) { return nil, nil }

// mutationSettings makes a statement that rewrites data wait for the
// mutation it starts, so that the next statement (and the collected SQL
// replayed on another connection) sees the result.
const mutationSettings = " SETTINGS mutations_sync = 2"

// Editor is the ClickHouse schema editor.
type Editor struct {
	*base.Editor
}

// A method whose name does not match a step of base.Overrides is a
// method nothing calls; this makes that a build failure.
var _ base.Overrides = (*Editor)(nil)

// NewEditor builds a ClickHouse schema editor.
func NewEditor(c *base.Conn, collect, atomic bool) *Editor {
	e := &Editor{}
	e.Editor = base.NewEditor(e, c, base.EditorOptions{
		CollectSQL: collect, Atomic: atomic, Grammar: Grammar{},
	})
	return e
}

// notSupported builds the backend's NotSupportedError.
func notSupported(op, subject, reason string) error {
	return m.NotSupportedf("clickhouse: %s %s: %s", op, subject, reason)
}

const (
	noForeignKeys = "ClickHouse has no foreign keys"
	noUnique      = "ClickHouse has no unique constraints or unique indexes"
)

// checkField rejects the field properties ClickHouse cannot express.
func (e *Editor) checkField(op string, model *m.Model, f *m.ModelField) error {
	subject := model.Label() + "." + f.Name
	if f.Field.ForeignKey != nil {
		to := f.Field.ForeignKey.To
		if f.Remote != nil {
			to = f.Remote.Label()
		}
		return notSupported(op, subject, fmt.Sprintf("%s (the field references %s)", noForeignKeys, to))
	}
	if f.Field.Unique && !f.Field.PrimaryKey {
		return notSupported(op, subject, noUnique+" (the field is unique)")
	}
	return nil
}

// checkModel rejects the model options ClickHouse cannot express.
func (e *Editor) checkModel(op string, model *m.Model) error {
	for _, f := range model.Fields {
		if err := e.checkField(op, model, f); err != nil {
			return err
		}
	}
	opts := model.Options()
	if len(opts.UniqueTogether) > 0 {
		return notSupported(op, model.Label(),
			fmt.Sprintf("%s (unique_together (%s))", noUnique, strings.Join(opts.UniqueTogether[0], ", ")))
	}
	for _, c := range opts.Constraints {
		if err := checkConstraint(op, model, c); err != nil {
			return err
		}
	}
	return nil
}

// checkConstraint rejects the table constraints ClickHouse cannot express.
// It has CHECK constraints, and nothing else: no unique constraints and no
// foreign keys, whether a foreign key sits on a field or spans several
// columns.
func checkConstraint(op string, model *m.Model, c m.Constraint) error {
	switch x := c.(type) {
	case *m.UniqueConstraint:
		return notSupported(op, model.Label(), fmt.Sprintf("%s (constraint %q)", noUnique, x.Name))
	case *m.ForeignKeyConstraint:
		return notSupported(op, model.Label(),
			fmt.Sprintf("%s (constraint %q references %s)", noForeignKeys, x.Name, x.To))
	}
	return nil
}

var wordSep = regexp.MustCompile(`[^0-9A-Za-z_]+`)

// keyColumns returns the columns that take part in the ORDER BY, PRIMARY
// KEY or PARTITION BY expressions of the table: ClickHouse refuses to
// rename, retype or drop any of them.
func keyColumns(model *m.Model) map[string]bool {
	out := map[string]bool{}
	ch := model.Options().ClickHouse
	if ch == nil || ch.OrderBy == "" {
		for _, f := range model.PK() {
			out[f.Column] = true
		}
	}
	if ch == nil {
		return out
	}
	words := map[string]bool{}
	for _, w := range wordSep.Split(ch.OrderBy+" "+ch.PrimaryKey+" "+ch.PartitionBy, -1) {
		words[w] = true
	}
	for _, f := range model.Fields {
		if words[f.Column] {
			out[f.Column] = true
		}
	}
	return out
}

// keyReason explains why a key column cannot be altered.
const keyReason = "the column is part of the ORDER BY / PRIMARY KEY / PARTITION BY key of the table, which ClickHouse cannot alter"

// EngineSQL renders the storage clause of CREATE TABLE.
func (e *Editor) EngineSQL(model *m.Model) string {
	engine, orderBy, partitionBy, primaryKey, settings := DefaultEngine, "", "", "", ""
	if ch := model.Options().ClickHouse; ch != nil {
		if ch.Engine != "" {
			engine = ch.Engine
		}
		orderBy, partitionBy, primaryKey, settings = ch.OrderBy, ch.PartitionBy, ch.PrimaryKey, ch.Settings
	}
	if orderBy == "" && strings.Contains(engine, "MergeTree") {
		// A MergeTree table must be sorted; the primary key columns are the
		// natural key, and a model without one is stored unsorted (the gorm
		// driver's default ORDER BY tuple()).
		var cols []string
		for _, f := range model.PK() {
			cols = append(cols, e.QuoteName(f.Column))
		}
		if len(cols) > 0 {
			orderBy = "(" + strings.Join(cols, ", ") + ")"
		} else {
			orderBy = "tuple()"
		}
	}
	sql := " ENGINE = " + engine
	for _, part := range [][2]string{
		{"PARTITION BY", partitionBy},
		{"PRIMARY KEY", primaryKey},
		{"ORDER BY", orderBy},
	} {
		if part[1] != "" {
			sql += " " + part[0] + " " + part[1]
		}
	}
	if settings != "" {
		sql += " SETTINGS " + settings
	}
	return sql
}

// ColumnSQL returns the column definition: type, DEFAULT and the inline
// comment, in the order gorm's clickhouse Migrator.FullDataTypeOf uses.
// There is no NULL / NOT NULL and no inline UNIQUE.
func (e *Editor) ColumnSQL(model *m.Model, f *m.ModelField, includeDefault bool) (string, error) {
	typ, err := e.ColumnType(model, f)
	if err != nil {
		return "", err
	}
	parts := []string{typ}
	if f.Field.DBDefault != nil {
		ds, err := e.Self().DBDefaultSQL(f)
		if err != nil {
			return "", err
		}
		parts = append(parts, "DEFAULT "+ds)
	} else if includeDefault {
		if v := base.EffectiveDefault(f); v != nil {
			q, err := e.QuoteValue(v)
			if err != nil {
				return "", err
			}
			parts = append(parts, "DEFAULT "+q)
		}
	}
	if f.Field.Comment != "" {
		q, err := e.QuoteValue(f.Field.Comment)
		if err != nil {
			return "", err
		}
		parts = append(parts, "COMMENT "+q)
	}
	return strings.Join(parts, " "), nil
}

// TableSQL returns the CREATE TABLE statement with its engine clause.
func (e *Editor) TableSQL(model *m.Model) (string, error) {
	if err := e.checkModel("CreateModel", model); err != nil {
		return "", err
	}
	var defs []string
	for _, f := range model.Fields {
		def, err := e.Self().ColumnSQL(model, f, false)
		if err != nil {
			return "", err
		}
		defs = append(defs, e.QuoteName(f.Column)+" "+def)
	}
	for _, c := range model.Options().Constraints {
		s, err := e.Self().ConstraintSQL(model, c)
		if err != nil {
			return "", err
		}
		if s != "" {
			defs = append(defs, s)
		}
	}
	ct := base.CreateTable{Table: e.QuoteName(model.Table), Definition: strings.Join(defs, ", ")}
	return e.Grammar().CreateTable(ct) + e.EngineSQL(model), nil
}

// AddField adds a column. A one-off default (Django's preserve_default
// False) is written into the existing parts with MATERIALIZE COLUMN before
// it is dropped again: ClickHouse computes the default of a missing column
// at read time, so removing it would otherwise leave the old rows with the
// zero value of the type.
func (e *Editor) AddField(model *m.Model, f *m.ModelField) error {
	if err := e.checkField("AddField", model, f); err != nil {
		return err
	}
	def, err := e.Self().ColumnSQL(model, f, true)
	if err != nil {
		return err
	}
	table, column := e.QuoteName(model.Table), e.QuoteName(f.Column)
	add := base.AddColumn{Table: table, Column: column, Definition: def}
	if err := e.Execute(e.Grammar().AddColumn(add)); err != nil {
		return err
	}
	if f.Field.DBDefault == nil && base.EffectiveDefault(f) != nil {
		if err := e.Execute("ALTER TABLE " + table + " MATERIALIZE COLUMN " + column + mutationSettings); err != nil {
			return err
		}
		changes, err := e.Self().AlterColumnDefaultSQL(model, nil, f, true)
		if err != nil {
			return err
		}
		return e.Execute(e.Grammar().AlterTable(base.AlterTable{Table: table, Changes: changes}))
	}
	return nil
}

// RemoveField drops a column that is not part of the table's key
// expressions. ClickHouse has no foreign keys, so the base editor's drop of
// them never runs.
func (e *Editor) RemoveField(model *m.Model, f *m.ModelField) error {
	if keyColumns(model)[f.Column] {
		return notSupported("RemoveField", model.Label()+"."+f.Name, keyReason)
	}
	return e.Editor.RemoveField(model, f)
}

// PerformAlterField rejects the alterations ClickHouse cannot make and
// leaves the rest to the base editor.
func (e *Editor) PerformAlterField(model *m.Model, old, new *m.ModelField, oldType, newType string, strict bool) error {
	subject := model.Label() + "." + new.Name
	if err := e.checkField("AlterField", model, new); err != nil {
		return err
	}
	if old.Field.ForeignKey != nil {
		return notSupported("AlterField", subject, noForeignKeys+" (the previous field was a foreign key)")
	}
	if old.Field.Unique && !old.Field.PrimaryKey {
		return notSupported("AlterField", subject, noUnique+" (the previous field was unique)")
	}
	if old.Field.PrimaryKey != new.Field.PrimaryKey {
		return notSupported("AlterField", subject,
			"the primary key of a ClickHouse table is its sorting key and cannot be changed by ALTER TABLE")
	}
	if keys := keyColumns(model); keys[old.Column] {
		switch {
		case old.Column != new.Column:
			return notSupported("AlterField", subject, keyReason)
		case oldType != newType:
			return notSupported("AlterField", subject, keyReason)
		}
	}
	return e.Editor.PerformAlterField(model, old, new, oldType, newType, strict)
}

// AlterUniqueTogether is not supported.
func (e *Editor) AlterUniqueTogether(model *m.Model, old, new [][]string) error {
	if len(old) == 0 && len(new) == 0 {
		return nil
	}
	return notSupported("AlterUniqueTogether", model.Label(), noUnique)
}

// RenameIndex is not supported: ClickHouse has no ALTER TABLE ... RENAME
// INDEX, and re-creating a data-skipping index under the new name would
// silently lose the index data of the existing parts.
func (e *Editor) RenameIndex(model *m.Model, old, new m.Index) error {
	return notSupported("RenameIndex", model.Label(),
		fmt.Sprintf("ClickHouse cannot rename index %q; remove it and add %q instead", old.Name, new.Name))
}

var granularityRe = regexp.MustCompile(`^(?i:GRANULARITY\s+)?(\d+)$`)

// granularity reads the GRANULARITY of a data-skipping index from
// Index.Option ("4" or "GRANULARITY 4"); the gorm driver's default is used
// when it is empty.
func granularity(ix m.Index) (int, error) {
	opt := strings.TrimSpace(ix.Option)
	if opt == "" {
		return DefaultGranularity, nil
	}
	mt := granularityRe.FindStringSubmatch(opt)
	if mt == nil {
		return 0, fmt.Errorf("clickhouse: index %q: Option %q is not a GRANULARITY", ix.Name, ix.Option)
	}
	n, err := strconv.Atoi(mt[1])
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("clickhouse: index %q: GRANULARITY %q must be a positive number", ix.Name, mt[1])
	}
	return n, nil
}

// CreateIndexSQL renders ALTER TABLE ... ADD INDEX for a data-skipping
// index.
func (e *Editor) CreateIndexSQL(model *m.Model, ix m.Index) (*base.Statement, error) {
	subject := fmt.Sprintf("%s (index %q)", model.Label(), ix.Name)
	switch {
	case ix.Unique:
		return nil, notSupported("AddIndex", subject, noUnique)
	case ix.Type == "":
		return nil, notSupported("AddIndex", subject,
			`ClickHouse only has data-skipping indexes, so Index.Type is required (for example "minmax", "set(100)" or "bloom_filter")`)
	case ix.Where != "":
		return nil, notSupported("AddIndex", subject, "a ClickHouse data-skipping index has no condition")
	case len(ix.Include) > 0:
		return nil, notSupported("AddIndex", subject, "a ClickHouse data-skipping index has no INCLUDE columns")
	case ix.Class != "":
		return nil, notSupported("AddIndex", subject, "ClickHouse has no index class; use Index.Type")
	case ix.Comment != "":
		return nil, notSupported("AddIndex", subject, "a ClickHouse data-skipping index has no comment")
	}
	for _, f := range ix.Fields {
		if f.Sort != "" || f.Collate != "" || f.Length != 0 {
			return nil, notSupported("AddIndex", subject,
				"a ClickHouse data-skipping index has no column order, collation or prefix length")
		}
	}
	gran, err := granularity(ix)
	if err != nil {
		return nil, err
	}
	var columns base.Reference
	if ix.HasExpressions() {
		columns = &base.Expressions{Table: model.Table, SQL: strings.Join(e.IndexColumnsSQL(ix, false), ", ")}
	} else {
		columns = &base.Columns{Table: model.Table, Names: ix.Columns(), Quote: e.QuoteName}
	}
	table := &base.Table{Name: model.Table, Quote: e.QuoteName}
	name := &base.Name{Value: ix.Name, Quote: e.QuoteName, Table: model.Table, Columns: ix.Columns()}
	tail := fmt.Sprintf(" TYPE %s GRANULARITY %d", ix.Type, gran)
	st := base.NewStatement(table, name, columns)
	return e.SpellAs(st, base.StatementCreateIndex, name, func(base.Grammar) string {
		return "ALTER TABLE " + table.String() + " ADD INDEX " + name.String() +
			" (" + columns.String() + ")" + tail
	}), nil
}

// ConstraintSQL renders a constraint inside CREATE TABLE.
func (e *Editor) ConstraintSQL(model *m.Model, c m.Constraint) (string, error) {
	if err := checkConstraint("CreateModel", model, c); err != nil {
		return "", err
	}
	return e.Editor.ConstraintSQL(model, c)
}

// CreateConstraintSQL renders ALTER TABLE ... ADD CONSTRAINT.
func (e *Editor) CreateConstraintSQL(model *m.Model, c m.Constraint) (*base.Statement, error) {
	if err := checkConstraint("AddConstraint", model, c); err != nil {
		return nil, err
	}
	return e.Editor.CreateConstraintSQL(model, c)
}

// RemoveConstraintSQL renders ALTER TABLE ... DROP CONSTRAINT.
func (e *Editor) RemoveConstraintSQL(model *m.Model, c m.Constraint) (*base.Statement, error) {
	if err := checkConstraint("RemoveConstraint", model, c); err != nil {
		return nil, err
	}
	return e.Editor.RemoveConstraintSQL(model, c)
}

// Grammar is ClickHouse's spelling of the statements gormgate emits. The
// ones it has no equivalent for render empty; the editor refuses them
// before they could be reached.
type Grammar struct{ base.BaseGrammar }

func (Grammar) RenameTable(c base.RenameTable) string {
	return "RENAME TABLE " + c.OldTable + " TO " + c.NewTable
}

// DropTable takes SYNC, which drops the data with the table so that a
// table recreated right after does not race with the background removal of
// the old one.
func (Grammar) DropTable(c base.DropTable) string { return "DROP TABLE " + c.Table + " SYNC" }

func (Grammar) AlterTable(c base.AlterTable) string {
	return "ALTER TABLE " + c.Table + " " + c.Changes + mutationSettings
}

func (Grammar) AlterColumnType(c base.AlterColumnType) string {
	return "MODIFY COLUMN " + c.Column + " " + c.Type
}

// AlterColumnNull and AlterColumnNotNull render nothing: nullability is
// not a column property here, it is part of the type (see the package
// comment).
func (Grammar) AlterColumnNull(base.AlterColumnNullity) string    { return "" }
func (Grammar) AlterColumnNotNull(base.AlterColumnNullity) string { return "" }

func (Grammar) AlterColumnDefault(c base.AlterColumnDefault) string {
	return "MODIFY COLUMN " + c.Column + " " + c.Type + " DEFAULT " + c.Default
}

func (Grammar) AlterColumnDropDefault(c base.AlterColumnDefault) string {
	return "MODIFY COLUMN " + c.Column + " REMOVE DEFAULT"
}

func (Grammar) AlterColumnDropDefaultNull(c base.AlterColumnDefault) string {
	return "MODIFY COLUMN " + c.Column + " REMOVE DEFAULT"
}

func (Grammar) FillColumnDefault(c base.FillColumnDefault) string {
	return "ALTER TABLE " + c.Table + " UPDATE " + c.Column + " = " + c.Default +
		" WHERE " + c.Column + " IS NULL" + mutationSettings
}

func (Grammar) TableComment(c base.TableComment) string {
	return "ALTER TABLE " + c.Table + " MODIFY COMMENT " + c.Comment
}

func (Grammar) ColumnComment(c base.ColumnComment) string {
	return "ALTER TABLE " + c.Table + " COMMENT COLUMN " + c.Column + " " + c.Comment
}

// PrimaryKeyConstraint and UniqueConstraint render nothing: ClickHouse has
// neither.
func (Grammar) PrimaryKeyConstraint(base.PrimaryKeyConstraint) string { return "" }
func (Grammar) UniqueConstraint(base.UniqueConstraint) string         { return "" }

// AddForeignKey and the six below render nothing: ClickHouse has neither
// foreign keys, nor unique constraints, nor primary key constraints, and an
// index cannot be renamed. The editor refuses each of them before it gets
// here; rendering empty is the second line.
func (Grammar) AddForeignKey(base.AddForeignKey) string   { return "" }
func (Grammar) AddUnique(base.AddUnique) string           { return "" }
func (Grammar) AddPrimaryKey(base.AddPrimaryKey) string   { return "" }
func (Grammar) CreateUniqueIndex(base.CreateIndex) string { return "" }
func (Grammar) DropUnique(base.DropConstraint) string     { return "" }
func (Grammar) DropForeignKey(base.DropConstraint) string { return "" }
func (Grammar) DropPrimaryKey(base.DropConstraint) string { return "" }
func (Grammar) RenameIndex(base.RenameIndex) string       { return "" }

// DropIndex names the table: a data-skipping index belongs to one.
func (Grammar) DropIndex(c base.DropIndex) string {
	return "ALTER TABLE " + c.Table + " DROP INDEX " + c.Name
}
