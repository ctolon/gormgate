package conformance

import "regexp"

// castSuffix matches PostgreSQL's rendering of the type a stored
// expression was written with, e.g. 'x'::character varying.
var castSuffix = regexp.MustCompile(`::"?[a-zA-Z][a-zA-Z0-9_ ]*"?(\([0-9, ]*\))?(\[\])?`)

// stripCasts removes those suffixes: ALTER COLUMN ... TYPE keeps the cast
// of the type the default was created with, while a fresh CREATE TABLE
// renders it with the new type. The value is the same.
func stripCasts(s string) string { return castSuffix.ReplaceAllString(s, "") }
