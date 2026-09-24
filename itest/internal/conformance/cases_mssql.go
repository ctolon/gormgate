package conformance

import (
	"github.com/ctolon/gormgate/backends/base"
	m "github.com/ctolon/gormgate/migrations"
)

// SQL Server refuses to alter a column while an index, a key, a check
// constraint or a default constraint depends on it, so the backend drops and
// recreates them around the ALTER. The cases below cover that; they are
// limited to SQL Server because no other backend needs the dance and the
// corpus in cases.go is what every backend has to pass.
func init() { Cases = append(Cases, mssqlCases...) }

// onlyMSSQL runs a case on the SQL Server backend alone.
func onlyMSSQL(b *base.Backend) string {
	if b.Vendor != "microsoft" {
		return "SQL Server specific"
	}
	return ""
}

var mssqlCases = []Builder{
	func(p string, q func(string) string) Case {
		return Case{Name: "mssql_alter_indexed_column", Skip: onlyMSSQL,
			Setup: []m.Operation{
				pony(p, col("n", intF(32))),
				&m.AddIndex{ModelName: "pony", Index: m.Index{Name: "idx_" + p + "n", Fields: []m.IndexField{{Column: "n", Sort: "DESC"}}}},
			},
			Ops: []m.Operation{&m.AlterField{ModelName: "pony", Name: "n", Field: intNN(64)}}}
	},
	func(p string, q func(string) string) Case {
		return Case{Name: "mssql_alter_indexed_column_null", Skip: onlyMSSQL,
			Setup: []m.Operation{
				pony(p, col("n", intF(32))),
				&m.AddIndex{ModelName: "pony", Index: m.Index{Name: "idx_" + p + "n", Fields: []m.IndexField{{Column: "n"}}}},
			},
			Ops: []m.Operation{&m.AlterField{ModelName: "pony", Name: "n", Field: intNN(32)}}}
	},
	func(p string, q func(string) string) Case {
		return Case{Name: "mssql_alter_unique_column_type", Skip: onlyMSSQL,
			Setup: []m.Operation{pony(p, col("code", m.Field{Type: m.String, Size: 10, Unique: true}))},
			Ops:   []m.Operation{&m.AlterField{ModelName: "pony", Name: "code", Field: m.Field{Type: m.String, Size: 30, Unique: true}}}}
	},
	func(p string, q func(string) string) Case {
		return Case{Name: "mssql_alter_column_of_unique_together", Skip: onlyMSSQL,
			Setup: []m.Operation{
				pony(p, col("color", str(20))),
				&m.AlterUniqueTogether{Name: "pony", UniqueTogether: [][]string{{"name", "color"}}},
			},
			Ops: []m.Operation{&m.AlterField{ModelName: "pony", Name: "color", Field: str(40)}}}
	},
	func(p string, q func(string) string) Case {
		return Case{Name: "mssql_alter_checked_column", Skip: onlyMSSQL,
			Setup: []m.Operation{
				// A 16-bit integer is a smallint and a 64-bit one a bigint;
				// gorm maps both 32-bit and 64-bit integers to bigint, so
				// those two would not change the column type at all.
				pony(p, col("weight", intF(16))),
				&m.AddConstraint{ModelName: "pony", Constraint: &m.CheckConstraint{Name: "chk_" + p + "w", Check: q("weight") + " > 0"}},
			},
			Ops: []m.Operation{&m.AlterField{ModelName: "pony", Name: "weight", Field: intF(64)}}}
	},
	func(p string, q func(string) string) Case {
		return Case{Name: "mssql_alter_column_keeps_db_default", Skip: onlyMSSQL,
			Setup: []m.Operation{pony(p, col("n", m.Field{Type: m.Int, Size: 16, Null: true, DBDefault: m.DBValue(int64(3))}))},
			Ops:   []m.Operation{&m.AlterField{ModelName: "pony", Name: "n", Field: m.Field{Type: m.Int, Size: 64, Null: true, DBDefault: m.DBValue(int64(3))}}}}
	},
	func(p string, q func(string) string) Case {
		return Case{Name: "mssql_alter_pk_type_with_incoming_fk", Skip: onlyMSSQL,
			Setup: []m.Operation{
				&m.CreateModel{Name: "Pony", Table: p + "pony", Fields: m.Fields{col("id", m.Field{Type: m.Int, Size: 8, PrimaryKey: true})}},
				&m.CreateModel{Name: "Rider", Table: p + "rider", Fields: m.Fields{id(), col("pony_id", m.Field{Type: m.Int, Size: 8, Null: true, ForeignKey: fk("Pony", "fk_"+p+"rider_pony")})}},
			},
			Ops: []m.Operation{
				&m.AlterField{ModelName: "pony", Name: "id", Field: m.Field{Type: m.Int, Size: 64, PrimaryKey: true}},
				&m.AlterField{ModelName: "rider", Name: "pony_id", Field: m.Field{Type: m.Int, Size: 64, Null: true, ForeignKey: fk("Pony", "fk_"+p+"rider_pony")}},
			}}
	},
	func(p string, q func(string) string) Case {
		return Case{Name: "mssql_add_identity_not_supported", Skip: onlyMSSQL,
			Setup: []m.Operation{
				&m.CreateModel{Name: "Pony", Table: p + "pony", Fields: m.Fields{col("id", m.Field{Type: m.Int, Size: 64, PrimaryKey: true})}},
			},
			ExpectError: "IDENTITY property",
			Ops:         []m.Operation{&m.AlterField{ModelName: "pony", Name: "id", Field: m.Field{Type: m.Int, Size: 64, PrimaryKey: true, AutoIncrement: true}}}}
	},
}
