package conformance

import (
	"testing"

	"gorm.io/gorm"

	"github.com/ctolon/gormgate/backends/base"
	m "github.com/ctolon/gormgate/migrations"
)

// ClickHouse has no ordinary indexes and no foreign keys, so most of the
// index and relation cases of the corpus in cases.go are skipped on it. The
// cases below cover, with the same forwards/backwards/from-scratch/replayed
// -SQL protocol, what it does have instead: a table engine of its own,
// typed data-skipping indexes and a table rename without any foreign key
// around.
func init() { Cases = append(Cases, clickhouseCases...) }

// onlyClickHouse runs a case on the ClickHouse backend alone.
func onlyClickHouse(b *base.Backend) string {
	if b.Vendor != "clickhouse" {
		return "ClickHouse specific"
	}
	return ""
}

var clickhouseCases = []Builder{
	func(p string, q func(string) string) Case {
		return Case{Name: "clickhouse_create_model_with_engine", Skip: onlyClickHouse,
			Ops: []m.Operation{
				&m.CreateModel{Name: "Event", Table: p + "event", Fields: m.Fields{
					col("day", m.Field{Type: m.Time}),
					col("id", m.Field{Type: m.Int, Size: 64, PrimaryKey: true}),
					col("payload", m.Field{Type: m.String}),
				}, Options: m.Options{
					ClickHouse: &m.ClickHouseTable{
						Engine:      "MergeTree()",
						PartitionBy: "toYYYYMM(" + q("day") + ")",
						OrderBy:     "(" + q("day") + ", " + q("id") + ")",
						Settings:    "index_granularity = 4096",
					},
					Indexes: []m.Index{{
						Name: "idx_" + p + "event_payload", Type: "minmax", Option: "GRANULARITY 4",
						Fields: []m.IndexField{{Column: "payload"}},
					}},
					Constraints: []m.Constraint{&m.CheckConstraint{Name: "chk_" + p + "event_id", Check: q("id") + " > 0"}},
				}},
			}}
	},
	func(p string, q func(string) string) Case {
		return Case{Name: "clickhouse_add_remove_data_skipping_index", Skip: onlyClickHouse,
			Setup: []m.Operation{pony(p, col("weight", intF(32)))},
			Ops: []m.Operation{
				&m.AddIndex{ModelName: "pony", Index: m.Index{
					Name: "idx_" + p + "a", Type: "minmax", Fields: []m.IndexField{{Column: "weight"}}}},
				&m.AddIndex{ModelName: "pony", Index: m.Index{
					Name: "idx_" + p + "b", Type: "set(100)", Option: "5",
					Fields: []m.IndexField{{Column: "name"}, {Column: "weight"}}}},
				&m.AddIndex{ModelName: "pony", Index: m.Index{
					Name: "idx_" + p + "c", Type: "bloom_filter",
					Fields: []m.IndexField{{Expression: "lower(" + q("name") + ")"}}}},
			}}
	},
	func(p string, q func(string) string) Case {
		return Case{Name: "clickhouse_remove_data_skipping_index", Skip: onlyClickHouse,
			Setup: []m.Operation{
				pony(p),
				&m.AddIndex{ModelName: "pony", Index: m.Index{
					Name: "idx_" + p + "a", Type: "minmax", Fields: []m.IndexField{{Column: "name"}}}},
			},
			Ops: []m.Operation{&m.RemoveIndex{ModelName: "pony", Name: "idx_" + p + "a"}}}
	},
	func(p string, q func(string) string) Case {
		return Case{Name: "clickhouse_rename_table_with_data", Skip: onlyClickHouse,
			Setup: []m.Operation{pony(p)},
			Ops: []m.Operation{
				&m.AlterModelTable{Name: "pony", Table: p + "pony2"},
				&m.AlterModelTableComment{Name: "pony", TableComment: "ponies' table"},
			},
			Before: func(t testing.TB, db *gorm.DB) {
				exec(t, db, "INSERT INTO "+qt(db, p+"pony")+" ("+qt(db, "id")+", "+qt(db, "name")+") VALUES (1, 'a')")
			},
			After: func(t testing.TB, db *gorm.DB) {
				if n := count(t, db, "SELECT count(*) FROM "+qt(db, p+"pony2")+" WHERE "+qt(db, "name")+" = 'a'"); n != 1 {
					t.Fatalf("rows after rename: %d", n)
				}
			},
			AfterBackwards: func(t testing.TB, db *gorm.DB) {
				if n := count(t, db, "SELECT count(*) FROM "+qt(db, p+"pony")); n != 1 {
					t.Fatalf("rows after renaming back: %d", n)
				}
			},
		}
	},
}
