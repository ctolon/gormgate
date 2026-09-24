// Package postgresql is the PostgreSQL backend (gorm.io/driver/postgres,
// dialector "postgres").
//
// It is also the base of the backends for the forks that speak PostgreSQL's
// dialect: backends/cockroachdb and backends/gaussdb build their schema
// editor with NewEmbeddedEditor and pass their own SerialSQL, which is
// where the three of them part company over sequences and identity
// columns. CockroachDB reaches gorm through this same dialector, so the
// detector registered here declines a version string that names it.
//
// django: db/backends/postgresql/
package postgresql

import (
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"

	"github.com/ctolon/gormgate/backends/base"
	m "github.com/ctolon/gormgate/migrations"
)

// Features are PostgreSQL's DatabaseFeatures values.
//
// django: db/backends/postgresql/features.py
var Features = base.Features{
	SupportsExtensions:                     true,
	SupportsCollations:                     true,
	SupportsTransactions:                   true,
	CanRollbackDDL:                         true,
	SupportsCombinedAlters:                 true,
	SupportsForeignKeys:                    true,
	SupportsUniqueConstraints:              true,
	SupportsComments:                       true,
	SupportsPartialIndexes:                 true,
	SupportsExpressionIndexes:              true,
	SupportsCoveringIndexes:                true,
	SupportsDeferrableUniqueConstraints:    true,
	SupportsNullsDistinctUniqueConstraints: true,
	SupportsIndexColumnOrdering:            true,
	SupportsTableCheckConstraints:          true,
	CanRenameIndex:                         true,
	AllowsMultipleConstraintsOnSameFields:  true,
}

// Backend is the PostgreSQL backend definition.
var Backend = &base.Backend{
	Vendor:           "postgresql",
	DisplayName:      "PostgreSQL",
	Features:         Features,
	MaxNameLength:    63,
	NewIntrospection: func(c *base.Conn) base.Introspection { return &Introspection{Conn: c} },
	SequenceResetSQL: SequenceResetSQL,
	Ops:              Ops{},
}

func init() {
	Backend.NewEditor = func(c *base.Conn, collect, atomic bool) base.SchemaEditor {
		return NewEditor(c, collect, atomic)
	}
	base.Register(base.Detector{
		Name:         "postgresql",
		Priority:     base.PriorityFamily,
		Dialector:    "postgres",
		VersionQuery: "SELECT version()",
		Match:        func(v string) bool { return !strings.Contains(v, "CockroachDB") },
		Backend:      Backend,
	})
}

// Ops are PostgreSQL's DatabaseOperations helpers.
type Ops struct{}

// QuoteName quotes an identifier.
//
// django: postgresql/operations.py DatabaseOperations.quote_name
func (Ops) QuoteName(name string) string {
	if strings.HasPrefix(name, `"`) && strings.HasSuffix(name, `"`) {
		return name
	}
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// QuoteValue renders a literal (standard_conforming_strings is on).
func (Ops) QuoteValue(v any) (string, error) {
	return base.StandardQuoteValue(v, base.QuoteValueOptions{
		True: "true", False: "false",
		TimeFormat: "2006-01-02 15:04:05.999999999-07:00",
		Bytes: func(b []byte) string {
			return `'\x` + hex.EncodeToString(b) + `'::bytea`
		},
	})
}

// SplitOptions are PostgreSQL's lexical rules: dollar-quoted bodies, and
// no backslash escapes (standard_conforming_strings is on) and no backtick
// identifiers.
var SplitOptions = base.SplitOptions{DollarQuotes: true}

// PrepareSQLScript splits a script into statements.
func (Ops) PrepareSQLScript(script string) []string { return base.SplitSQL(script, SplitOptions) }

// StartTransactionSQL is used by sqlmigrate.
func (Ops) StartTransactionSQL() string { return "BEGIN;" }

// EndTransactionSQL is used by sqlmigrate.
func (Ops) EndTransactionSQL() string { return "COMMIT;" }

// SequenceResetSQL resets serial sequences to the maximum key.
//
// django: postgresql/operations.py DatabaseOperations.sequence_reset_sql
func SequenceResetSQL(c *base.Conn, style base.Style, models []*m.Model) ([]string, error) {
	q := Ops{}.QuoteName
	var out []string
	for _, model := range models {
		for _, f := range model.Fields {
			if !f.Field.AutoIncrement {
				continue
			}
			// The table and the column name are arguments of
			// pg_get_serial_sequence(), so they go into single-quoted
			// literals and a quote inside them has to be doubled.
			out = append(out, fmt.Sprintf("%s setval(pg_get_serial_sequence('%s','%s'), coalesce(max(%s), 1), max(%s) %s null) %s %s;",
				style.SQLKeyword("SELECT"), style.SQLTable(sqlLiteral(q(model.Table))), style.SQLField(sqlLiteral(f.Column)),
				style.SQLField(q(f.Column)), style.SQLField(q(f.Column)), style.SQLKeyword("IS NOT"),
				style.SQLKeyword("FROM"), style.SQLTable(q(model.Table))))
			break
		}
	}
	return out, nil
}

// sqlLiteral escapes a name that is interpolated into a single-quoted SQL
// literal rather than bound as a parameter.
func sqlLiteral(name string) string { return strings.ReplaceAll(name, "'", "''") }

// SerialSQL spells the statements behind a serial column. openGauss forked
// PostgreSQL 9.2 and writes both of them differently, so the PostgreSQL
// editor takes them as settings rather than hard-coding its own.
type SerialSQL struct {
	// TypedSequences reports that a sequence carries an integer type:
	// CREATE SEQUENCE ... AS <type> and ALTER SEQUENCE ... AS <type> are
	// PostgreSQL 10 syntax. Where it is false the sequence is created
	// untyped and a change between two serial widths needs no sequence
	// change at all.
	TypedSequences bool
	// Identity reports that the server has identity columns
	// (GENERATED ... AS IDENTITY). Where it is false, altering a column
	// into or out of one is an error.
	Identity bool
}

// StandardSerial is PostgreSQL's own spelling of the serial statements.
var StandardSerial = SerialSQL{TypedSequences: true, Identity: true}

// Editor is the PostgreSQL schema editor.
type Editor struct {
	*base.Editor
	// Serial is how this server spells the statements behind a serial
	// column; the constructors fill it in.
	Serial SerialSQL
	// concurrently is set while one of the CONCURRENTLY steps below runs,
	// so that the index SQL the shared algorithms build carries the
	// keyword. It stands for the concurrently=True keyword argument
	// Django's postgres operations pass to add_index and remove_index.
	//
	// django: postgresql/schema.py DatabaseSchemaEditor.add_index
	concurrently bool
}

// A method whose name does not match a step of base.Overrides is a
// method nothing calls; this makes that a build failure.
var _ base.Overrides = (*Editor)(nil)

// NewEditor builds a PostgreSQL schema editor.
func NewEditor(c *base.Conn, collect, atomic bool) *Editor {
	e := &Editor{Serial: StandardSerial}
	e.Editor = base.NewEditor(e, c, base.EditorOptions{
		CollectSQL: collect, Atomic: atomic, Grammar: Grammar{},
	})
	return e
}

// HasExtensions reports whether this server has CREATE EXTENSION.
func (e *Editor) HasExtensions() bool { return e.Conn().Backend.Features.SupportsExtensions }

// HasCollations reports whether this server has CREATE COLLATION.
func (e *Editor) HasCollations() bool { return e.Conn().Backend.Features.SupportsCollations }

// NewEmbeddedEditor builds a PostgreSQL editor for a backend that embeds
// it. outer is that backend's own editor, so the shared algorithms reach
// its overrides rather than PostgreSQL's; serial is how that server spells
// the statements behind a serial column. CockroachDB and openGauss are
// built this way. The caller applies its own templates on top.
func NewEmbeddedEditor(outer base.Overrides, c *base.Conn, serial SerialSQL, opts base.EditorOptions) *Editor {
	e := &Editor{Serial: serial}
	// An embedding backend that spells nothing differently gets
	// PostgreSQL's grammar; openGauss and CockroachDB are those backends.
	if opts.Grammar == nil {
		opts.Grammar = Grammar{}
	}
	e.Editor = base.NewEditor(outer, c, opts)
	return e
}

var (
	serialTypes = map[string]string{"smallserial": "smallint", "serial": "integer", "bigserial": "bigint"}
	typeParams  = regexp.MustCompile(`\(.*\)`)
)

// SplitType separates gorm's type strings into the base type and an
// identity/generated clause. The forks that speak PostgreSQL's dialect use
// it too.
func SplitType(t string) (baseType, clause string) {
	if i := strings.Index(t, " GENERATED "); i >= 0 {
		return t[:i], t[i+1:]
	}
	return t, ""
}

// BaseTypeName is a type name without its parameters, lower-cased, so that
// two spellings of the same type compare equal.
func BaseTypeName(t string) string {
	return strings.TrimSpace(typeParams.ReplaceAllString(strings.ToLower(t), ""))
}

// CreateIndexSQL renders CREATE INDEX with PostgreSQL's USING placement.
//
// django: postgresql/schema.py DatabaseSchemaEditor.sql_create_index
func (e *Editor) CreateIndexSQL(model *m.Model, ix m.Index) (*base.Statement, error) {
	st, err := e.Editor.CreateIndexSQL(model, ix)
	if err != nil {
		return nil, err
	}
	concurrently := ""
	option := ""
	if e.concurrently || strings.EqualFold(strings.TrimSpace(ix.Option), "CONCURRENTLY") {
		concurrently = "CONCURRENTLY "
	}
	if ix.Option != "" && !strings.EqualFold(strings.TrimSpace(ix.Option), "CONCURRENTLY") {
		option = " " + ix.Option
	}
	if a := st.Index(); a != nil {
		a.Concurrently, a.Option, a.Comment = concurrently != "", option, ""
		if ix.Type != "" {
			a.Using = " USING " + string(ix.Type)
		}
	}
	if len(ix.OpClasses) > 0 {
		if cols := st.Columns(); cols != nil {
			for i := range cols.Suffixes {
				if i < len(ix.OpClasses) && ix.OpClasses[i] != "" {
					cols.Suffixes[i] = strings.TrimSpace(ix.OpClasses[i] + " " + cols.Suffixes[i])
				}
			}
		}
	}
	return st, nil
}

// DeleteIndexSQL renders DROP INDEX, with CONCURRENTLY while one of the
// concurrent steps below is running.
//
// django: postgresql/schema.py DatabaseSchemaEditor.remove_index
func (e *Editor) DeleteIndexSQL(model *m.Model, name string) *base.Statement {
	st := e.Editor.DeleteIndexSQL(model, name)
	if a := st.DropIndexArgs(); e.concurrently && a != nil {
		a.Concurrently = true
	}
	return st
}

// AddIndexConcurrently creates an index with CREATE INDEX CONCURRENTLY.
// The statement cannot run inside a transaction; the operation that calls
// this has already refused one.
//
// django: postgresql/schema.py DatabaseSchemaEditor.add_index (concurrently=True)
func (e *Editor) AddIndexConcurrently(model *m.Model, ix m.Index) error {
	defer e.setConcurrently(true)()
	return e.Self().AddIndex(model, ix)
}

// RemoveIndexConcurrently drops an index with DROP INDEX CONCURRENTLY.
//
// django: postgresql/schema.py DatabaseSchemaEditor.remove_index (concurrently=True)
func (e *Editor) RemoveIndexConcurrently(model *m.Model, ix m.Index) error {
	defer e.setConcurrently(true)()
	return e.Self().RemoveIndex(model, ix)
}

// setConcurrently sets the flag and returns the function that restores it.
func (e *Editor) setConcurrently(v bool) func() {
	old := e.concurrently
	e.concurrently = v
	return func() { e.concurrently = old }
}

// AddConstraintNotValid adds a table constraint with PostgreSQL's NOT VALID
// suffix, which records the constraint without checking the rows already in
// the table.
//
// django: contrib/postgres/operations.py AddConstraintNotValid.database_forwards
func (e *Editor) AddConstraintNotValid(model *m.Model, c m.Constraint) error {
	st, err := e.Self().CreateConstraintSQL(model, c)
	if err != nil || st == nil {
		return err
	}
	return e.Execute(st.String() + " NOT VALID")
}

// ValidateConstraint checks the rows of a constraint added NOT VALID and
// marks it valid.
//
// django: contrib/postgres/operations.py ValidateConstraint.database_forwards
func (e *Editor) ValidateConstraint(model *m.Model, name string) error {
	q := e.QuoteName
	return e.Execute("ALTER TABLE " + q(model.Table) + " VALIDATE CONSTRAINT " + q(name))
}

// AlterColumnTypeSQL handles serial and identity columns and adds USING
// casts, like Django's PostgreSQL _alter_column_type_sql. What the serial
// statements look like, and whether the server has identity columns at all,
// comes from e.Serial.
//
// django: postgresql/schema.py DatabaseSchemaEditor._alter_column_type_sql
func (e *Editor) AlterColumnTypeSQL(model *m.Model, old, new *m.ModelField, newType string) (base.Fragment, []string, error) {
	backend := e.Conn().Backend
	oldType, err := e.ColumnType(model, old)
	if err != nil {
		return base.Fragment{}, nil, err
	}
	var other []string
	if backend.Features.SupportsComments && old.Field.Comment != new.Field.Comment {
		s, err := e.AlterColumnCommentSQL(model, new, newType, new.Field.Comment)
		if err != nil {
			return base.Fragment{}, nil, err
		}
		other = append(other, s)
	}
	if oldType == newType {
		return base.Fragment{}, other, nil
	}
	oldBase, oldClause := SplitType(oldType)
	newBase, newClause := SplitType(newType)
	if strings.Contains(oldClause, "AS (") || strings.Contains(newClause, "AS (") {
		// A server without identity columns still has GENERATED ALWAYS AS
		// (...) STORED, and like PostgreSQL it cannot turn a column into
		// one or out of one.
		return base.Fragment{}, nil, &m.ValueError{Msg: fmt.Sprintf("modifying GeneratedFields is not supported - the field %s.%s must be removed and re-added with the new definition", model.Label(), new.Name)}
	}
	if !e.Serial.Identity && (oldClause != "" || newClause != "") {
		// "ERROR: syntax error at or near "by"" for
		// GENERATED BY DEFAULT AS IDENTITY: the server has no identity
		// columns, only serial types backed by a sequence.
		return base.Fragment{}, nil, &m.ValueError{Msg: fmt.Sprintf(
			"%s has no identity columns; the field %s.%s cannot be altered into or out of one",
			backend.DisplayName, model.Label(), new.Name)}
	}
	oldSerial, oldIsSerial := serialTypes[strings.ToLower(oldBase)]
	newSerial, newIsSerial := serialTypes[strings.ToLower(newBase)]
	if oldIsSerial {
		oldBase = oldSerial
	}
	if newIsSerial {
		newBase = newSerial
	}
	q := e.QuoteName
	table, column := q(model.Table), q(new.Column)
	var frag base.Fragment
	if oldBase != newBase {
		args := base.AlterColumnType{Column: column, Type: newBase}
		s := e.Grammar().AlterColumnType(args)
		if BaseTypeName(oldBase) != BaseTypeName(newBase) {
			s += fmt.Sprintf(" USING %s::%s", column, newBase)
		}
		frag = base.Fragment{SQL: s}
	}
	seq := func() (string, error) {
		seqs, err := e.Conn().Introspection().Sequences(model.Table)
		if err != nil {
			return "", err
		}
		for _, s := range seqs {
			if s.Column == old.Column || s.Column == new.Column {
				return s.Name, nil
			}
		}
		return "", nil
	}
	switch {
	case newIsSerial && !oldIsSerial:
		name := base.TruncateName(model.Table+"_"+new.Column+"_seq", backend.MaxNameLength, 4)
		create := fmt.Sprintf("CREATE SEQUENCE IF NOT EXISTS %s", q(name))
		if e.Serial.TypedSequences {
			create += " AS " + newBase
		}
		other = append(other,
			create,
			fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s SET DEFAULT nextval('%s')", table, column, sqlLiteral(q(name))),
			fmt.Sprintf("ALTER SEQUENCE %s OWNED BY %s.%s", q(name), table, column),
			fmt.Sprintf("SELECT setval('%s', coalesce(max(%s), 0) + 1, false) FROM %s", sqlLiteral(q(name)), column, table),
		)
	case oldIsSerial && !newIsSerial:
		name, err := seq()
		if err != nil {
			return base.Fragment{}, nil, err
		}
		other = append(other, fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s DROP DEFAULT", table, column))
		if name != "" {
			other = append(other, fmt.Sprintf("DROP SEQUENCE IF EXISTS %s CASCADE", q(name)))
		}
	case oldIsSerial && newIsSerial && oldBase != newBase && e.Serial.TypedSequences:
		name, err := seq()
		if err != nil {
			return base.Fragment{}, nil, err
		}
		if name != "" {
			other = append(other, fmt.Sprintf("ALTER SEQUENCE IF EXISTS %s AS %s", q(name), newBase))
		}
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

// Grammar is PostgreSQL's spelling of the statements gormgate emits.
type Grammar struct{ base.BaseGrammar }

// CreateIndex puts the access method before the column list, and has no
// index comment clause.
func (Grammar) CreateIndex(c base.CreateIndex) string {
	concurrently := ""
	if c.Concurrently {
		concurrently = "CONCURRENTLY "
	}
	return "CREATE " + base.IndexClass(c) + "INDEX " + concurrently + c.Name + " ON " + c.Table +
		c.Using + " (" + c.Columns + ")" + c.Include + c.Option + c.Condition
}

// DropIndex takes IF EXISTS, and CONCURRENTLY while a concurrent step is
// running.
func (Grammar) DropIndex(c base.DropIndex) string {
	if c.Concurrently {
		return "DROP INDEX CONCURRENTLY IF EXISTS " + c.Name
	}
	return "DROP INDEX IF EXISTS " + c.Name
}

// DropTable takes CASCADE so that the views and constraints that depend on
// the table go with it, as Django's PostgreSQL editor does.
func (Grammar) DropTable(c base.DropTable) string { return "DROP TABLE " + c.Table + " CASCADE" }
