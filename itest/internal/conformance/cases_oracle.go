package conformance

import (
	"github.com/ctolon/gormgate/backends/base"
	m "github.com/ctolon/gormgate/migrations"
)

// Oracle-specific cases. They run on every backend whose Features declare
// the limitation they cover, and are skipped everywhere else.
func init() {
	Cases = append(Cases,
		// Oracle's REFERENCES clause has no ON UPDATE action: gorm-oracle
		// silently replaces it with an AFTER UPDATE trigger on the parent
		// table, gormgate refuses the field instead of migrating something
		// the migration state does not describe.
		func(p string, q func(string) string) Case {
			return Case{
				Name:        "oracle_fk_on_update_not_supported",
				Skip:        requires(func(f base.Features) bool { return f.NoForeignKeyOnUpdate }, "foreign keys have an ON UPDATE action"),
				Setup:       []m.Operation{pony(p)},
				ExpectError: "foreign keys have no ON UPDATE action",
				Ops: []m.Operation{&m.CreateModel{Name: "Rider", Table: p + "rider", Fields: m.Fields{
					id(),
					col("pony_id", m.Field{Type: m.Int, Size: 64, Null: true, ForeignKey: &m.ForeignKey{
						To: "Pony", ToField: "id", Name: "fk_" + p + "rider_pony", OnUpdate: m.Cascade,
					}}),
				}}},
			}
		},
	)
}
