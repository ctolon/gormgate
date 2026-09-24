package conformance

func init() { Normalizers["mssql"] = sqlserver }

// sqlserver normalizes the constraint names SQL Server derives itself.
//
// A PRIMARY KEY written inside CREATE TABLE and a DEFAULT written next to a
// column definition are named by the server ("PK__pony__3213E83FA8335D4F"),
// and the suffix comes from the object id, so two tables with the same name
// in two databases never get the same name. Both are compared by what they
// cover instead: a primary key by its columns, a default constraint by the
// column it belongs to (the default value itself is compared as part of the
// column).
var sqlserver = &Normalizer{
	Constraint: func(table, name string, c Constraint) (string, bool) {
		switch {
		case c.PrimaryKey:
			return "<pk " + c.Columns + ">", true
		case !c.Unique && !c.Check && !c.Index && c.ForeignKey == "":
			// Only default constraints carry no other attribute.
			return "<default " + c.Columns + ">", true
		}
		return name, true
	},
}
