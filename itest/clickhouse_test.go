//go:build integration

package itest

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"gorm.io/gorm"

	"github.com/ctolon/gormgate/itest/internal/conformance"
	"github.com/ctolon/gormgate/itest/internal/dbtest"
	"github.com/ctolon/gormgate/itest/internal/harness"
	m "github.com/ctolon/gormgate/migrations"
)

// TestClickHouse covers the ClickHouse backend end to end: the subset of
// the operations it supports (forwards and backwards, with data), the
// migrations recorder round trip, and every operation it refuses, with the
// exact message of the migrations.NotSupportedError it raises.
func TestClickHouse(t *testing.T) {
	skipUnlessVendor(t, "clickhouse")
	t.Run("subset", testClickHouseSubset)
	t.Run("recorder", testClickHouseRecorder)
	t.Run("historical_models", testClickHouseHistoricalModels)
	t.Run("not_supported", testClickHouseNotSupported)
}

func chOpen(t *testing.T) (*gorm.DB, dbtest.Info) {
	t.Helper()
	db, info := dbtest.Open(t, "clickhouse")
	if info.Dialect != "clickhouse" {
		t.Fatalf("dialect %q", info.Dialect)
	}
	return db, info
}

func chExec(t testing.TB, db *gorm.DB, sql string, args ...any) {
	t.Helper()
	if err := db.Exec(sql, args...).Error; err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func chCount(t testing.TB, db *gorm.DB, sql string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := db.Raw(sql, args...).Row().Scan(&n); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return n
}

func chID() m.NamedField {
	return m.NamedField{Name: "id", Field: m.Field{Type: m.Int, Size: 64, PrimaryKey: true, AutoIncrement: true}}
}

func chField(name string, f m.Field) m.NamedField { return m.NamedField{Name: name, Field: f} }

// chPony is the model the subset test starts from: a MergeTree table sorted
// by its primary key, with a CHECK constraint and a data-skipping index.
func chPony() *m.CreateModel {
	return &m.CreateModel{Name: "Pony", Table: "ch_pony", Fields: m.Fields{
		chID(),
		chField("name", m.Field{Type: m.String}),
		chField("weight", m.Field{Type: m.Int, Size: 32}),
	}, Options: m.Options{
		ClickHouse: &m.ClickHouseTable{Engine: "MergeTree()", Settings: "index_granularity = 8192"},
		Indexes: []m.Index{{
			Name: "idx_ch_pony_name", Type: "minmax", Fields: []m.IndexField{{Column: "name"}},
		}},
		Constraints: []m.Constraint{&m.CheckConstraint{Name: "chk_ch_pony_weight", Check: "`weight` >= 0"}},
	}}
}

// testClickHouseSubset applies every supported operation, checks the
// resulting schema and data, and unapplies them again.
func testClickHouseSubset(t *testing.T) {
	db, _ := chOpen(t)
	p := harness.New(t, db, "ch")
	first := &m.Migration{App: "ch", Name: "0001_initial", Operations: []m.Operation{chPony()}}
	second := &m.Migration{App: "ch", Name: "0002_change",
		Dependencies: []m.Key{{App: "ch", Name: "0001_initial"}},
		Operations: []m.Operation{
			// A one-off default: the existing rows must keep the value
			// after the default is dropped again.
			&m.AddField{ModelName: "pony", Name: "height",
				Field: m.Field{Type: m.Int, Size: 32, Default: int64(7)}, PreserveDefault: m.Ptr(false)},
			// A database default and an inline comment.
			&m.AddField{ModelName: "pony", Name: "kind",
				Field: m.Field{Type: m.String, DBDefault: m.DBValue("pony"), Comment: "the kind"}},
			// Type change of a plain column, and a comment change.
			&m.AlterField{ModelName: "pony", Name: "weight",
				Field: m.Field{Type: m.Int, Size: 64, Comment: "in kilos"}},
			&m.RenameField{ModelName: "pony", OldName: "name", NewName: "title"},
			&m.AddIndex{ModelName: "pony", Index: m.Index{
				Name: "idx_ch_pony_weight", Type: "minmax", Option: "GRANULARITY 4",
				Fields: []m.IndexField{{Column: "weight"}}}},
			&m.RemoveIndex{ModelName: "pony", Name: "idx_ch_pony_name"},
			&m.AddConstraint{ModelName: "pony", Constraint: &m.CheckConstraint{
				Name: "chk_ch_pony_height", Check: "`height` >= 0"}},
			&m.AlterModelTableComment{Name: "pony", TableComment: "ponies' table"},
			&m.RunSQL{
				SQL:        m.Statements{"INSERT INTO `ch_pony` (`id`, `title`, `weight`, `height`) VALUES (2, 'sql', 3, 9)"},
				ReverseSQL: m.Statements{"DELETE FROM `ch_pony` WHERE `id` = 2"},
			},
			&m.AlterModelTable{Name: "pony", Table: "ch_pony2"},
		}}
	p.Register(first, second)

	p.MustMigrate(first.Key())
	chExec(t, db, "INSERT INTO `ch_pony` (`id`, `name`, `weight`) VALUES (1, 'a', 5)")
	tables := []string{"ch_pony", "ch_pony2"}
	afterFirst := chSnap(t, p, tables)

	p.MustMigrate(second.Key())
	afterSecond := chSnap(t, p, tables)

	// Schema.
	pony2, ok := afterSecond["ch_pony2"]
	if !ok {
		t.Fatalf("table ch_pony2 missing after the rename: %+v", afterSecond)
	}
	if _, ok := afterSecond["ch_pony"]; ok {
		t.Fatal("the old table ch_pony still exists after the rename")
	}
	if pony2.Comment != "ponies' table" {
		t.Errorf("table comment %q", pony2.Comment)
	}
	want := map[string]conformance.Column{
		"id":     {Type: "Int64"},
		"title":  {Type: "String"},
		"weight": {Type: "Int64", Comment: "in kilos"},
		"height": {Type: "Int32"},
		"kind":   {Type: "String", Default: "'pony'", Comment: "the kind"},
	}
	for name, w := range want {
		if got := pony2.Columns[name]; got != w {
			t.Errorf("column %s: got %+v, want %+v", name, got, w)
		}
	}
	if len(pony2.Columns) != len(want) {
		t.Errorf("columns: %+v", pony2.Columns)
	}
	for _, name := range []string{"PRIMARY", "idx_ch_pony_weight", "chk_ch_pony_weight", "chk_ch_pony_height"} {
		if _, ok := pony2.Constraints[name]; !ok {
			t.Errorf("constraint %s missing: %+v", name, pony2.Constraints)
		}
	}
	if _, ok := pony2.Constraints["idx_ch_pony_name"]; ok {
		t.Error("the removed index idx_ch_pony_name is still there")
	}
	if c := pony2.Constraints["PRIMARY"]; c.Columns != "id" || !c.PrimaryKey {
		t.Errorf("primary key: %+v", c)
	}
	if c := pony2.Constraints["chk_ch_pony_height"]; !c.Check {
		t.Errorf("check constraint: %+v", c)
	}

	// Data: the row inserted before the migration kept its values, was
	// given the one-off default and the database default, and the row
	// inserted by RunSQL is there.
	if n := chCount(t, db, "SELECT count() FROM `ch_pony2` WHERE `id` = 1 AND `title` = 'a' AND `weight` = 5 AND `height` = 7 AND `kind` = 'pony'"); n != 1 {
		t.Error("the existing row did not survive the migration unchanged")
	}
	if n := chCount(t, db, "SELECT count() FROM `ch_pony2` WHERE `id` = 2 AND `title` = 'sql'"); n != 1 {
		t.Error("RunSQL did not insert its row")
	}
	// The one-off default was dropped again.
	if def := pony2.Columns["height"].Default; def != "" {
		t.Errorf("the one-off default was kept: %q", def)
	}
	// The CHECK constraint is enforced.
	err := db.Exec("INSERT INTO `ch_pony2` (`id`, `title`, `weight`, `height`) VALUES (3, 'bad', 1, -1)").Error
	if err == nil {
		t.Error("the check constraint chk_ch_pony_height is not enforced")
	} else if !strings.Contains(err.Error(), "chk_ch_pony_height") {
		t.Errorf("check violation: %v", err)
	}

	// Backwards.
	p.MustMigrate(first.Key())
	if d := conformance.Diff(afterFirst, chSnap(t, p, tables)); d != "" {
		t.Errorf("schema after unapplying:\n%s", d)
	}
	if n := chCount(t, db, "SELECT count() FROM `ch_pony` WHERE `id` = 1 AND `name` = 'a' AND `weight` = 5"); n != 1 {
		t.Error("the row did not survive the backwards migration")
	}
	if n := chCount(t, db, "SELECT count() FROM `ch_pony`"); n != 1 {
		t.Error("RunSQL was not reversed")
	}

	// Forwards again, then down to nothing.
	p.MustMigrate(second.Key())
	if d := conformance.Diff(afterSecond, chSnap(t, p, tables)); d != "" {
		t.Errorf("schema after re-applying:\n%s", d)
	}
	p.MustMigrate(m.Key{App: "ch", Name: ""})
	if left := chSnap(t, p, tables); len(left) != 0 {
		t.Errorf("tables left after migrating to zero: %+v", left)
	}
}

func chSnap(t *testing.T, p *harness.Project, tables []string) conformance.Snapshot {
	t.Helper()
	s, err := conformance.Take(p.Conn.Introspection(), tables, nil)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	return s
}

// testClickHouseRecorder checks the migrations table: it is a MergeTree
// sorted by (app, name), its id is filled without a sequence, and applying
// and unapplying a migration adds and removes its row.
func testClickHouseRecorder(t *testing.T) {
	db, _ := chOpen(t)
	p := harness.New(t, db, "ch")
	first := &m.Migration{App: "ch", Name: "0001_initial", Operations: []m.Operation{chPony()}}
	second := &m.Migration{App: "ch", Name: "0002_more",
		Dependencies: []m.Key{{App: "ch", Name: "0001_initial"}},
		Operations:   []m.Operation{&m.AddField{ModelName: "pony", Name: "height", Field: m.Field{Type: m.Int, Size: 32}}}}
	p.Register(first, second)

	rec := p.Executor().Recorder
	if applied, err := rec.AppliedMigrations(); err != nil || len(applied) != 0 {
		t.Fatalf("applied before migrating: %v %v", applied, err)
	}
	p.MustMigrate(second.Key())

	// The recorder table itself.
	var engine, sortingKey string
	if err := db.Raw("SELECT engine, sorting_key FROM system.tables WHERE database = currentDatabase() AND name = 'gormgate_migrations'").Row().Scan(&engine, &sortingKey); err != nil {
		t.Fatalf("recorder table: %v", err)
	}
	if engine != "MergeTree" {
		t.Errorf("recorder engine %q, want MergeTree", engine)
	}
	if sortingKey != "app, name" {
		t.Errorf("recorder sorting key %q, want \"app, name\"", sortingKey)
	}
	types := map[string]string{}
	rows, err := db.Raw("SELECT name, type FROM system.columns WHERE database = currentDatabase() AND table = 'gormgate_migrations'").Rows()
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var n, ty string
		if err := rows.Scan(&n, &ty); err != nil {
			t.Fatal(err)
		}
		types[n] = ty
	}
	rows.Close()
	wantTypes := map[string]string{"id": "Int64", "app": "String", "name": "String", "applied": "DateTime64(3)"}
	for n, w := range wantTypes {
		if types[n] != w {
			t.Errorf("recorder column %s: %q, want %q", n, types[n], w)
		}
	}

	records, err := rec.Records()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 {
		t.Fatalf("records: %+v", records)
	}
	for i, r := range records {
		if r.ID != int64(i+1) {
			t.Errorf("record %d has id %d, want %d", i, r.ID, i+1)
		}
		if r.App != "ch" {
			t.Errorf("record %d app %q", i, r.App)
		}
		if r.Applied.IsZero() {
			t.Errorf("record %d has no applied time", i)
		}
	}
	if records[0].Name != "0001_initial" || records[1].Name != "0002_more" {
		t.Errorf("recorded names: %+v", records)
	}

	// Unapplying removes the row again.
	p.MustMigrate(first.Key())
	applied, err := p.Executor().Recorder.AppliedMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != 1 || applied[0] != (m.Key{App: "ch", Name: "0001_initial"}) {
		t.Fatalf("applied after unapplying: %+v", applied)
	}
	p.MustMigrate(m.Key{App: "ch", Name: ""})
	if applied, err := p.Executor().Recorder.AppliedMigrations(); err != nil || len(applied) != 0 {
		t.Fatalf("applied after migrating to zero: %v %v", applied, err)
	}
	// A migration applied after an unapply gets the next id.
	p.MustMigrate(first.Key())
	records, err = p.Executor().Recorder.Records()
	if err != nil || len(records) != 1 || records[0].ID != 1 {
		t.Fatalf("records after reapplying: %+v %v", records, err)
	}
}

// testClickHouseHistoricalModels drives the RunGo helpers of
// migrations/historical.go against ClickHouse.
func testClickHouseHistoricalModels(t *testing.T) {
	db, _ := chOpen(t)
	p := harness.New(t, db, "ch")
	first := &m.Migration{App: "ch", Name: "0001_initial", Operations: []m.Operation{chPony()}}
	var found []map[string]any
	var counted int64
	second := &m.Migration{App: "ch", Name: "0002_data",
		Dependencies: []m.Key{{App: "ch", Name: "0001_initial"}},
		Operations: []m.Operation{
			&m.RunGo{Code: func(apps *m.Apps, ed m.SchemaEditor) error {
				model, err := apps.GetModel("ch", "Pony")
				if err != nil {
					return err
				}
				h := model.Objects(ed)
				if err := h.Create(
					map[string]any{"id": 1, "name": "go", "weight": 1},
					map[string]any{"id": 2, "name": "other", "weight": 2},
				); err != nil {
					return err
				}
				if _, err := h.Update(map[string]any{"name": "go"}, map[string]any{"weight": 4}); err != nil {
					return err
				}
				if _, err := h.Delete(map[string]any{"name": "other"}); err != nil {
					return err
				}
				if found, err = h.Find(nil); err != nil {
					return err
				}
				counted, err = h.Count(nil)
				return err
			}, ReverseCode: func(apps *m.Apps, ed m.SchemaEditor) error {
				model, err := apps.GetModel("ch", "Pony")
				if err != nil {
					return err
				}
				_, err = model.Objects(ed).DeleteAll()
				return err
			}},
		}}
	p.Register(first, second)
	p.MustMigrate(second.Key())

	if len(found) != 1 || fmt.Sprint(found[0]["name"]) != "go" || fmt.Sprint(found[0]["weight"]) != "4" {
		t.Errorf("Find inside RunGo returned %+v", found)
	}
	if counted != 1 {
		t.Errorf("Count inside RunGo returned %d", counted)
	}
	if n := chCount(t, db, "SELECT count() FROM `ch_pony` WHERE `name` = 'go' AND `weight` = 4"); n != 1 {
		t.Error("the row created and updated by RunGo is not in the table")
	}
	if n := chCount(t, db, "SELECT count() FROM `ch_pony`"); n != 1 {
		t.Error("the row deleted by RunGo is still in the table")
	}

	p.MustMigrate(first.Key())
	if n := chCount(t, db, "SELECT count() FROM `ch_pony`"); n != 0 {
		t.Error("the reverse RunGo code did not delete the rows")
	}
	p.MustMigrate(m.Key{App: "ch", Name: ""})
}

// chNotSupported is one operation ClickHouse refuses.
type chNotSupported struct {
	// app is the migration app label; the tables of the case are named
	// after it, and so is the model in the expected message.
	app   string
	name  string
	setup []m.Operation
	ops   []m.Operation
	want  string
}

// testClickHouseNotSupported checks that every unsupported operation fails
// with a NotSupportedError carrying the exact message below, and that it is
// never silently ignored.
func testClickHouseNotSupported(t *testing.T) {
	pony := func(app string, extra ...m.NamedField) *m.CreateModel {
		fs := m.Fields{chID(), chField("name", m.Field{Type: m.String})}
		return &m.CreateModel{Name: "Pony", Table: app + "_pony", Fields: append(fs, extra...)}
	}
	riderFK := func(app string) *m.CreateModel {
		return &m.CreateModel{Name: "Rider", Table: app + "_rider", Fields: m.Fields{
			chID(),
			chField("pony_id", m.Field{Type: m.Int, Size: 64, ForeignKey: &m.ForeignKey{To: "Pony", ToField: "id", Name: "fk_rider_pony"}}),
		}}
	}
	cases := []chNotSupported{
		{
			name: "create_model_with_foreign_key",
			app:  "n0",
			ops:  []m.Operation{pony("n0"), riderFK("n0")},
			want: `clickhouse: CreateModel n0.Rider.pony_id: ClickHouse has no foreign keys (the field references n0.Pony)`,
		},
		{
			name: "create_model_with_unique_field",
			app:  "n1",
			ops:  []m.Operation{pony("n1", chField("code", m.Field{Type: m.String, Unique: true}))},
			want: `clickhouse: CreateModel n1.Pony.code: ClickHouse has no unique constraints or unique indexes (the field is unique)`,
		},
		{
			name: "create_model_with_unique_together",
			app:  "n2",
			ops: []m.Operation{&m.CreateModel{Name: "Pony", Table: "n2_pony", Fields: m.Fields{
				chID(), chField("name", m.Field{Type: m.String}), chField("color", m.Field{Type: m.String}),
			}, Options: m.Options{UniqueTogether: [][]string{{"name", "color"}}}}},
			want: `clickhouse: CreateModel n2.Pony: ClickHouse has no unique constraints or unique indexes (unique_together (name, color))`,
		},
		{
			name: "create_model_with_unique_constraint",
			app:  "n3",
			ops: []m.Operation{&m.CreateModel{Name: "Pony", Table: "n3_pony", Fields: m.Fields{
				chID(), chField("name", m.Field{Type: m.String}),
			}, Options: m.Options{Constraints: []m.Constraint{&m.UniqueConstraint{Name: "uq_n3_pony_name", Fields: []string{"name"}}}}}},
			want: `clickhouse: CreateModel n3.Pony: ClickHouse has no unique constraints or unique indexes (constraint "uq_n3_pony_name")`,
		},
		{
			name:  "add_foreign_key_field",
			app:   "n4",
			setup: []m.Operation{pony("n4"), &m.CreateModel{Name: "Rider", Table: "n4_rider", Fields: m.Fields{chID()}}},
			ops: []m.Operation{&m.AddField{ModelName: "rider", Name: "pony_id",
				Field: m.Field{Type: m.Int, Size: 64, ForeignKey: &m.ForeignKey{To: "Pony", ToField: "id", Name: "fk_rider_pony"}}}},
			want: `clickhouse: AddField n4.Rider.pony_id: ClickHouse has no foreign keys (the field references n4.Pony)`,
		},
		{
			name:  "add_unique_field",
			app:   "n5",
			setup: []m.Operation{pony("n5")},
			ops:   []m.Operation{&m.AddField{ModelName: "pony", Name: "code", Field: m.Field{Type: m.String, Unique: true}}},
			want:  `clickhouse: AddField n5.Pony.code: ClickHouse has no unique constraints or unique indexes (the field is unique)`,
		},
		{
			name:  "alter_field_to_unique",
			app:   "n6",
			setup: []m.Operation{pony("n6")},
			ops:   []m.Operation{&m.AlterField{ModelName: "pony", Name: "name", Field: m.Field{Type: m.String, Unique: true}}},
			want:  `clickhouse: AlterField n6.Pony.name: ClickHouse has no unique constraints or unique indexes (the field is unique)`,
		},
		{
			name:  "alter_key_column_type",
			app:   "n7",
			setup: []m.Operation{pony("n7")},
			ops:   []m.Operation{&m.AlterField{ModelName: "pony", Name: "id", Field: m.Field{Type: m.Int, Size: 32, PrimaryKey: true}}},
			want:  `clickhouse: AlterField n7.Pony.id: the column is part of the ORDER BY / PRIMARY KEY / PARTITION BY key of the table, which ClickHouse cannot alter`,
		},
		{
			name:  "rename_key_column",
			app:   "n8",
			setup: []m.Operation{pony("n8")},
			ops:   []m.Operation{&m.RenameField{ModelName: "pony", OldName: "id", NewName: "pony_id"}},
			want:  `clickhouse: AlterField n8.Pony.pony_id: the column is part of the ORDER BY / PRIMARY KEY / PARTITION BY key of the table, which ClickHouse cannot alter`,
		},
		{
			name:  "remove_key_column",
			app:   "n9",
			setup: []m.Operation{pony("n9")},
			ops:   []m.Operation{&m.RemoveField{ModelName: "pony", Name: "id"}},
			want:  `clickhouse: RemoveField n9.Pony.id: the column is part of the ORDER BY / PRIMARY KEY / PARTITION BY key of the table, which ClickHouse cannot alter`,
		},
		{
			name:  "drop_primary_key",
			app:   "n10",
			setup: []m.Operation{pony("n10")},
			ops: []m.Operation{&m.AlterField{ModelName: "pony", Name: "id",
				Field: m.Field{Type: m.Int, Size: 64, AutoIncrement: true}}},
			want: `clickhouse: AlterField n10.Pony.id: the primary key of a ClickHouse table is its sorting key and cannot be changed by ALTER TABLE`,
		},
		{
			name:  "alter_unique_together",
			app:   "n11",
			setup: []m.Operation{pony("n11", chField("color", m.Field{Type: m.String}))},
			ops:   []m.Operation{&m.AlterUniqueTogether{Name: "pony", UniqueTogether: [][]string{{"name", "color"}}}},
			want:  `clickhouse: AlterUniqueTogether n11.Pony: ClickHouse has no unique constraints or unique indexes`,
		},
		{
			name:  "add_unique_constraint",
			app:   "n12",
			setup: []m.Operation{pony("n12")},
			ops: []m.Operation{&m.AddConstraint{ModelName: "pony", Constraint: &m.UniqueConstraint{
				Name: "uq_n12_pony_name", Fields: []string{"name"}}}},
			want: `clickhouse: AddConstraint n12.Pony: ClickHouse has no unique constraints or unique indexes (constraint "uq_n12_pony_name")`,
		},
		{
			name:  "add_index_without_type",
			app:   "n13",
			setup: []m.Operation{pony("n13")},
			ops: []m.Operation{&m.AddIndex{ModelName: "pony", Index: m.Index{
				Name: "idx_n13_pony_name", Fields: []m.IndexField{{Column: "name"}}}}},
			want: `clickhouse: AddIndex n13.Pony (index "idx_n13_pony_name"): ClickHouse only has data-skipping indexes, so Index.Type is required (for example "minmax", "set(100)" or "bloom_filter")`,
		},
		{
			name:  "add_unique_index",
			app:   "n14",
			setup: []m.Operation{pony("n14")},
			ops: []m.Operation{&m.AddIndex{ModelName: "pony", Index: m.Index{
				Name: "idx_n14_pony_name", Type: "minmax", Unique: true, Fields: []m.IndexField{{Column: "name"}}}}},
			want: `clickhouse: AddIndex n14.Pony (index "idx_n14_pony_name"): ClickHouse has no unique constraints or unique indexes`,
		},
		{
			name:  "add_index_with_sort_order",
			app:   "n15",
			setup: []m.Operation{pony("n15")},
			ops: []m.Operation{&m.AddIndex{ModelName: "pony", Index: m.Index{
				Name: "idx_n15_pony_name", Type: "minmax", Fields: []m.IndexField{{Column: "name", Sort: "DESC"}}}}},
			want: `clickhouse: AddIndex n15.Pony (index "idx_n15_pony_name"): a ClickHouse data-skipping index has no column order, collation or prefix length`,
		},
		{
			name:  "add_partial_index",
			app:   "n16",
			setup: []m.Operation{pony("n16")},
			ops: []m.Operation{&m.AddIndex{ModelName: "pony", Index: m.Index{
				Name: "idx_n16_pony_name", Type: "minmax", Where: "`name` != ''", Fields: []m.IndexField{{Column: "name"}}}}},
			want: "clickhouse: AddIndex n16.Pony (index \"idx_n16_pony_name\"): a ClickHouse data-skipping index has no condition",
		},
		{
			name: "rename_index",
			app:  "n17",
			setup: []m.Operation{pony("n17"), &m.AddIndex{ModelName: "pony", Index: m.Index{
				Name: "idx_n17_pony_a", Type: "minmax", Fields: []m.IndexField{{Column: "name"}}}}},
			ops:  []m.Operation{&m.RenameIndex{ModelName: "pony", OldName: "idx_n17_pony_a", NewName: "idx_n17_pony_b"}},
			want: `clickhouse: RenameIndex n17.Pony: ClickHouse cannot rename index "idx_n17_pony_a"; remove it and add "idx_n17_pony_b" instead`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db, _ := chOpen(t)
			app := c.app
			p := harness.New(t, db, app)
			var deps []m.Key
			var migs []*m.Migration
			if len(c.setup) > 0 {
				setup := &m.Migration{App: app, Name: "0001_setup", Operations: c.setup}
				migs = append(migs, setup)
				deps = []m.Key{setup.Key()}
			}
			target := &m.Migration{App: app, Name: "0002_change", Dependencies: deps, Operations: c.ops}
			migs = append(migs, target)
			p.Register(migs...)
			if len(deps) > 0 {
				p.MustMigrate(deps[0])
			}
			err := p.Migrate(target.Key())
			if err == nil {
				t.Fatalf("migration succeeded, want %q", c.want)
			}
			var ns *m.NotSupportedError
			if !errors.As(err, &ns) {
				t.Fatalf("error %T (%v), want a *migrations.NotSupportedError", err, err)
			}
			if ns.Msg != c.want {
				t.Fatalf("message:\n got %q\nwant %q", ns.Msg, c.want)
			}
			// The failed migration was not recorded.
			applied, aerr := p.Executor().Recorder.AppliedMigrations()
			if aerr != nil {
				t.Fatal(aerr)
			}
			for _, k := range applied {
				if k == target.Key() {
					t.Fatal("the failed migration was recorded as applied")
				}
			}
		})
	}
}
