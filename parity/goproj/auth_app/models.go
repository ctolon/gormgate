//lint:file-ignore ST1003 The package name is Django's app label: the
// parity harness compares this project with a Django project whose app is
// called auth_app, and the label is what every message prints.

// Package auth_app holds the parity project's auth models.
package auth_app

// Models returns the app's models.
func Models() []any { return nil }
