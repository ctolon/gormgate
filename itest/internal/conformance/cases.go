package conformance

import (
	"testing"

	"gorm.io/gorm"

	"github.com/ctolon/gormgate/backends/base"
	m "github.com/ctolon/gormgate/migrations"
)

// Builder makes a case for a table-name prefix (so cases never collide).
type Builder func(p string, q func(string) string) Case

func id() m.NamedField {
	return m.NamedField{Name: "id", Field: m.Field{Type: m.Int, Size: 64, PrimaryKey: true, AutoIncrement: true}}
}

func col(name string, f m.Field) m.NamedField { return m.NamedField{Name: name, Field: f} }

func str(size int) m.Field   { return m.Field{Type: m.String, Size: size, Null: true} }
func strNN(size int) m.Field { return m.Field{Type: m.String, Size: size} }
func intF(size int) m.Field  { return m.Field{Type: m.Int, Size: size, Null: true} }
func intNN(size int) m.Field { return m.Field{Type: m.Int, Size: size} }
func fk(to, name string) *m.ForeignKey {
	return &m.ForeignKey{To: to, ToField: "id", Name: name}
}

func pony(p string, extra ...m.NamedField) *m.CreateModel {
	fs := m.Fields{id(), col("name", str(100))}
	fs = append(fs, extra...)
	return &m.CreateModel{Name: "Pony", Table: p + "pony", Fields: fs}
}

func rider(p string, extra ...m.NamedField) *m.CreateModel {
	fs := m.Fields{id(), col("pony_id", m.Field{Type: m.Int, Size: 64, Null: true, ForeignKey: fk("Pony", "fk_"+p+"rider_pony")})}
	fs = append(fs, extra...)
	return &m.CreateModel{Name: "Rider", Table: p + "rider", Fields: fs}
}

// compositeFKSetup creates the two models of the composite foreign key
// cases: a barn identified by (tenant_id, code) and a stall holding both
// columns of that key.
func compositeFKSetup(p string) []m.Operation {
	return []m.Operation{
		&m.CreateModel{Name: "Barn", Table: p + "barn", Fields: m.Fields{
			col("tenant_id", m.Field{Type: m.Int, Size: 64, PrimaryKey: true}),
			col("code", m.Field{Type: m.String, Size: 20, PrimaryKey: true}),
			col("label", str(50)),
		}},
		&m.CreateModel{Name: "Stall", Table: p + "stall", Fields: m.Fields{
			id(),
			col("barn_tenant_id", intF(64)),
			col("barn_code", str(20)),
		}},
	}
}

// compositeFK is the foreign key the composite cases add and remove.
func compositeFK(p string) *m.ForeignKeyConstraint {
	return &m.ForeignKeyConstraint{
		Name:     "fk_" + p + "stall_barn",
		Fields:   []string{"barn_tenant_id", "barn_code"},
		To:       "Barn",
		ToFields: []string{"tenant_id", "code"},
	}
}

func exec(t testing.TB, db *gorm.DB, sql string, args ...any) {
	t.Helper()
	if err := db.Exec(sql, args...).Error; err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func count(t testing.TB, db *gorm.DB, sql string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := db.Raw(sql, args...).Row().Scan(&n); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return n
}

// forbids skips a case on backends whose Features set a limitation flag.
func forbids(flag func(base.Features) bool, reason string) func(*base.Backend) string {
	return func(b *base.Backend) string {
		if flag(b.Features) {
			return reason
		}
		return ""
	}
}

func requires(flag func(base.Features) bool, reason string) func(*base.Backend) string {
	return func(b *base.Backend) string {
		if !flag(b.Features) {
			return reason
		}
		return ""
	}
}

// skipAutoIncrementAlter skips the two cases that add or drop the
// auto-increment property of a primary key column.
var skipAutoIncrementAlter = func(b *base.Backend) string {
	if b.Features.CannotAlterIntegerPrimaryKeyType {
		return "the type of an integer primary key column can't be changed"
	}
	if b.Features.CannotAlterAutoIncrement {
		return "the auto-increment property of an existing column can't be added or removed"
	}
	return ""
}

// skipAny returns the reason of the first skip function that gives one.
func skipAny(fs ...func(*base.Backend) string) func(*base.Backend) string {
	return func(b *base.Backend) string {
		for _, f := range fs {
			if reason := f(b); reason != "" {
				return reason
			}
		}
		return ""
	}
}

// skipNoForeignKeys skips the cases whose models are linked by a foreign key.
var skipNoForeignKeys = requires(func(f base.Features) bool { return f.SupportsForeignKeys }, "no foreign keys")

// skipNoUnique skips the cases needing a unique constraint or unique index.
var skipNoUnique = requires(func(f base.Features) bool { return f.SupportsUniqueConstraints }, "no unique constraints")

// skipNoPlainIndexes skips the cases creating an index without an explicit
// type (ClickHouse only has typed data-skipping indexes).
var skipNoPlainIndexes = forbids(func(f base.Features) bool { return f.NoPlainIndexes }, "every index needs an explicit type")

// Cases is the conformance corpus.
var Cases = []Builder{
	func(p string, q func(string) string) Case {
		return Case{Name: "create_model_all_types", Ops: []m.Operation{
			&m.CreateModel{Name: "Thing", Table: p + "thing", Fields: m.Fields{
				id(),
				col("b", m.Field{Type: m.Bool, Null: true}),
				col("i8", intF(8)), col("i16", intF(16)), col("i32", intF(32)), col("i64", intF(64)),
				col("u32", m.Field{Type: m.Uint, Size: 32, Null: true}),
				col("f32", m.Field{Type: m.Float, Size: 32, Null: true}),
				col("f64", m.Field{Type: m.Float, Size: 64, Null: true}),
				col("dec", m.Field{Type: m.Float, Size: 64, Precision: 10, Scale: 2, Null: true}),
				col("s", str(50)), col("txt", m.Field{Type: m.String, Null: true}),
				col("t", m.Field{Type: m.Time, Null: true}),
				col("bin", m.Field{Type: m.Bytes, Null: true}),
			}},
		}}
	},
	func(p string, q func(string) string) Case {
		return Case{Name: "create_model_composite_pk", Ops: []m.Operation{
			&m.CreateModel{Name: "Pair", Table: p + "pair", Fields: m.Fields{
				col("a", m.Field{Type: m.Int, Size: 64, PrimaryKey: true}),
				col("b", m.Field{Type: m.String, Size: 20, PrimaryKey: true}),
				col("v", str(10)),
			}},
		}}
	},
	func(p string, q func(string) string) Case {
		return Case{Name: "create_model_with_constraints",
			Skip: skipAny(skipNoUnique, skipNoPlainIndexes), Ops: []m.Operation{
				&m.CreateModel{Name: "Pony", Table: p + "pony", Fields: m.Fields{
					id(),
					col("name", m.Field{Type: m.String, Size: 100, Unique: true}),
					col("weight", m.Field{Type: m.Int, Size: 32, DBDefault: m.DBValue(int64(5))}),
					col("label", m.Field{Type: m.String, Size: 30, Null: true, DBDefault: m.DBValue("x'y")}),
				}, Options: m.Options{
					Indexes:     []m.Index{{Name: "idx_" + p + "pony_weight", Fields: []m.IndexField{{Column: "weight"}}}},
					Constraints: []m.Constraint{&m.CheckConstraint{Name: "chk_" + p + "pony_weight", Check: q("weight") + " >= 0"}},
				}},
			}}
	},
	func(p string, q func(string) string) Case {
		return Case{Name: "delete_model", Setup: []m.Operation{pony(p)}, Ops: []m.Operation{&m.DeleteModel{Name: "Pony"}}}
	},
	func(p string, q func(string) string) Case {
		return Case{Name: "delete_model_with_incoming_fk",
			Skip:  skipNoForeignKeys,
			Setup: []m.Operation{pony(p), rider(p)},
			Ops:   []m.Operation{&m.RemoveField{ModelName: "rider", Name: "pony_id"}, &m.DeleteModel{Name: "Pony"}},
		}
	},
	func(p string, q func(string) string) Case {
		return Case{Name: "create_models_with_fk_and_self_fk",
			Skip: skipNoForeignKeys, Ops: []m.Operation{
				pony(p, col("parent_id", m.Field{Type: m.Int, Size: 64, Null: true, ForeignKey: fk("Pony", "fk_"+p+"pony_parent")})),
				rider(p),
			}}
	},
	func(p string, q func(string) string) Case {
		return Case{Name: "alter_model_table",
			Skip:  skipNoForeignKeys,
			Setup: []m.Operation{pony(p), rider(p)},
			Ops:   []m.Operation{&m.AlterModelTable{Name: "pony", Table: p + "pony2"}},
			Before: func(t testing.TB, db *gorm.DB) {
				exec(t, db, "INSERT INTO "+qt(db, p+"pony")+" ("+qt(db, "name")+") VALUES ('a')")
			},
			After: func(t testing.TB, db *gorm.DB) {
				if n := count(t, db, "SELECT count(*) FROM "+qt(db, p+"pony2")); n != 1 {
					t.Fatalf("rows after rename: %d", n)
				}
			},
		}
	},
	func(p string, q func(string) string) Case {
		return Case{Name: "rename_model",
			Skip:  skipNoForeignKeys,
			Setup: []m.Operation{pony(p), rider(p)},
			Ops:   []m.Operation{&m.RenameModel{OldName: "Pony", NewName: "Horse"}},
		}
	},
	func(p string, q func(string) string) Case {
		return Case{Name: "table_comment",
			Skip:  requires(func(f base.Features) bool { return f.SupportsComments }, "no comments"),
			Setup: []m.Operation{pony(p)},
			Ops: []m.Operation{
				&m.AlterModelTableComment{Name: "pony", TableComment: "ponies' table"},
			},
		}
	},
	func(p string, q func(string) string) Case {
		return Case{Name: "unique_together",
			Skip:  skipNoUnique,
			Setup: []m.Operation{pony(p, col("color", str(20)))},
			Ops:   []m.Operation{&m.AlterUniqueTogether{Name: "pony", UniqueTogether: [][]string{{"name", "color"}}}},
		}
	},
	func(p string, q func(string) string) Case {
		return Case{Name: "add_field_nullable", Setup: []m.Operation{pony(p)},
			Ops: []m.Operation{&m.AddField{ModelName: "pony", Name: "height", Field: intF(32)}}}
	},
	func(p string, q func(string) string) Case {
		return Case{Name: "add_field_not_null_one_off_default", Setup: []m.Operation{pony(p)},
			Ops: []m.Operation{&m.AddField{ModelName: "pony", Name: "height", Field: m.Field{Type: m.Int, Size: 32, Default: int64(7)}, PreserveDefault: m.Ptr(false)}},
			Before: func(t testing.TB, db *gorm.DB) {
				exec(t, db, "INSERT INTO "+qt(db, p+"pony")+" ("+qt(db, "name")+") VALUES ('a')")
			},
			After: func(t testing.TB, db *gorm.DB) {
				if n := count(t, db, "SELECT count(*) FROM "+qt(db, p+"pony")+" WHERE "+qt(db, "height")+" = 7"); n != 1 {
					t.Fatalf("existing row not populated with the one-off default")
				}
			},
		}
	},
	func(p string, q func(string) string) Case {
		return Case{Name: "add_field_db_default", Setup: []m.Operation{pony(p)},
			Ops: []m.Operation{&m.AddField{ModelName: "pony", Name: "kind", Field: m.Field{Type: m.String, Size: 10, DBDefault: m.DBValue("pony")}}},
			Before: func(t testing.TB, db *gorm.DB) {
				exec(t, db, "INSERT INTO "+qt(db, p+"pony")+" ("+qt(db, "name")+") VALUES ('a')")
			},
			After: func(t testing.TB, db *gorm.DB) {
				if n := count(t, db, "SELECT count(*) FROM "+qt(db, p+"pony")+" WHERE "+qt(db, "kind")+" = 'pony'"); n != 1 {
					t.Fatalf("db default not applied to existing row")
				}
			},
		}
	},
	func(p string, q func(string) string) Case {
		return Case{Name: "add_field_unique",
			Skip: skipNoUnique, Setup: []m.Operation{pony(p)},
			Ops: []m.Operation{&m.AddField{ModelName: "pony", Name: "code", Field: m.Field{Type: m.String, Size: 10, Null: true, Unique: true}}}}
	},
	func(p string, q func(string) string) Case {
		return Case{Name: "add_field_fk",
			Skip: skipNoForeignKeys, Setup: []m.Operation{pony(p), &m.CreateModel{Name: "Rider", Table: p + "rider", Fields: m.Fields{id()}}},
			Ops: []m.Operation{&m.AddField{ModelName: "rider", Name: "pony_id", Field: m.Field{Type: m.Int, Size: 64, Null: true, ForeignKey: &m.ForeignKey{To: "Pony", ToField: "id", Name: "fk_" + p + "rider_pony", OnDelete: m.Cascade}}}}}
	},
	func(p string, q func(string) string) Case {
		return Case{Name: "add_field_comment",
			Skip:  requires(func(f base.Features) bool { return f.SupportsComments }, "no comments"),
			Setup: []m.Operation{pony(p)},
			Ops:   []m.Operation{&m.AddField{ModelName: "pony", Name: "note", Field: m.Field{Type: m.String, Size: 50, Null: true, Comment: "a note"}}}}
	},
	func(p string, q func(string) string) Case {
		return Case{Name: "remove_field", Setup: []m.Operation{pony(p, col("height", intF(32)))},
			Ops: []m.Operation{&m.RemoveField{ModelName: "pony", Name: "height"}}}
	},
	func(p string, q func(string) string) Case {
		return Case{Name: "remove_field_fk",
			Skip: skipNoForeignKeys, Setup: []m.Operation{pony(p), rider(p)},
			Ops: []m.Operation{&m.RemoveField{ModelName: "rider", Name: "pony_id"}}}
	},
	func(p string, q func(string) string) Case {
		return Case{Name: "remove_field_unique",
			Skip: skipNoUnique, Setup: []m.Operation{pony(p, col("code", m.Field{Type: m.String, Size: 10, Null: true, Unique: true}))},
			Ops: []m.Operation{&m.RemoveField{ModelName: "pony", Name: "code"}}}
	},
	func(p string, q func(string) string) Case {
		return Case{Name: "rename_field", Setup: []m.Operation{pony(p)},
			Ops: []m.Operation{&m.RenameField{ModelName: "pony", OldName: "name", NewName: "title"}},
			Before: func(t testing.TB, db *gorm.DB) {
				exec(t, db, "INSERT INTO "+qt(db, p+"pony")+" ("+qt(db, "name")+") VALUES ('keep')")
			},
			After: func(t testing.TB, db *gorm.DB) {
				if n := count(t, db, "SELECT count(*) FROM "+qt(db, p+"pony")+" WHERE "+qt(db, "title")+" = 'keep'"); n != 1 {
					t.Fatalf("data lost by rename")
				}
			},
		}
	},
	func(p string, q func(string) string) Case {
		return Case{Name: "rename_field_fk_column",
			Skip: skipNoForeignKeys, Setup: []m.Operation{pony(p), rider(p)},
			Ops: []m.Operation{&m.RenameField{ModelName: "rider", OldName: "pony_id", NewName: "horse_id"}}}
	},
	func(p string, q func(string) string) Case {
		return Case{Name: "rename_field_indexed",
			Skip: skipNoPlainIndexes, Setup: []m.Operation{
				pony(p),
				&m.AddIndex{ModelName: "pony", Index: m.Index{Name: "idx_" + p + "pony_name", Fields: []m.IndexField{{Column: "name"}}}},
			},
			Ops: []m.Operation{&m.RenameField{ModelName: "pony", OldName: "name", NewName: "title"}}}
	},
	func(p string, q func(string) string) Case {
		return Case{Name: "rename_field_referenced_pk",
			Skip: skipNoForeignKeys, Setup: []m.Operation{pony(p), rider(p)},
			Ops: []m.Operation{&m.RenameField{ModelName: "pony", OldName: "id", NewName: "pony_key"}}}
	},
	func(p string, q func(string) string) Case {
		return Case{Name: "alter_field_null_to_not_null_with_default",
			Skip: forbids(func(f base.Features) bool { return f.NoColumnNullability }, "NULL / NOT NULL is not a column property, so there are no NULL rows to fill"), Setup: []m.Operation{pony(p)},
			Ops: []m.Operation{&m.AlterField{ModelName: "pony", Name: "name", Field: m.Field{Type: m.String, Size: 100, Default: "unnamed"}, PreserveDefault: m.Ptr(false)}},
			Before: func(t testing.TB, db *gorm.DB) {
				exec(t, db, "INSERT INTO "+qt(db, p+"pony")+" ("+qt(db, "name")+") VALUES (NULL)")
			},
			After: func(t testing.TB, db *gorm.DB) {
				if n := count(t, db, "SELECT count(*) FROM "+qt(db, p+"pony")+" WHERE "+qt(db, "name")+" = 'unnamed'"); n != 1 {
					t.Fatalf("NULL row not filled with the one-off default")
				}
			},
		}
	},
	func(p string, q func(string) string) Case {
		return Case{Name: "alter_field_not_null_to_null", Setup: []m.Operation{pony(p, col("code", strNN(10)))},
			Ops: []m.Operation{&m.AlterField{ModelName: "pony", Name: "code", Field: str(10)}}}
	},
	func(p string, q func(string) string) Case {
		return Case{Name: "alter_field_increase_length",
			Skip: forbids(func(f base.Features) bool { return f.NoCastsToSizedStrings }, "a column cannot be converted to a fixed-width string type"), Setup: []m.Operation{pony(p)},
			Ops: []m.Operation{&m.AlterField{ModelName: "pony", Name: "name", Field: str(200)}},
			Before: func(t testing.TB, db *gorm.DB) {
				exec(t, db, "INSERT INTO "+qt(db, p+"pony")+" ("+qt(db, "name")+") VALUES ('keep')")
			},
			After: func(t testing.TB, db *gorm.DB) {
				if n := count(t, db, "SELECT count(*) FROM "+qt(db, p+"pony")+" WHERE "+qt(db, "name")+" = 'keep'"); n != 1 {
					t.Fatalf("data lost")
				}
			},
		}
	},
	func(p string, q func(string) string) Case {
		return Case{Name: "alter_field_int_to_bigint", Setup: []m.Operation{pony(p, col("n", intF(32)))},
			Ops: []m.Operation{&m.AlterField{ModelName: "pony", Name: "n", Field: intF(64)}},
			Before: func(t testing.TB, db *gorm.DB) {
				exec(t, db, "INSERT INTO "+qt(db, p+"pony")+" ("+qt(db, "n")+") VALUES (42)")
			},
			After: func(t testing.TB, db *gorm.DB) {
				if n := count(t, db, "SELECT count(*) FROM "+qt(db, p+"pony")+" WHERE "+qt(db, "n")+" = 42"); n != 1 {
					t.Fatalf("data lost")
				}
			},
		}
	},
	func(p string, q func(string) string) Case {
		return Case{Name: "alter_field_varchar_to_text", Setup: []m.Operation{pony(p)},
			Ops: []m.Operation{&m.AlterField{ModelName: "pony", Name: "name", Field: m.Field{Type: m.String, Null: true}}}}
	},
	func(p string, q func(string) string) Case {
		return Case{Name: "alter_field_int_to_string",
			Skip: forbids(func(f base.Features) bool { return f.NoCastsToSizedStrings }, "a column of another type cannot be converted to a fixed-width string type"), Setup: []m.Operation{pony(p, col("n", intF(32)))},
			Ops: []m.Operation{&m.AlterField{ModelName: "pony", Name: "n", Field: str(20)}},
			Before: func(t testing.TB, db *gorm.DB) {
				exec(t, db, "INSERT INTO "+qt(db, p+"pony")+" ("+qt(db, "n")+") VALUES (42)")
			},
			After: func(t testing.TB, db *gorm.DB) {
				if n := count(t, db, "SELECT count(*) FROM "+qt(db, p+"pony")+" WHERE "+qt(db, "n")+" = '42'"); n != 1 {
					t.Fatalf("data not converted")
				}
			},
			AfterBackwards: func(t testing.TB, db *gorm.DB) {
				if n := count(t, db, "SELECT count(*) FROM "+qt(db, p+"pony")+" WHERE "+qt(db, "n")+" = 42"); n != 1 {
					t.Fatalf("data not converted back")
				}
			},
		}
	},
	func(p string, q func(string) string) Case {
		return Case{Name: "alter_field_add_unique",
			Skip: skipNoUnique, Setup: []m.Operation{pony(p)},
			Ops: []m.Operation{&m.AlterField{ModelName: "pony", Name: "name", Field: m.Field{Type: m.String, Size: 100, Null: true, Unique: true}}}}
	},
	func(p string, q func(string) string) Case {
		return Case{Name: "alter_field_remove_unique",
			Skip: skipNoUnique, Setup: []m.Operation{pony(p, col("code", m.Field{Type: m.String, Size: 10, Null: true, Unique: true}))},
			Ops: []m.Operation{&m.AlterField{ModelName: "pony", Name: "code", Field: str(10)}}}
	},
	func(p string, q func(string) string) Case {
		return Case{Name: "alter_field_db_default_add_change_drop", Setup: []m.Operation{pony(p, col("n", intF(32)))},
			Ops: []m.Operation{
				&m.AlterField{ModelName: "pony", Name: "n", Field: m.Field{Type: m.Int, Size: 32, Null: true, DBDefault: m.DBValue(int64(1))}},
				&m.AlterField{ModelName: "pony", Name: "name", Field: m.Field{Type: m.String, Size: 100, Null: true, DBDefault: m.DBValue("x")}},
			}}
	},
	func(p string, q func(string) string) Case {
		return Case{Name: "alter_field_drop_db_default", Setup: []m.Operation{pony(p, col("n", m.Field{Type: m.Int, Size: 32, Null: true, DBDefault: m.DBValue(int64(3))}))},
			Ops: []m.Operation{&m.AlterField{ModelName: "pony", Name: "n", Field: intF(32)}}}
	},
	func(p string, q func(string) string) Case {
		return Case{Name: "alter_field_comment",
			Skip:  requires(func(f base.Features) bool { return f.SupportsComments }, "no comments"),
			Setup: []m.Operation{pony(p)},
			Ops:   []m.Operation{&m.AlterField{ModelName: "pony", Name: "name", Field: m.Field{Type: m.String, Size: 100, Null: true, Comment: "the name"}}}}
	},
	func(p string, q func(string) string) Case {
		return Case{Name: "alter_field_pk_type_with_incoming_fk",
			Skip: skipAny(skipNoForeignKeys, forbids(func(f base.Features) bool { return f.CannotAlterIntegerPrimaryKeyType }, "the type of an integer primary key column can't be changed")),
			Setup: []m.Operation{
				&m.CreateModel{Name: "Pony", Table: p + "pony", Fields: m.Fields{col("id", m.Field{Type: m.Int, Size: 32, PrimaryKey: true})}},
				&m.CreateModel{Name: "Rider", Table: p + "rider", Fields: m.Fields{id(), col("pony_id", m.Field{Type: m.Int, Size: 32, Null: true, ForeignKey: fk("Pony", "fk_"+p+"rider_pony")})}},
			},
			Ops: []m.Operation{
				&m.AlterField{ModelName: "pony", Name: "id", Field: m.Field{Type: m.Int, Size: 64, PrimaryKey: true}},
				&m.AlterField{ModelName: "rider", Name: "pony_id", Field: m.Field{Type: m.Int, Size: 64, Null: true, ForeignKey: fk("Pony", "fk_"+p+"rider_pony")}},
			}}
	},
	func(p string, q func(string) string) Case {
		return Case{Name: "alter_field_fk_on_delete",
			Skip: skipNoForeignKeys, Setup: []m.Operation{pony(p), rider(p)},
			Ops: []m.Operation{&m.AlterField{ModelName: "rider", Name: "pony_id", Field: m.Field{Type: m.Int, Size: 64, Null: true, ForeignKey: &m.ForeignKey{To: "Pony", ToField: "id", Name: "fk_" + p + "rider_pony", OnDelete: m.SetNull}}}}}
	},
	func(p string, q func(string) string) Case {
		return Case{Name: "alter_field_fk_retarget",
			Skip: skipNoForeignKeys, Setup: []m.Operation{
				pony(p), rider(p), &m.CreateModel{Name: "Horse", Table: p + "horse", Fields: m.Fields{id()}},
			},
			Ops: []m.Operation{&m.AlterField{ModelName: "rider", Name: "pony_id", Field: m.Field{Type: m.Int, Size: 64, Null: true, ForeignKey: &m.ForeignKey{To: "Horse", ToField: "id", Name: "fk_" + p + "rider_horse"}}}}}
	},
	func(p string, q func(string) string) Case {
		return Case{Name: "alter_field_fk_to_plain",
			Skip: skipNoForeignKeys, Setup: []m.Operation{pony(p), rider(p)},
			Ops: []m.Operation{&m.AlterField{ModelName: "rider", Name: "pony_id", Field: intF(64)}}}
	},
	func(p string, q func(string) string) Case {
		return Case{Name: "alter_field_plain_to_fk",
			Skip: skipNoForeignKeys, Setup: []m.Operation{pony(p), &m.CreateModel{Name: "Rider", Table: p + "rider", Fields: m.Fields{id(), col("pony_id", intF(64))}}},
			Ops: []m.Operation{&m.AlterField{ModelName: "rider", Name: "pony_id", Field: m.Field{Type: m.Int, Size: 64, Null: true, ForeignKey: fk("Pony", "fk_"+p+"rider_pony")}}}}
	},
	func(p string, q func(string) string) Case {
		return Case{Name: "alter_field_drop_autoincrement",
			Skip:  skipAutoIncrementAlter,
			Setup: []m.Operation{pony(p)},
			Ops:   []m.Operation{&m.AlterField{ModelName: "pony", Name: "id", Field: m.Field{Type: m.Int, Size: 64, PrimaryKey: true}}}}
	},
	func(p string, q func(string) string) Case {
		return Case{Name: "alter_field_add_autoincrement",
			Skip: skipAutoIncrementAlter,
			Setup: []m.Operation{
				&m.CreateModel{Name: "Pony", Table: p + "pony", Fields: m.Fields{col("id", m.Field{Type: m.Int, Size: 64, PrimaryKey: true}), col("name", str(10))}},
			},
			Ops: []m.Operation{&m.AlterField{ModelName: "pony", Name: "id", Field: m.Field{Type: m.Int, Size: 64, PrimaryKey: true, AutoIncrement: true}}}}
	},
	func(p string, q func(string) string) Case {
		return Case{Name: "add_index_simple_multi_desc_unique",
			Skip: skipNoPlainIndexes, Setup: []m.Operation{pony(p, col("weight", intF(32)))},
			Ops: []m.Operation{
				&m.AddIndex{ModelName: "pony", Index: m.Index{Name: "idx_" + p + "a", Fields: []m.IndexField{{Column: "name"}}}},
				&m.AddIndex{ModelName: "pony", Index: m.Index{Name: "idx_" + p + "b", Fields: []m.IndexField{{Column: "name"}, {Column: "weight", Sort: "DESC"}}}},
				&m.AddIndex{ModelName: "pony", Index: m.Index{Name: "idx_" + p + "c", Unique: true, Fields: []m.IndexField{{Column: "weight"}}}},
			}}
	},
	func(p string, q func(string) string) Case {
		return Case{Name: "add_index_partial",
			Skip:  requires(func(f base.Features) bool { return f.SupportsPartialIndexes }, "no partial indexes"),
			Setup: []m.Operation{pony(p, col("weight", intF(32)))},
			Ops: []m.Operation{
				&m.AddIndex{ModelName: "pony", Index: m.Index{Name: "idx_" + p + "p", Fields: []m.IndexField{{Column: "name"}}, Where: q("weight") + " > 10"}},
			}}
	},
	func(p string, q func(string) string) Case {
		return Case{Name: "add_index_expression",
			Skip:  skipAny(requires(func(f base.Features) bool { return f.SupportsExpressionIndexes }, "no expression indexes"), skipNoPlainIndexes),
			Setup: []m.Operation{pony(p)},
			Ops: []m.Operation{
				&m.AddIndex{ModelName: "pony", Index: m.Index{Name: "idx_" + p + "e", Fields: []m.IndexField{{Expression: "(lower(" + q("name") + "))"}}}},
			}}
	},
	func(p string, q func(string) string) Case {
		return Case{Name: "remove_index",
			Skip: skipNoPlainIndexes, Setup: []m.Operation{
				pony(p), &m.AddIndex{ModelName: "pony", Index: m.Index{Name: "idx_" + p + "a", Fields: []m.IndexField{{Column: "name"}}}},
			},
			Ops: []m.Operation{&m.RemoveIndex{ModelName: "pony", Name: "idx_" + p + "a"}}}
	},
	func(p string, q func(string) string) Case {
		return Case{Name: "rename_index",
			Skip: skipNoPlainIndexes, Setup: []m.Operation{
				pony(p), &m.AddIndex{ModelName: "pony", Index: m.Index{Name: "idx_" + p + "a", Fields: []m.IndexField{{Column: "name"}}}},
			},
			Ops: []m.Operation{&m.RenameIndex{ModelName: "pony", OldName: "idx_" + p + "a", NewName: "idx_" + p + "b"}}}
	},
	func(p string, q func(string) string) Case {
		return Case{Name: "add_remove_check_constraint", Setup: []m.Operation{pony(p, col("weight", intF(32)))},
			Ops: []m.Operation{
				&m.AddConstraint{ModelName: "pony", Constraint: &m.CheckConstraint{Name: "chk_" + p + "w", Check: q("weight") + " > 0"}},
				&m.AddConstraint{ModelName: "pony", Constraint: &m.CheckConstraint{Name: "chk_" + p + "x", Check: q("weight") + " < 1000"}},
				&m.RemoveConstraint{ModelName: "pony", Name: "chk_" + p + "x"},
			}}
	},
	func(p string, q func(string) string) Case {
		return Case{Name: "add_unique_constraint",
			Skip: skipNoUnique, Setup: []m.Operation{pony(p, col("color", str(20)))},
			Ops: []m.Operation{&m.AddConstraint{ModelName: "pony", Constraint: &m.UniqueConstraint{Name: "uq_" + p + "nc", Fields: []string{"name", "color"}}}}}
	},
	func(p string, q func(string) string) Case {
		return Case{Name: "add_unique_constraint_partial",
			Skip:  skipAny(skipNoUnique, requires(func(f base.Features) bool { return f.SupportsPartialIndexes }, "no partial indexes")),
			Setup: []m.Operation{pony(p, col("color", str(20)))},
			Ops:   []m.Operation{&m.AddConstraint{ModelName: "pony", Constraint: &m.UniqueConstraint{Name: "uq_" + p + "nc", Fields: []string{"name"}, Condition: q("color") + " IS NOT NULL"}}}}
	},
	func(p string, q func(string) string) Case {
		return Case{Name: "remove_unique_constraint",
			Skip: skipNoUnique, Setup: []m.Operation{
				pony(p, col("color", str(20))),
				&m.AddConstraint{ModelName: "pony", Constraint: &m.UniqueConstraint{Name: "uq_" + p + "nc", Fields: []string{"name", "color"}}},
			},
			Ops: []m.Operation{&m.RemoveConstraint{ModelName: "pony", Name: "uq_" + p + "nc"}}}
	},
	func(p string, q func(string) string) Case {
		return Case{Name: "add_composite_foreign_key",
			Skip: skipNoForeignKeys, Setup: compositeFKSetup(p),
			Ops: []m.Operation{&m.AddConstraint{ModelName: "stall", Constraint: compositeFK(p)}}}
	},
	func(p string, q func(string) string) Case {
		return Case{Name: "remove_composite_foreign_key",
			Skip: skipNoForeignKeys,
			Setup: append(compositeFKSetup(p),
				&m.AddConstraint{ModelName: "stall", Constraint: compositeFK(p)}),
			Ops: []m.Operation{&m.RemoveConstraint{ModelName: "stall", Name: "fk_" + p + "stall_barn"}}}
	},
	func(p string, q func(string) string) Case {
		// Altering a column a composite foreign key covers: the key has to
		// be dropped and put back around the alteration, because a search
		// for the constraints of one column never finds a key over two.
		// The column keeps its type, since a foreign key column and the
		// column it references must stay type-compatible (SQL Server
		// requires them to be identical).
		return Case{Name: "alter_field_under_composite_foreign_key",
			Skip: skipNoForeignKeys,
			Setup: append(compositeFKSetup(p),
				&m.AddConstraint{ModelName: "stall", Constraint: compositeFK(p)}),
			Ops: []m.Operation{&m.AlterField{ModelName: "stall", Name: "barn_code",
				Field: m.Field{Type: m.String, Size: 20}}}}
	},
	func(p string, q func(string) string) Case {
		return Case{Name: "alter_constraint_state_only", Setup: []m.Operation{
			pony(p, col("weight", intF(32))),
			&m.AddConstraint{ModelName: "pony", Constraint: &m.CheckConstraint{Name: "chk_" + p + "w", Check: q("weight") + " > 0"}},
		},
			Ops: []m.Operation{&m.AlterConstraint{ModelName: "pony", Name: "chk_" + p + "w", Constraint: &m.CheckConstraint{Name: "chk_" + p + "w", Check: q("weight") + " > 0", ViolationErrorMessage: "too light"}}}}
	},
	func(p string, q func(string) string) Case {
		return Case{Name: "run_sql", Setup: []m.Operation{pony(p)},
			Ops: []m.Operation{&m.RunSQL{
				SQL:        m.Statements{"INSERT INTO " + q(p+"pony") + " (" + q("name") + ") VALUES ('sql')"},
				ReverseSQL: m.Statements{"DELETE FROM " + q(p+"pony") + " WHERE " + q("name") + " = 'sql'"},
			}},
			After: func(t testing.TB, db *gorm.DB) {
				if n := count(t, db, "SELECT count(*) FROM "+qt(db, p+"pony")+" WHERE "+qt(db, "name")+" = 'sql'"); n != 1 {
					t.Fatalf("RunSQL forwards not applied")
				}
			},
			AfterBackwards: func(t testing.TB, db *gorm.DB) {
				if n := count(t, db, "SELECT count(*) FROM "+qt(db, p+"pony")); n != 0 {
					t.Fatalf("RunSQL backwards not applied")
				}
			},
		}
	},
	func(p string, q func(string) string) Case {
		return Case{Name: "run_go_historical_model", Setup: []m.Operation{pony(p)}, NotSQL: true,
			Ops: []m.Operation{
				&m.AddField{ModelName: "pony", Name: "height", Field: intF(32)},
				&m.RunGo{Code: func(apps *m.Apps, ed m.SchemaEditor) error {
					model, err := apps.GetModel(p[:len(p)-1], "Pony")
					if err != nil {
						return err
					}
					h := model.Objects(ed)
					if err := h.Create(map[string]any{"name": "go", "height": 3}); err != nil {
						return err
					}
					_, err = h.Update(map[string]any{"name": "go"}, map[string]any{"height": 4})
					return err
				}, ReverseCode: func(apps *m.Apps, ed m.SchemaEditor) error {
					model, err := apps.GetModel(p[:len(p)-1], "Pony")
					if err != nil {
						return err
					}
					_, err = model.Objects(ed).Delete(map[string]any{"name": "go"})
					return err
				}},
			},
			After: func(t testing.TB, db *gorm.DB) {
				if n := count(t, db, "SELECT count(*) FROM "+qt(db, p+"pony")+" WHERE "+qt(db, "name")+" = 'go' AND "+qt(db, "height")+" = 4"); n != 1 {
					t.Fatalf("RunGo forwards not applied")
				}
			},
		}
	},
	func(p string, q func(string) string) Case {
		return Case{Name: "separate_database_and_state", Setup: []m.Operation{pony(p)},
			Ops: []m.Operation{&m.SeparateDatabaseAndState{
				DatabaseOperations: []m.Operation{&m.AddField{ModelName: "pony", Name: "extra", Field: intF(32)}},
				StateOperations:    []m.Operation{&m.AddField{ModelName: "pony", Name: "extra", Field: intF(32)}},
			}}}
	},
	func(p string, q func(string) string) Case {
		return Case{Name: "atomic_failure_rolls_back",
			Skip:        requires(func(f base.Features) bool { return f.CanRollbackDDL }, "DDL can't be rolled back"),
			Setup:       []m.Operation{pony(p)},
			ExpectError: p + "does_not_exist",
			Ops: []m.Operation{
				&m.AddField{ModelName: "pony", Name: "height", Field: intF(32)},
				&m.RunSQL{SQL: m.Statements{"SELECT * FROM " + q(p+"does_not_exist")}, ReverseSQL: m.NoSQL},
			},
		}
	},
}

// qt quotes a table name for raw test SQL.
func qt(db *gorm.DB, name string) string {
	return db.Statement.Quote(name)
}
