package migrations

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"gorm.io/gorm"
)

// Objects reads and writes the rows of a historical model from RunGo code.
// It addresses the table and columns the model had at the point in the
// migration history being run, never those of the current Go struct.
//
// django: apps/registry.py Apps.get_model(...).objects
type Objects struct {
	Model *Model
	db    *gorm.DB
}

// Objects returns the manager bound to the schema editor's connection
// (inside the migration's transaction when there is one).
func (m *Model) Objects(ed SchemaEditor) *Objects {
	return &Objects{Model: m, db: ed.Connection().DB()}
}

// Columns returns the model's column names in field order.
func (m *Model) Columns() []string {
	out := make([]string, len(m.Fields))
	for i, f := range m.Fields {
		out[i] = f.Column
	}
	return out
}

// DB returns a gorm handle on the model's table.
func (o *Objects) DB() *gorm.DB { return o.db.Table(o.Model.Table) }

// check reports the column names in values that the historical model does
// not have.
func (o *Objects) check(values map[string]any) error {
	var unknown []string
	for k := range values {
		if o.Model.Field(k) == nil {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		slices.Sort(unknown)
		return &FieldDoesNotExist{Msg: fmt.Sprintf("%s has no field named '%s'", o.Model.Name, strings.Join(unknown, "', '"))}
	}
	return nil
}

// where narrows db by an equality test per entry, joined with AND, in a
// fixed key order so that the SQL is deterministic. Anything else needs the
// gorm handle from DB.
func (o *Objects) where(db *gorm.DB, where map[string]any) (*gorm.DB, error) {
	if err := o.check(where); err != nil {
		return nil, err
	}
	for _, k := range slices.Sorted(maps.Keys(where)) {
		db = db.Where(db.Statement.Quote(k)+" = ?", where[k])
	}
	return db, nil
}

// Create inserts rows; each row maps field names to values.
func (o *Objects) Create(rows ...map[string]any) error {
	for _, r := range rows {
		if err := o.check(r); err != nil {
			return err
		}
		if err := o.DB().Create(r).Error; err != nil {
			return err
		}
	}
	return nil
}

// Find returns the rows matching where (all rows for nil) ordered by the
// primary key.
func (o *Objects) Find(where map[string]any) ([]map[string]any, error) {
	db, err := o.where(o.DB(), where)
	if err != nil {
		return nil, err
	}
	for _, pk := range o.Model.PK() {
		db = db.Order(db.Statement.Quote(pk.Column))
	}
	var out []map[string]any
	return out, db.Select(o.Model.Columns()).Find(&out).Error
}

// Count counts the rows matching where.
func (o *Objects) Count(where map[string]any) (int64, error) {
	db, err := o.where(o.DB(), where)
	if err != nil {
		return 0, err
	}
	var n int64
	return n, db.Count(&n).Error
}

// Update sets values on the rows matching where and returns the number of
// rows affected. where must select something: an empty or nil filter is
// refused, because it is nearly always a filter that was meant to be built
// and was not. UpdateAll is the deliberate whole-table form.
func (o *Objects) Update(where, values map[string]any) (int64, error) {
	if len(where) == 0 {
		return 0, &ValueError{Msg: "Objects.Update needs a filter; use UpdateAll to update every row"}
	}
	if err := o.check(values); err != nil {
		return 0, err
	}
	db, err := o.where(o.DB(), where)
	if err != nil {
		return 0, err
	}
	res := db.Updates(values)
	return res.RowsAffected, res.Error
}

// UpdateAll sets values on every row of the table and returns the number of
// rows affected.
func (o *Objects) UpdateAll(values map[string]any) (int64, error) {
	if err := o.check(values); err != nil {
		return 0, err
	}
	res := o.DB().Session(&gorm.Session{AllowGlobalUpdate: true}).Updates(values)
	return res.RowsAffected, res.Error
}

// Delete removes the rows matching where and returns the number of rows
// affected. As with Update, an empty or nil filter is refused; DeleteAll
// empties the table.
func (o *Objects) Delete(where map[string]any) (int64, error) {
	if len(where) == 0 {
		return 0, &ValueError{Msg: "Objects.Delete needs a filter; use DeleteAll to delete every row"}
	}
	db, err := o.where(o.DB(), where)
	if err != nil {
		return 0, err
	}
	res := db.Delete(map[string]any{})
	return res.RowsAffected, res.Error
}

// DeleteAll removes every row of the table and returns the number of rows
// affected.
func (o *Objects) DeleteAll() (int64, error) {
	res := o.DB().Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(map[string]any{})
	return res.RowsAffected, res.Error
}
