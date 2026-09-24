package migrations

import (
	"errors"
	"testing"
)

// thingObjects is a manager over a model with one column, with no database
// behind it: the calls under test are refused before they reach one.
func thingObjects() *Objects {
	st := NewProjectState()
	st.AddModel(&ModelState{App: "app", Name: "Thing", Table: "app_thing", Fields: Fields{
		{Name: "id", Field: Field{Type: Int, Size: 64, PrimaryKey: true, AutoIncrement: true}},
		{Name: "views", Field: Field{Type: Int, Size: 64}},
	}})
	return &Objects{Model: st.MustApps().MustModel("app", "Thing")}
}

// TestUpdateAndDeleteNeedAFilter checks that a filter that was meant to be
// built and was not does not silently rewrite the whole table, and that a
// nil filter and an empty one are treated the same way.
func TestUpdateAndDeleteNeedAFilter(t *testing.T) {
	o := thingObjects()
	for _, where := range []map[string]any{nil, {}} {
		if _, err := o.Update(where, map[string]any{"views": 0}); err == nil {
			t.Fatalf("Update(%v) must be refused", where)
		} else {
			var ve *ValueError
			if !errors.As(err, &ve) {
				t.Fatalf("Update(%v): got %T, want *ValueError", where, err)
			}
		}
		if _, err := o.Delete(where); err == nil {
			t.Fatalf("Delete(%v) must be refused", where)
		}
	}
}

// TestUpdateAllChecksItsValues checks that UpdateAll still rejects a column
// the historical model does not have, so that skipping the filter does not
// skip the column check.
func TestUpdateAllChecksItsValues(t *testing.T) {
	o := thingObjects()
	_, err := o.UpdateAll(map[string]any{"nosuch": 1})
	var fe *FieldDoesNotExist
	if !errors.As(err, &fe) {
		t.Fatalf("got %T (%v), want *FieldDoesNotExist", err, err)
	}
}
