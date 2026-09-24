package conformance

import (
	"testing"

	"gorm.io/gorm"

	"github.com/ctolon/gormgate/backends/base"
	m "github.com/ctolon/gormgate/migrations"
	pg "github.com/ctolon/gormgate/migrations/postgres"
)

// The operations of migrations/postgres, the port of
// django.contrib.postgres.operations. Each case is gated to the vendors the
// operation counts as having the feature; the operation itself is a silent
// no-op everywhere else, which the unit tests of that package cover.
func init() { Cases = append(Cases, postgresOpCases...) }

// onlyPostgreSQL runs a case on PostgreSQL proper alone: CockroachDB has no
// extensions and no CREATE COLLATION, and openGauss has neither DROP
// EXTENSION nor user-defined collations.
func onlyPostgreSQL(b *base.Backend) string {
	if b.Vendor != "postgresql" {
		return "PostgreSQL-specific case"
	}
	return ""
}

// onlyPostgreSQLFamily runs a case on every backend that speaks
// PostgreSQL's dialect: CREATE INDEX CONCURRENTLY, DROP INDEX CONCURRENTLY,
// ADD CONSTRAINT ... NOT VALID and VALIDATE CONSTRAINT work on all three.
func onlyPostgreSQLFamily(b *base.Backend) string {
	switch b.Vendor {
	case "postgresql", "cockroachdb", "gaussdb":
		return ""
	}
	return "PostgreSQL-family case"
}

// extensionCount counts an installed extension.
func extensionCount(t testing.TB, db *gorm.DB, name string) int64 {
	return count(t, db, "SELECT count(*) FROM pg_extension WHERE extname = ?", name)
}

// collationCount counts a collation by name.
func collationCount(t testing.TB, db *gorm.DB, name string) int64 {
	return count(t, db, "SELECT count(*) FROM pg_collation WHERE collname = ?", name)
}

var postgresOpCases = []Builder{
	// An extension is installed on the way forwards and dropped again on
	// the way backwards. It is not part of the migration state, so the
	// schema snapshots are unchanged either way.
	func(p string, q func(string) string) Case {
		return Case{
			Name:  "postgres_create_extension",
			Skip:  onlyPostgreSQL,
			Setup: []m.Operation{pony(p)},
			Ops:   []m.Operation{pg.HStoreExtension()},
			After: func(t testing.TB, db *gorm.DB) {
				if n := extensionCount(t, db, "hstore"); n != 1 {
					t.Fatalf("hstore extensions after the migration: %d", n)
				}
			},
			AfterBackwards: func(t testing.TB, db *gorm.DB) {
				if n := extensionCount(t, db, "hstore"); n != 0 {
					t.Fatalf("hstore extensions after unapplying: %d", n)
				}
			},
		}
	},

	// An index created with CREATE INDEX CONCURRENTLY outside a
	// transaction has to come out exactly like the one a fresh CREATE
	// TABLE would build, which is what the case protocol compares.
	func(p string, q func(string) string) Case {
		return Case{
			Name:      "postgres_add_index_concurrently",
			Skip:      onlyPostgreSQLFamily,
			NonAtomic: true,
			Setup:     []m.Operation{pony(p, col("weight", intF(64)))},
			Ops: []m.Operation{&pg.AddIndexConcurrently{ModelName: "pony", Index: m.Index{
				Name: "idx_" + p + "pony_weight", Fields: m.Columns("weight"),
			}}},
		}
	},

	// The removal is the same statement the other way round: the index is
	// there after the setup and gone after the migration.
	func(p string, q func(string) string) Case {
		return Case{
			Name:      "postgres_remove_index_concurrently",
			Skip:      onlyPostgreSQLFamily,
			NonAtomic: true,
			Setup: []m.Operation{&m.CreateModel{Name: "Pony", Table: p + "pony",
				Fields: m.Fields{id(), col("name", str(100)), col("weight", intF(64))},
				Options: m.Options{Indexes: []m.Index{{
					Name: "idx_" + p + "pony_weight", Fields: m.Columns("weight"),
				}}},
			}},
			Ops: []m.Operation{&pg.RemoveIndexConcurrently{
				ModelName: "pony", Name: "idx_" + p + "pony_weight",
			}},
		}
	},

	// A collation is created on the way forwards and dropped again on the
	// way backwards; like an extension it is not part of the state.
	func(p string, q func(string) string) Case {
		name := p + "coll"
		return Case{
			Name:  "postgres_create_collation",
			Skip:  onlyPostgreSQL,
			Setup: []m.Operation{pony(p)},
			Ops:   []m.Operation{&pg.CreateCollation{Name: name, Locale: "C"}},
			After: func(t testing.TB, db *gorm.DB) {
				if n := collationCount(t, db, name); n != 1 {
					t.Fatalf("collations named %s after the migration: %d", name, n)
				}
			},
			AfterBackwards: func(t testing.TB, db *gorm.DB) {
				if n := collationCount(t, db, name); n != 0 {
					t.Fatalf("collations named %s after unapplying: %d", name, n)
				}
			},
		}
	},

	// A check constraint added NOT VALID accepts the rows already in the
	// table and rejects new ones; validating it afterwards leaves a
	// constraint indistinguishable from one a fresh CREATE TABLE makes.
	func(p string, q func(string) string) Case {
		name := "chk_" + p + "w"
		return Case{
			Name: "postgres_add_constraint_not_valid",
			Skip: skipAny(onlyPostgreSQLFamily,
				requires(func(f base.Features) bool { return f.SupportsTableCheckConstraints }, "no table check constraints")),
			Setup: []m.Operation{pony(p, col("weight", intNN(64)))},
			Before: func(t testing.TB, db *gorm.DB) {
				exec(t, db, "INSERT INTO "+qt(db, p+"pony")+" ("+qt(db, "name")+", "+qt(db, "weight")+") VALUES ('ok', 10)")
			},
			Ops: []m.Operation{
				&pg.AddConstraintNotValid{ModelName: "pony",
					Constraint: &m.CheckConstraint{Name: name, Check: q("weight") + " > 0"}},
				&pg.ValidateConstraint{ModelName: "pony", Name: name},
			},
			After: func(t testing.TB, db *gorm.DB) {
				if n := count(t, db, "SELECT count(*) FROM "+qt(db, p+"pony")); n != 1 {
					t.Fatalf("rows after the migration: %d", n)
				}
				err := db.Exec("INSERT INTO " + qt(db, p+"pony") + " (" + qt(db, "name") + ", " + qt(db, "weight") + ") VALUES ('bad', -1)").Error
				if err == nil {
					t.Fatal("the validated constraint accepted a violating row")
				}
			},
		}
	},
}
