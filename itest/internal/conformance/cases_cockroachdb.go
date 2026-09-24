package conformance

import (
	"testing"

	"gorm.io/gorm"

	"github.com/ctolon/gormgate/backends/base"
	m "github.com/ctolon/gormgate/migrations"
)

// onlyCockroachDB skips a case on every other backend.
func onlyCockroachDB(b *base.Backend) string {
	if b.Vendor != "cockroachdb" {
		return "CockroachDB-specific case"
	}
	return ""
}

// CockroachDB has no ALTER TABLE ... DROP CONSTRAINT for a primary key and
// every table has one, so gormgate changes a primary key with ALTER PRIMARY
// KEY and drops the index CockroachDB leaves behind. Other backends name
// the recreated constraint differently from the one a fresh CREATE TABLE
// makes, which is why this case is CockroachDB-only.
func init() {
	Cases = append(Cases,
		func(p string, q func(string) string) Case {
			return Case{Name: "cockroach_add_column_to_primary_key",
				Skip: onlyCockroachDB,
				Setup: []m.Operation{&m.CreateModel{Name: "Pair", Table: p + "pair", Fields: m.Fields{
					col("a", m.Field{Type: m.Int, Size: 64, PrimaryKey: true}),
					col("b", strNN(20)),
					col("v", str(10)),
				}}},
				Ops: []m.Operation{&m.AlterField{ModelName: "pair", Name: "b",
					Field: m.Field{Type: m.String, Size: 20, PrimaryKey: true}}},
				Before: func(t testing.TB, db *gorm.DB) {
					exec(t, db, "INSERT INTO "+qt(db, p+"pair")+" ("+qt(db, "a")+", "+qt(db, "b")+") VALUES (1, 'x')")
				},
				After: func(t testing.TB, db *gorm.DB) {
					if n := count(t, db, "SELECT count(*) FROM "+qt(db, p+"pair")); n != 1 {
						t.Fatalf("rows after the primary key change: %d", n)
					}
				},
			}
		},
	)
}
