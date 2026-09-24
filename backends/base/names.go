package base

import (
	"crypto/md5"
	"encoding/hex"
	"strings"
)

// NamesDigest generates a digest of a set of arguments that can be used to
// shorten identifying names.
//
// django: db/backends/utils.py names_digest
func NamesDigest(length int, args ...string) string {
	h := md5.New()
	for _, a := range args {
		h.Write([]byte(a))
	}
	return hex.EncodeToString(h.Sum(nil))[:length]
}

// SplitIdentifier splits "namespace"."name" into its two parts.
//
// django: db/backends/utils.py split_identifier
func SplitIdentifier(identifier string) (string, string) {
	ns, name, ok := strings.Cut(identifier, `"."`)
	if !ok {
		return "", strings.Trim(identifier, `"`)
	}
	return strings.Trim(ns, `"`), strings.Trim(name, `"`)
}

// TruncateName shortens an identifier to a repeatable mangled version of
// the given length.
//
// django: db/backends/utils.py truncate_name
func TruncateName(identifier string, length, hashLen int) string {
	ns, name := SplitIdentifier(identifier)
	if length <= 0 || len(name) <= length {
		return identifier
	}
	digest := NamesDigest(hashLen, name)
	prefix := ""
	if ns != "" {
		prefix = ns + `"."`
	}
	return prefix + name[:length-hashLen] + digest
}

// CreateIndexName generates a unique name for an index or unique
// constraint: table, columns, and a digest plus suffix.
//
// django: base/schema.py BaseDatabaseSchemaEditor._create_index_name
func CreateIndexName(maxLength int, table string, columns []string, suffix string) string {
	_, table = SplitIdentifier(table)
	hashSuffix := NamesDigest(8, append([]string{table}, columns...)...) + suffix
	if maxLength <= 0 {
		maxLength = 200
	}
	name := table + "_" + strings.Join(columns, "_") + "_" + hashSuffix
	if len(name) <= maxLength {
		return name
	}
	if len(hashSuffix) > maxLength/3 {
		hashSuffix = hashSuffix[:maxLength/3]
	}
	other := (maxLength-len(hashSuffix))/2 - 1
	cols := strings.Join(columns, "_")
	if len(table) > other {
		table = table[:other]
	}
	if len(cols) > other {
		cols = cols[:other]
	}
	name = table + "_" + cols + "_" + hashSuffix
	if name[0] == '_' || (name[0] >= '0' && name[0] <= '9') {
		name = "D" + name[:len(name)-1]
	}
	return name
}
