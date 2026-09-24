// Package migrations holds the model of a migration: the operations a
// migration is made of, the project state they mutate, the historical models
// that state renders into, and the schema-editor interface the backends
// implement to turn operations into DDL.
//
// It is a port of Django 6.0's django.db.migrations package. Comments of the
// form "django: <file> <symbol>" name the Django source each declaration
// comes from, so parity can be audited.
package migrations
