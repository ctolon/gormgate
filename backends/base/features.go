package base

// Features mirrors django.db.backends.base.features.BaseDatabaseFeatures,
// limited to the flags the migration machinery consults.
type Features struct {
	SupportsTransactions bool
	// CanRollbackDDL is Django's can_rollback_ddl: DDL runs inside
	// transactions and is rolled back with them.
	CanRollbackDDL            bool
	SupportsCombinedAlters    bool
	SupportsForeignKeys       bool
	SupportsUniqueConstraints bool
	// CanCreateInlineFK: foreign keys are declared inside CREATE TABLE
	// instead of added afterwards (SQLite).
	CanCreateInlineFK                      bool
	SupportsComments                       bool
	SupportsCommentsInline                 bool
	SupportsPartialIndexes                 bool
	SupportsExpressionIndexes              bool
	SupportsCoveringIndexes                bool
	SupportsDeferrableUniqueConstraints    bool
	SupportsNullsDistinctUniqueConstraints bool
	SupportsIndexColumnOrdering            bool
	SupportsTableCheckConstraints          bool
	CanRenameIndex                         bool
	InterpretsEmptyStringsAsNulls          bool
	AllowsMultipleConstraintsOnSameFields  bool
	IgnoresTableNameCase                   bool
	// RefusesUnsupportedObjects is set where the backend's schema editor
	// raises migrations.NotSupportedError for a declared object it cannot
	// express, instead of leaving it to the base editor to skip silently
	// (ClickHouse). It is what tells "the migration stops here" apart from
	// "the schema will not have this": the same missing feature means the
	// first on such a backend and the second everywhere else.
	RefusesUnsupportedObjects bool
	// NoPlainIndexes is set where every index needs an explicit Index.Type
	// (ClickHouse only has data-skipping indexes: minmax, set(n),
	// bloom_filter, ...); ordinary column indexes don't exist.
	NoPlainIndexes bool
	// CannotAlterIntegerPrimaryKeyType is set where the type of an integer
	// primary key column can't be changed because the primary key is the
	// physical row key (TiDB's clustered index).
	CannotAlterIntegerPrimaryKeyType bool
	// UniqueConstraintsRejectMultipleNulls is set where a UNIQUE constraint
	// treats NULLs as equal, so at most one row of a nullable unique column
	// may be NULL (SQL Server).
	UniqueConstraintsRejectMultipleNulls bool
	// CannotAlterAutoIncrement is set where the auto-increment property of
	// an existing column can neither be added nor removed by an ALTER
	// statement (SQL Server's IDENTITY): the column has to be dropped and
	// added again.
	CannotAlterAutoIncrement bool
	// NoForeignKeyOnUpdate is set where a foreign key has no ON UPDATE
	// action at all (Oracle): a field declaring ForeignKey.OnUpdate is
	// rejected with a NotSupportedError instead of being ignored.
	NoForeignKeyOnUpdate bool
	// NoColumnNullability is set where NULL / NOT NULL is not a column
	// property: nullability is part of the column type (ClickHouse
	// Nullable(T)), so Field.Null has no DDL effect and there are no NULL
	// rows for a NULL -> NOT NULL alteration to fill.
	NoColumnNullability bool
	// NonSequentialAutoIncrement is set where an auto-increment column does
	// not count 1, 2, 3, ... but draws unordered values (CockroachDB's
	// DEFAULT unique_rowid()), so a row's key can't be predicted after an
	// INSERT that leaves it to the database.
	NonSequentialAutoIncrement bool
	// NoCastsToSizedStrings is set where the data of an existing column
	// can't be converted into a fixed-width string type, which is what gorm
	// maps a string field with a size to. On ClickHouse casting to
	// FixedString is "only implemented for types String and FixedString",
	// and a FixedString value is always padded to its full width, so it
	// never fits a narrower FixedString either.
	NoCastsToSizedStrings bool

	// The three below describe what the migrations table itself needs.
	// They are capabilities rather than a vendor test because that is what
	// the recorder is actually asking about.

	// PadsSizedStrings is set where a string column with a size pads what
	// it stores up to that width (ClickHouse FixedString pads with NUL),
	// so the recorder stores the app and migration names unsized and gets
	// back exactly what it wrote.
	PadsSizedStrings bool
	// NoAutoIncrementOnInsert is set where an auto-increment column does
	// not fill itself in, so the recorder has to compute the next id as
	// part of the INSERT.
	NoAutoIncrementOnInsert bool

	// SupportsExtensions is set where CREATE EXTENSION / DROP EXTENSION
	// work as they do on PostgreSQL. CockroachDB has no extensions in
	// that sense and openGauss accepts only the ones built into its own
	// installation, so both leave it false.
	SupportsExtensions bool
	// SupportsCollations is set where CREATE COLLATION / DROP COLLATION
	// work. Neither CockroachDB nor openGauss has them.
	SupportsCollations bool
}
