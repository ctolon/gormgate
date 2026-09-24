package conformance

import (
	"testing"

	"gorm.io/gorm"

	"github.com/ctolon/gormgate/backends/base"
	m "github.com/ctolon/gormgate/migrations"
)

// Cases that a backend remaking whole tables (SQLite) has to survive: the
// table definition carries the indexes, the unique_together, the check
// constraints and the foreign keys, so rebuilding it must reproduce every
// one of them under its original name. They are ordinary operations on
// every other backend.
func init() {
	Cases = append(Cases,
		func(p string, q func(string) string) Case {
			return Case{Name: "table_with_everything_add_field",
				Skip: skipAny(skipNoUnique, skipNoPlainIndexes),
				Setup: []m.Operation{
					&m.CreateModel{Name: "Pony", Table: p + "pony", Fields: m.Fields{
						id(),
						col("name", str(100)),
						col("code", m.Field{Type: m.String, Size: 10, Null: true, Unique: true}),
						col("color", str(20)),
						col("weight", intF(32)),
					}, Options: m.Options{
						UniqueTogether: [][]string{{"name", "color"}},
						Indexes:        []m.Index{{Name: "idx_" + p + "pony_weight", Fields: []m.IndexField{{Column: "weight"}}}},
						Constraints:    []m.Constraint{&m.CheckConstraint{Name: "chk_" + p + "pony_weight", Check: q("weight") + " >= 0"}},
					}},
				},
				Ops: []m.Operation{
					&m.AddField{ModelName: "pony", Name: "height", Field: m.Field{Type: m.Int, Size: 32, Default: int64(3)}, PreserveDefault: m.Ptr(false)},
				},
				Before: func(t testing.TB, db *gorm.DB) {
					exec(t, db, "INSERT INTO "+qt(db, p+"pony")+" ("+qt(db, "name")+", "+qt(db, "weight")+") VALUES ('a', 5)")
				},
				After: func(t testing.TB, db *gorm.DB) {
					if n := count(t, db, "SELECT count(*) FROM "+qt(db, p+"pony")+" WHERE "+qt(db, "height")+" = 3 AND "+qt(db, "weight")+" = 5"); n != 1 {
						t.Fatalf("row not preserved with the one-off default")
					}
				},
			}
		},
		func(p string, q func(string) string) Case {
			return Case{Name: "self_fk_table_add_unique_field",
				// The two rows inserted below both get a NULL code, which a
				// unique constraint that treats NULLs as equal refuses.
				Skip: skipAny(skipNoForeignKeys, skipNoUnique,
					forbids(func(f base.Features) bool { return f.UniqueConstraintsRejectMultipleNulls },
						"a unique constraint allows only one NULL row"),
					forbids(func(f base.Features) bool { return f.NonSequentialAutoIncrement },
						"generated keys are not 1, 2, 3, ... so the references below can't be written down")),
				Setup: []m.Operation{
					pony(p, col("parent_id", m.Field{Type: m.Int, Size: 64, Null: true, ForeignKey: fk("Pony", "fk_"+p+"pony_parent")})),
					rider(p),
				},
				Ops: []m.Operation{
					&m.AddField{ModelName: "pony", Name: "code", Field: m.Field{Type: m.String, Size: 10, Null: true, Unique: true}},
				},
				Before: func(t testing.TB, db *gorm.DB) {
					// The keys are left to the database: an identity column
					// (SQL Server, Oracle) refuses an explicit value. Every
					// backend starts numbering at 1, which is what the
					// references below rely on.
					exec(t, db, "INSERT INTO "+qt(db, p+"pony")+" ("+qt(db, "name")+") VALUES ('a')")
					exec(t, db, "INSERT INTO "+qt(db, p+"pony")+" ("+qt(db, "name")+", "+qt(db, "parent_id")+") VALUES ('b', 1)")
					exec(t, db, "INSERT INTO "+qt(db, p+"rider")+" ("+qt(db, "pony_id")+") VALUES (2)")
				},
				After: func(t testing.TB, db *gorm.DB) {
					if n := count(t, db, "SELECT count(*) FROM "+qt(db, p+"pony")+" WHERE "+qt(db, "parent_id")+" = 1"); n != 1 {
						t.Fatalf("self reference lost")
					}
					if n := count(t, db, "SELECT count(*) FROM "+qt(db, p+"rider")+" WHERE "+qt(db, "pony_id")+" = 2"); n != 1 {
						t.Fatalf("incoming reference lost")
					}
				},
			}
		},
	)
}
