// Package cockroachdb is the CockroachDB backend. CockroachDB speaks the
// PostgreSQL wire protocol and is used through gorm.io/driver/postgres, so
// this backend builds on backends/postgresql and ports the differences of
// django-cockroachdb.
//
// django: django-cockroachdb (django_cockroachdb/)
package cockroachdb

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"gorm.io/gorm"

	"github.com/ctolon/gormgate/backends/base"
	"github.com/ctolon/gormgate/backends/postgresql"
	m "github.com/ctolon/gormgate/migrations"
)

// Features are django-cockroachdb's DatabaseFeatures values (which extend
// PostgreSQL's), corrected where CockroachDB v25.2 really behaves
// differently from what django-cockroachdb assumes.
//
// django: django_cockroachdb/features.py DatabaseFeatures
var Features = func() base.Features {
	f := postgresql.Features
	// CREATE EXTENSION is "not yet implemented" (go.crdb.dev/issue-v/54516)
	// and there is no CREATE COLLATION at all.
	f.SupportsExtensions = false
	f.SupportsCollations = false
	// autocommit_before_ddl is on by default since v25.2: a DDL statement
	// inside a transaction commits that transaction first ("NOTICE:
	// auto-committing transaction before processing DDL due to
	// autocommit_before_ddl setting"), so DDL can never be rolled back.
	// (cockroachlabs.com/docs/stable/known-limitations#schema-changes-within-transactions)
	f.CanRollbackDDL = false
	// "unimplemented: ALTER COLUMN TYPE operations that require rewriting
	// on-disk data cannot be combined with other ALTER TABLE commands"
	// (go.crdb.dev/issue/49351).
	f.SupportsCombinedAlters = false
	// "at or near "deferrable": syntax error: unimplemented: this syntax"
	// (go.crdb.dev/issue-v/31632).
	f.SupportsDeferrableUniqueConstraints = false
	// UNIQUE NULLS NOT DISTINCT: "syntax error at or near "nulls""
	// (go.crdb.dev/issue/115836).
	f.SupportsNullsDistinctUniqueConstraints = false
	// django-cockroachdb sets supports_comments = False only because
	// pg_catalog.obj_description() is slow (go.crdb.dev/issue/95068).
	// COMMENT ON TABLE/COLUMN and their introspection work on v25.2, and
	// gorm's AutoMigrate emits them, so gormgate must support them.
	f.SupportsComments = true
	// A serial column defaults to unique_rowid(), which mixes the current
	// timestamp with the node id: keys are unique and roughly ordered but
	// never 1, 2, 3, ...
	f.NonSequentialAutoIncrement = true
	return f
}()

// Backend is the CockroachDB backend definition.
var Backend = &base.Backend{
	Vendor:      "cockroachdb",
	DisplayName: "CockroachDB",
	Features:    Features,
	// django-cockroachdb inherits PostgreSQL's max_name_length (63).
	MaxNameLength:       63,
	NewIntrospection:    func(c *base.Conn) base.Introspection { return NewIntrospection(c) },
	SequenceResetSQL:    SequenceResetSQL,
	Ops:                 Ops{},
	InitConnectionState: InitConnectionState,
}

func init() {
	Backend.NewEditor = func(c *base.Conn, collect, atomic bool) base.SchemaEditor {
		return NewEditor(c, collect, atomic)
	}
	base.Register(base.Detector{
		Name: "cockroachdb",
		// Ahead of the PostgreSQL detector, which shares the dialector.
		Priority:     base.PriorityDerived,
		Dialector:    "postgres",
		VersionQuery: "SELECT version()",
		Match:        func(v string) bool { return strings.Contains(v, "CockroachDB") },
		Check:        CheckVersion,
		Backend:      Backend,
	})
}

// MinimumVersion is django-cockroachdb's minimum_database_version.
//
// django: django_cockroachdb/features.py DatabaseFeatures.minimum_database_version
var MinimumVersion = [2]int{24, 1}

// CheckVersion rejects servers older than MinimumVersion.
func CheckVersion(_ *base.Conn, version string) error {
	major, minor, ok := ServerVersion(version)
	if !ok {
		return fmt.Errorf("gormgate: cannot read the CockroachDB version from %q", version)
	}
	if major < MinimumVersion[0] || (major == MinimumVersion[0] && minor < MinimumVersion[1]) {
		return fmt.Errorf("gormgate: CockroachDB %d.%d is too old; gormgate requires %d.%d or later",
			major, minor, MinimumVersion[0], MinimumVersion[1])
	}
	return nil
}

// InitConnectionState enables the general form of ALTER COLUMN TYPE, which
// is experimental before v25.1 and rejects any change needing a rewrite
// with "ALTER COLUMN TYPE from ... to ... is only supported experimentally"
// otherwise. The setting is a no-op on newer servers.
//
// django: django_cockroachdb/schema.py DatabaseSchemaEditor._alter_field
func InitConnectionState(c *base.Conn, session *gorm.DB) (*gorm.DB, error) {
	if err := session.Exec("SET enable_experimental_alter_column_type_general = true").Error; err != nil {
		return nil, fmt.Errorf("enabling ALTER COLUMN TYPE: %w", err)
	}
	return nil, nil
}

// Ops are django-cockroachdb's DatabaseOperations helpers; quoting,
// literals and transaction SQL are PostgreSQL's.
//
// django: django_cockroachdb/operations.py DatabaseOperations
type Ops struct{ postgresql.Ops }

// SequenceResetSQL returns nothing: serial columns default to
// unique_rowid() instead of a sequence, and resetting sequences is not
// implemented (go.crdb.dev/issue/20956).
//
// django: django_cockroachdb/operations.py DatabaseOperations.sequence_reset_sql
func SequenceResetSQL(*base.Conn, base.Style, []*m.Model) ([]string, error) { return nil, nil }

// Editor is the CockroachDB schema editor.
//
// django: django_cockroachdb/schema.py DatabaseSchemaEditor
type Editor struct {
	*postgresql.Editor
	// replacingPK is set while PerformAlterField turns a non-primary-key
	// field into a primary key: ALTER PRIMARY KEY replaces the old key, so
	// there is nothing to drop first.
	replacingPK bool
	// droppingPK is set while PerformAlterField takes a field out of the
	// primary key; newPK are the primary-key columns that remain.
	droppingPK bool
	newPK      []string
}

// A method whose name does not match a step of base.Overrides is a
// method nothing calls; this makes that a build failure.
var _ base.Overrides = (*Editor)(nil)

// NewEditor builds a CockroachDB schema editor.
func NewEditor(c *base.Conn, collect, atomic bool) *Editor {
	e := &Editor{}
	e.Editor = postgresql.NewEmbeddedEditor(e, c, postgresql.StandardSerial,
		base.EditorOptions{CollectSQL: collect, Atomic: atomic, Grammar: Grammar{}})
	return e
}

// CreateIndexSQL renders CREATE INDEX without PostgreSQL operator classes,
// which CockroachDB does not support.
//
// django: django_cockroachdb/schema.py DatabaseSchemaEditor._index_columns
func (e *Editor) CreateIndexSQL(model *m.Model, ix m.Index) (*base.Statement, error) {
	ix.OpClasses = nil
	return e.Editor.CreateIndexSQL(model, ix)
}

// CreatePrimaryKeySQL renders ALTER PRIMARY KEY: CockroachDB has no
// ALTER TABLE ... ADD CONSTRAINT ... PRIMARY KEY for a table that already
// has one, and every table has one.
func (e *Editor) CreatePrimaryKeySQL(model *m.Model, columns []string) *base.Statement {
	table := &base.Table{Name: model.Table, Quote: e.QuoteName}
	cols := &base.Columns{Table: model.Table, Names: append([]string(nil), columns...), Quote: e.QuoteName}
	return e.Spell(base.NewStatement(table, cols), func(g base.Grammar) string {
		return g.AddPrimaryKey(base.AddPrimaryKey{Table: table.String(), Columns: cols.String()})
	})
}

// DeletePrimaryKey replaces the primary key with the one that remains.
// CockroachDB has no ALTER TABLE ... DROP CONSTRAINT for a primary key
// ("unimplemented: primary key dropped without subsequent addition of new
// primary key in same transaction", go.crdb.dev/issue/48026) and every
// table must have one, so the only way to change it is ALTER PRIMARY KEY.
func (e *Editor) DeletePrimaryKey(model *m.Model, strict bool) error {
	if e.replacingPK {
		// The caller installs the new key right after this; ALTER PRIMARY
		// KEY replaces the old one in the same statement.
		return nil
	}
	if e.droppingPK && len(e.newPK) > 0 {
		return e.Execute(e.Self().CreatePrimaryKeySQL(model, e.newPK).String())
	}
	return fmt.Errorf("gormgate: CockroachDB cannot drop the primary key of %s without installing a new one: "+
		"every table has a primary key and dropping one is unimplemented (go.crdb.dev/issue/48026)", model.Table)
}

var (
	// CockroachDB normalizes every serial type to INT8 DEFAULT
	// unique_rowid() (the default serial_normalization = rowid).
	serialTypes = map[string]bool{
		"smallserial": true, "serial": true, "bigserial": true,
		"serial2": true, "serial4": true, "serial8": true,
	}
	versionRE     = regexp.MustCompile(`CockroachDB \w+ v(\d+)\.(\d+)`)
	integerDBType = map[string]bool{"integer": true, "bigint": true, "smallint": true}
)

// serialDefault is the default expression behind every serial column.
const serialDefault = "unique_rowid()"

// ServerVersion parses "CockroachDB CCL v25.2.23 (...)".
func ServerVersion(v string) (major, minor int, ok bool) {
	mm := versionRE.FindStringSubmatch(v)
	if mm == nil {
		return 0, 0, false
	}
	major, _ = strconv.Atoi(mm[1])
	minor, _ = strconv.Atoi(mm[2])
	return major, minor, true
}

// usingSQL returns the USING cast for a type change. Integer types are all
// stored as the same 64-bit value and a change that only touches type
// parameters keeps the on-disk representation; adding a USING clause anyway
// makes CockroachDB treat the change as a rewrite, which fails for columns
// that are part of an index.
//
// django: db/backends/postgresql/schema.py DatabaseSchemaEditor._using_sql
// (the integer short-circuit is gormgate's; django-cockroachdb has none)
func usingSQL(column, oldType, newType string) string {
	oldBase, newBase := postgresql.BaseTypeName(oldType), postgresql.BaseTypeName(newType)
	if integerDBType[oldBase] && integerDBType[newBase] {
		return ""
	}
	if oldBase == newBase {
		return ""
	}
	return fmt.Sprintf(" USING %s::%s", column, newType)
}

// PerformAlterField drives the primary-key transitions through ALTER
// PRIMARY KEY and cleans up after it: CockroachDB keeps the index of the
// replaced primary key as a secondary unique index named
// "<table>_<columns>_key", which a table created from scratch would not
// have.
//
// django: django_cockroachdb/schema.py DatabaseSchemaEditor._alter_field
func (e *Editor) PerformAlterField(model *m.Model, old, new *m.ModelField, oldType, newType string, strict bool) error {
	e.replacingPK = !old.Field.PrimaryKey && new.Field.PrimaryKey
	e.droppingPK = old.Field.PrimaryKey && !new.Field.PrimaryKey
	e.newPK = nil
	leftover := ""
	if e.replacingPK || e.droppingPK {
		cols, err := e.Conn().Introspection().PrimaryKeyColumns(model.Table)
		if err != nil {
			return err
		}
		if len(cols) > 0 {
			// The key is replaced after the column rename, so the index
			// CockroachDB leaves behind carries the new column names.
			renamed := make([]string, len(cols))
			for i, c := range cols {
				if c == old.Column {
					c = new.Column
				}
				renamed[i] = c
			}
			leftover = base.TruncateName(model.Table+"_"+strings.Join(renamed, "_")+"_key", e.Conn().Backend.MaxNameLength, 4)
		}
		for _, f := range new.Model.PK() {
			e.newPK = append(e.newPK, f.Column)
		}
	}
	defer func() { e.replacingPK, e.droppingPK, e.newPK = false, false, nil }()
	if err := e.Editor.Editor.PerformAlterField(model, old, new, oldType, newType, strict); err != nil {
		return err
	}
	if leftover == "" {
		return nil
	}
	return e.Execute(fmt.Sprintf("DROP INDEX IF EXISTS %s@%s CASCADE", e.QuoteName(model.Table), e.QuoteName(leftover)))
}

// AlterColumnTypeSQL changes a column type. Serial columns are INT8 with
// DEFAULT unique_rowid() on CockroachDB, so turning a column into (or out
// of) a serial one sets or drops that default instead of managing a
// sequence the way PostgreSQL does.
//
// django: django_cockroachdb/schema.py DatabaseSchemaEditor._alter_column_type_sql
func (e *Editor) AlterColumnTypeSQL(model *m.Model, old, new *m.ModelField, newType string) (base.Fragment, []string, error) {
	oldType, err := e.ColumnType(model, old)
	if err != nil {
		return base.Fragment{}, nil, err
	}
	var other []string
	if e.Conn().Features().SupportsComments && old.Field.Comment != new.Field.Comment {
		s, err := e.AlterColumnCommentSQL(model, new, newType, new.Field.Comment)
		if err != nil {
			return base.Fragment{}, nil, err
		}
		other = append(other, s)
	}
	if oldType == newType {
		return base.Fragment{}, other, nil
	}
	oldBase, oldClause := postgresql.SplitType(oldType)
	newBase, newClause := postgresql.SplitType(newType)
	if strings.Contains(oldClause, "AS (") || strings.Contains(newClause, "AS (") {
		return base.Fragment{}, nil, &m.ValueError{Msg: fmt.Sprintf("modifying GeneratedFields is not supported - the field %s.%s must be removed and re-added with the new definition", model.Label(), new.Name)}
	}
	oldIsSerial := serialTypes[strings.ToLower(postgresql.BaseTypeName(oldBase))]
	newIsSerial := serialTypes[strings.ToLower(postgresql.BaseTypeName(newBase))]
	if oldIsSerial {
		oldBase = "bigint"
	}
	if newIsSerial {
		newBase = "bigint"
	}
	q := e.QuoteName
	table, column := q(model.Table), q(new.Column)
	var frag base.Fragment
	if oldBase != newBase {
		args := base.AlterColumnType{Column: column, Type: newBase}
		frag = base.Fragment{SQL: e.Grammar().AlterColumnType(args) + usingSQL(column, oldBase, newBase)}
	}
	switch {
	case newIsSerial && !oldIsSerial:
		other = append(other, fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s SET DEFAULT %s", table, column, serialDefault))
	case oldIsSerial && !newIsSerial:
		other = append(other, fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s DROP DEFAULT", table, column))
	}
	switch {
	case newClause != "" && oldClause == "":
		other = append(other, fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s ADD %s", table, column, newClause))
	case oldClause != "" && newClause == "":
		if err := e.Execute(fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s DROP IDENTITY IF EXISTS", table, column)); err != nil {
			return base.Fragment{}, nil, err
		}
	case oldClause != "" && newClause != "" && oldClause != newClause:
		mode := "BY DEFAULT"
		if strings.Contains(newClause, "ALWAYS") {
			mode = "ALWAYS"
		}
		other = append(other, fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s SET GENERATED %s", table, column, mode))
	}
	return frag, other, nil
}

// Grammar is CockroachDB's spelling. It is PostgreSQL's apart from the
// index statements, where an index name is scoped to its table.
type Grammar struct{ postgresql.Grammar }

func (Grammar) DropUnique(c base.DropConstraint) string {
	return "DROP INDEX " + c.Table + "@" + c.Name + " CASCADE"
}

func (Grammar) DropIndex(c base.DropIndex) string {
	if c.Concurrently {
		return "DROP INDEX CONCURRENTLY IF EXISTS " + c.Table + "@" + c.Name
	}
	return "DROP INDEX IF EXISTS " + c.Table + "@" + c.Name
}

func (Grammar) RenameIndex(c base.RenameIndex) string {
	return "ALTER INDEX " + c.Table + "@" + c.OldName + " RENAME TO " + c.NewName
}

// AddPrimaryKey replaces the primary key rather than adding one: every
// table here has one already.
func (Grammar) AddPrimaryKey(c base.AddPrimaryKey) string {
	return "ALTER TABLE " + c.Table + " ALTER PRIMARY KEY USING COLUMNS (" + c.Columns + ")"
}
