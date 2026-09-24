// Package oracle is the Oracle Database backend
// (github.com/oracle-samples/gorm-oracle, dialector "oracle").
//
// Two properties of the server shape everything here. Every DDL statement
// commits the open transaction, so no migration can be rolled back
// (CanRollbackDDL is false) and a failure leaves the statements that
// already ran in place. And the empty string is NULL, which
// InterpretsEmptyStringsAsNulls reports: a NOT NULL on a string column
// would reject values Django and gorm both consider present, so it is not
// emitted, and a nullability change on such a column is a no-op.
//
// django: db/backends/oracle/
package oracle

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ctolon/gormgate/backends/base"
	m "github.com/ctolon/gormgate/migrations"
)

// MaxNameLength is the identifier length limit of Oracle 12.2 and later.
//
// Django's Oracle backend still uses 30 (the Oracle 11 limit) in
// operations.max_name_length() and truncates every identifier to it.
// gormgate must generate the names gorm's NamingStrategy generates, which
// truncates nothing, so identifiers are only shortened where the database
// itself would reject them.
const MaxNameLength = 128

// Features are Oracle's DatabaseFeatures values.
//
// django: db/backends/oracle/features.py
var Features = base.Features{
	SupportsTransactions: true,
	// Every Oracle DDL statement commits the open transaction implicitly,
	// so a migration can't be rolled back.
	CanRollbackDDL: false,
	// ALTER TABLE takes one MODIFY clause at a time here: Django's Oracle
	// editor does not set supports_combined_alters either.
	SupportsCombinedAlters:    false,
	SupportsForeignKeys:       true,
	SupportsUniqueConstraints: true,
	CanCreateInlineFK:         false,
	SupportsComments:          true,
	// COMMENT ON is a separate statement; Oracle has no inline column
	// comment syntax.
	SupportsCommentsInline: false,
	// Oracle has no partial (filtered) indexes.
	SupportsPartialIndexes: false,
	// Function-based indexes.
	SupportsExpressionIndexes:              true,
	SupportsCoveringIndexes:                false,
	SupportsDeferrableUniqueConstraints:    true,
	SupportsNullsDistinctUniqueConstraints: false,
	SupportsIndexColumnOrdering:            true,
	SupportsTableCheckConstraints:          true,
	CanRenameIndex:                         true,
	// Oracle stores '' as NULL, so a string column can never be NOT NULL
	// without making the empty string unstorable (ORA-01400).
	InterpretsEmptyStringsAsNulls:         true,
	AllowsMultipleConstraintsOnSameFields: false,
	// Django uppercases every identifier, which makes table names case
	// insensitive for it. gorm-oracle quotes them verbatim, so gormgate
	// treats them as case sensitive.
	IgnoresTableNameCase: false,
	// Oracle's REFERENCES clause has no ON UPDATE action at all.
	NoForeignKeyOnUpdate: true,
}

// Backend is the Oracle backend definition.
var Backend = &base.Backend{
	Vendor:           "oracle",
	DisplayName:      "Oracle",
	Features:         Features,
	MaxNameLength:    MaxNameLength,
	NewIntrospection: func(c *base.Conn) base.Introspection { return &Introspection{Conn: c} },
	SequenceResetSQL: SequenceResetSQL,
	Ops:              Ops{},
}

// VersionQuery reads the server banner.
const VersionQuery = "SELECT banner_full FROM v$version"

func init() {
	Backend.NewEditor = func(c *base.Conn, collect, atomic bool) base.SchemaEditor {
		return NewEditor(c, collect, atomic)
	}
	base.Register(base.Detector{
		Name:         "oracle",
		Dialector:    "oracle",
		VersionQuery: VersionQuery,
		Resolve: func(c *base.Conn, version string) (*base.Backend, error) {
			if v, ok := ParseVersion(version); ok && v < 19 {
				return nil, &m.NotSupportedError{Msg: fmt.Sprintf("Oracle 19 or later is required (found %d)", v)}
			}
			return Backend, nil
		},
	})
}

var versionRe = regexp.MustCompile(`(\d+)\.\d+`)

// ParseVersion extracts the major release from a banner such as
// "Oracle Database 23ai Free Release 23.26.3.0.0".
//
// django: oracle/base.py DatabaseWrapper.oracle_version
func ParseVersion(banner string) (int, bool) {
	mt := versionRe.FindStringSubmatch(banner)
	if mt == nil {
		return 0, false
	}
	v, err := strconv.Atoi(mt[1])
	return v, err == nil
}

// Ops are Oracle's DatabaseOperations helpers.
type Ops struct{}

// QuoteName quotes an identifier.
//
// Django's quote_name() upper-cases and truncates to 30 characters;
// gorm-oracle's Dialector.QuoteTo writes the name verbatim between double
// quotes, and gormgate has to address the very objects gorm creates.
//
// django: oracle/operations.py DatabaseOperations.quote_name
func (Ops) QuoteName(name string) string {
	if len(name) >= 2 && strings.HasPrefix(name, `"`) && strings.HasSuffix(name, `"`) {
		return name // Quoting once is enough.
	}
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// QuoteValue renders a literal.
//
// django: oracle/schema.py DatabaseSchemaEditor.quote_value
func (Ops) QuoteValue(v any) (string, error) {
	switch x := v.(type) {
	case bool:
		if x {
			return "1", nil
		}
		return "0", nil
	case time.Time:
		return base.QuoteString(x.Format("2006-01-02 15:04:05.999999")), nil
	case []byte:
		return base.QuoteString(fmt.Sprintf("%x", x)), nil
	}
	return base.StandardQuoteValue(v, base.QuoteValueOptions{
		True: "1", False: "0", TimeFormat: "2006-01-02 15:04:05.999999",
	})
}

// SplitOptions are Oracle's lexical rules: plain standard SQL, with
// neither backslash escapes nor backtick identifiers nor dollar quoting.
var SplitOptions = base.SplitOptions{}

// PrepareSQLScript splits a script into statements.
func (Ops) PrepareSQLScript(script string) []string { return base.SplitSQL(script, SplitOptions) }

// StartTransactionSQL is used by sqlmigrate; Oracle has no statement that
// starts a transaction.
//
// django: oracle/operations.py DatabaseOperations.start_transaction_sql
func (Ops) StartTransactionSQL() string { return "" }

// EndTransactionSQL is used by sqlmigrate.
//
// django: base/operations.py BaseDatabaseOperations.end_transaction_sql
func (Ops) EndTransactionSQL() string { return "COMMIT;" }

// sequenceResetSQL is the PL/SQL block Django's sqlsequencereset prints for
// Oracle: it advances the identity sequence of the table's auto-increment
// column until it is past the largest key in the table.
//
// django: oracle/operations.py DatabaseOperations._sequence_reset_sql
const sequenceResetSQL = `
DECLARE
    table_value integer;
    seq_value integer;
    seq_name user_tab_identity_cols.sequence_name%%TYPE;
BEGIN
    BEGIN
        SELECT sequence_name INTO seq_name FROM user_tab_identity_cols
        WHERE  table_name = '%[1]s' AND
               column_name = '%[2]s';
        EXCEPTION WHEN NO_DATA_FOUND THEN
            seq_name := '%[3]s';
    END;

    SELECT NVL(MAX(%[4]s), 0) INTO table_value FROM %[5]s;
    SELECT NVL(last_number - cache_size, 0) INTO seq_value FROM user_sequences
           WHERE sequence_name = seq_name;
    WHILE table_value > seq_value LOOP
        EXECUTE IMMEDIATE 'SELECT "'||seq_name||'".nextval%[6]s'
        INTO seq_value;
    END LOOP;
END;
/`

// SequenceResetSQL returns one PL/SQL block per model with an
// auto-increment column.
//
// django: oracle/operations.py DatabaseOperations.sequence_reset_sql
func SequenceResetSQL(c *base.Conn, style base.Style, models []*m.Model) ([]string, error) {
	q := Ops{}.QuoteName
	suffix := " FROM DUAL"
	if v, ok := ParseVersion(c.Version); ok && v >= 23 {
		suffix = ""
	}
	var out []string
	for _, model := range models {
		for _, f := range model.Fields {
			if !f.Field.AutoIncrement {
				continue
			}
			// The table, the column and the sequence name are compared
			// against catalog columns inside single-quoted literals, so a
			// quote inside them has to be doubled.
			out = append(out, fmt.Sprintf(sequenceResetSQL,
				sqlLiteral(model.Table), sqlLiteral(f.Column),
				sqlLiteral(noAutofieldSequenceName(model.Table)),
				style.SQLField(q(f.Column)), style.SQLTable(q(model.Table)), suffix))
			// Only one auto-increment column is allowed per table.
			break
		}
	}
	return out, nil
}

// sqlLiteral escapes a name that is interpolated into a single-quoted SQL
// literal rather than bound as a parameter.
func sqlLiteral(name string) string { return strings.ReplaceAll(name, "'", "''") }

// noAutofieldSequenceName is the name of the sequence Django creates for
// auto-increment columns that are not Oracle identity columns.
//
// django: oracle/operations.py DatabaseOperations._get_no_autofield_sequence_name
func noAutofieldSequenceName(table string) string {
	return strings.ToUpper(base.TruncateName(table, MaxNameLength-3, 4)) + "_SQ"
}

// oraCodeRe matches the ORA-nnnnn prefix of an Oracle error message.
var oraCodeRe = regexp.MustCompile(`ORA-(\d{5})`)

// oraCode reports whether err is an Oracle error with one of the codes.
func oraCode(err error, codes ...int) bool {
	if err == nil {
		return false
	}
	mt := oraCodeRe.FindStringSubmatch(err.Error())
	if mt == nil {
		return false
	}
	got, _ := strconv.Atoi(mt[1])
	for _, c := range codes {
		if c == got {
			return true
		}
	}
	return false
}
