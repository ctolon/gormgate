package migrations

// Meta carries model options that gorm tags can't express. A model provides
// it by implementing MetaProvider.
type Meta struct {
	// Managed set to false makes gormgate track the model without
	// creating or altering its table. nil means true.
	Managed *bool
	// DBTableComment is the comment set on the model's table.
	DBTableComment string
	// UniqueTogether lists groups of field names that must be unique in
	// combination.
	UniqueTogether [][]string
	// Indexes and Constraints are added to the ones derived from gorm tags.
	Indexes     []Index
	Constraints []Constraint
	// ClickHouse carries the table settings only ClickHouse uses.
	ClickHouse *ClickHouseTable
}

// MetaProvider is implemented by models that declare Meta options.
type MetaProvider interface {
	MigrationMeta() Meta
}
