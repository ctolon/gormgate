package postgres

import (
	m "github.com/ctolon/gormgate/migrations"
)

// CreateExtension installs a PostgreSQL extension, and drops it again when
// the migration is unapplied. It changes no migration state.
//
// django: contrib/postgres/operations.py CreateExtension
type CreateExtension struct {
	baseOp
	// Name is the extension to install.
	Name string
	// Hints are passed through to the database routers.
	Hints map[string]any
}

// Category reports the symbol makemigrations prints for this operation.
func (o *CreateExtension) Category() m.Category { return m.CategoryAddition }

// StateForwards does nothing: an extension is not part of the model state.
//
// django: contrib/postgres/operations.py CreateExtension.state_forwards
func (o *CreateExtension) StateForwards(string, *m.ProjectState) error { return nil }

// DatabaseForwards installs the extension unless it is already there.
//
// django: contrib/postgres/operations.py CreateExtension.database_forwards
func (o *CreateExtension) DatabaseForwards(app string, ed m.SchemaEditor, _, _ *m.ProjectState) error {
	if !hasExtensions(ed) || !routerAllows(ed, app, o.Hints) {
		return nil
	}
	pg, err := editorOf(ed)
	if err != nil {
		return err
	}
	exists, err := extensionExists(ed, o.Name)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	return ed.Execute("CREATE EXTENSION IF NOT EXISTS " + pg.QuoteName(o.Name))
}

// DatabaseBackwards drops the extension if it is there.
//
// Django checks only the routers here, not the vendor, which would leave
// the pg_extension query to run on a database that has no such table; the
// vendor guard of the forwards direction is applied to both.
//
// django: contrib/postgres/operations.py CreateExtension.database_backwards
func (o *CreateExtension) DatabaseBackwards(app string, ed m.SchemaEditor, _, _ *m.ProjectState) error {
	if !hasExtensions(ed) || !routerAllows(ed, app, o.Hints) {
		return nil
	}
	pg, err := editorOf(ed)
	if err != nil {
		return err
	}
	exists, err := extensionExists(ed, o.Name)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	return ed.Execute("DROP EXTENSION IF EXISTS " + pg.QuoteName(o.Name))
}

// ReferencesField reports true: the operation names no model, so it is
// never optimized past a field change.
func (o *CreateExtension) ReferencesField(string, string, string) bool { return true }

// Describe is the line makemigrations and migrate --plan print.
//
// django: contrib/postgres/operations.py CreateExtension.describe
func (o *CreateExtension) Describe() string { return "Creates extension " + o.Name }

// MigrationNameFragment is this operation's contribution to a suggested
// migration name.
//
// django: contrib/postgres/operations.py CreateExtension.migration_name_fragment
func (o *CreateExtension) MigrationNameFragment() string { return "create_extension_" + o.Name }

// Reduce is the default reduction: nothing is optimized across an
// extension operation.
func (o *CreateExtension) Reduce(other m.Operation, _ string) ([]m.Operation, m.ReduceKind) {
	return baseReduce(o, other)
}

// extensionExists reports whether the extension is installed.
//
// django: contrib/postgres/operations.py CreateExtension.extension_exists
func extensionExists(ed m.SchemaEditor, name string) (bool, error) {
	var n int64
	err := ed.Connection().DB().Raw("SELECT count(*) FROM pg_extension WHERE extname = ?", name).Scan(&n).Error
	return n > 0, err
}

// The extensions Django ships a shortcut for. Each returns the
// CreateExtension the Django class of the same name is, so that
// postgres.HStoreExtension() reads as Django's HStoreExtension() does; set
// Hints on the result to pass router hints.
//
// django: contrib/postgres/operations.py

// BloomExtension installs the bloom extension.
//
// django: contrib/postgres/operations.py BloomExtension
func BloomExtension() *CreateExtension { return &CreateExtension{Name: "bloom"} }

// BtreeGinExtension installs the btree_gin extension.
//
// django: contrib/postgres/operations.py BtreeGinExtension
func BtreeGinExtension() *CreateExtension { return &CreateExtension{Name: "btree_gin"} }

// BtreeGistExtension installs the btree_gist extension.
//
// django: contrib/postgres/operations.py BtreeGistExtension
func BtreeGistExtension() *CreateExtension { return &CreateExtension{Name: "btree_gist"} }

// CITextExtension installs the citext extension.
//
// django: contrib/postgres/operations.py CITextExtension
func CITextExtension() *CreateExtension { return &CreateExtension{Name: "citext"} }

// CryptoExtension installs the pgcrypto extension.
//
// django: contrib/postgres/operations.py CryptoExtension
func CryptoExtension() *CreateExtension { return &CreateExtension{Name: "pgcrypto"} }

// HStoreExtension installs the hstore extension.
//
// django: contrib/postgres/operations.py HStoreExtension
func HStoreExtension() *CreateExtension { return &CreateExtension{Name: "hstore"} }

// TrigramExtension installs the pg_trgm extension.
//
// django: contrib/postgres/operations.py TrigramExtension
func TrigramExtension() *CreateExtension { return &CreateExtension{Name: "pg_trgm"} }

// UnaccentExtension installs the unaccent extension.
//
// django: contrib/postgres/operations.py UnaccentExtension
func UnaccentExtension() *CreateExtension { return &CreateExtension{Name: "unaccent"} }
