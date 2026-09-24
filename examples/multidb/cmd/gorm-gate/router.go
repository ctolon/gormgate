package main

import "github.com/ctolon/gormgate"

// reportsRouter keeps the analytics app on the "reports" database and every
// other app off it. Returning nil means "no opinion": the next router
// decides, and if none does, the model is migrated.
type reportsRouter struct{}

func (reportsRouter) AllowMigrate(db, app string, h gormgate.Hints) *bool {
	switch {
	case app == "analytics":
		return gormgate.Ptr(db == "reports")
	case db == "reports":
		return gormgate.Ptr(false)
	default:
		return nil
	}
}
