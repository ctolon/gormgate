package base

import (
	"encoding/hex"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode"

	m "github.com/ctolon/gormgate/migrations"
)

// QuoteString renders a standard SQL string literal: the text wrapped in
// single quotes, with every single quote inside it doubled.
func QuoteString(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// QuoteValueOptions spells out the parts of an SQL literal that differ per
// vendor.
type QuoteValueOptions struct {
	// True and False spell a boolean.
	True, False string
	// TimeFormat is the layout of a time literal.
	TimeFormat string
	// Bytes renders a byte slice; nil falls back to the standard X'..'
	// hexadecimal literal.
	Bytes func([]byte) string
}

// StandardQuoteValue renders common Go values as SQL literals, with the
// vendor-specific spellings taken from o.
func StandardQuoteValue(v any, o QuoteValueOptions) (string, error) {
	switch x := v.(type) {
	case nil:
		return "NULL", nil
	case *m.GoExpr:
		return "", goExprError(x)
	case string:
		return QuoteString(x), nil
	case bool:
		if x {
			return o.True, nil
		}
		return o.False, nil
	case int:
		return strconv.FormatInt(int64(x), 10), nil
	case int8:
		return strconv.FormatInt(int64(x), 10), nil
	case int16:
		return strconv.FormatInt(int64(x), 10), nil
	case int32:
		return strconv.FormatInt(int64(x), 10), nil
	case int64:
		return strconv.FormatInt(x, 10), nil
	case uint:
		return strconv.FormatUint(uint64(x), 10), nil
	case uint8:
		return strconv.FormatUint(uint64(x), 10), nil
	case uint16:
		return strconv.FormatUint(uint64(x), 10), nil
	case uint32:
		return strconv.FormatUint(uint64(x), 10), nil
	case uint64:
		return strconv.FormatUint(x, 10), nil
	case float32:
		return formatFloat(float64(x), 32)
	case float64:
		return formatFloat(x, 64)
	case time.Time:
		return QuoteString(x.Format(o.TimeFormat)), nil
	case []byte:
		if o.Bytes != nil {
			return o.Bytes(x), nil
		}
		return "X'" + hex.EncodeToString(x) + "'", nil
	case fmt.Stringer:
		return QuoteString(x.String()), nil
	}
	return "", fmt.Errorf("gormgate: cannot quote value %v of type %T", v, v)
}

// goExprError reports that a prompt-entered default which isn't a literal
// only becomes a value once the migration is compiled.
func goExprError(x *m.GoExpr) error {
	return fmt.Errorf("gormgate: the default %s can only be applied by a compiled migration: run makemigrations, then migrate", x.Source)
}

func formatFloat(f float64, bits int) (string, error) {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return "", fmt.Errorf("gormgate: cannot quote %v", f)
	}
	return strconv.FormatFloat(f, 'g', -1, bits), nil
}

// GormLiteralOptions spells out what gorm's own rendering of a bound
// variable varies by dialector.
type GormLiteralOptions struct {
	// Escaper is the quote character the dialector passes to
	// logger.ExplainSQL; it is doubled inside the value.
	Escaper string
	// True and False spell a boolean. They are empty where the dialector
	// leaves booleans to ExplainSQL, which writes true/false.
	True, False string
	// Bytes renders a byte slice; nil is ExplainSQL's own rule, which
	// quotes printable bytes as a string and writes <binary> for the rest.
	Bytes func([]byte) string
}

// gormTimeFormat and gormZeroTime are logger.tmFmtWithMS and
// logger.tmFmtZero, the layouts gorm's ExplainSQL renders a time with.
const (
	gormTimeFormat = "2006-01-02 15:04:05.999"
	gormZeroTime   = "0000-00-00 00:00:00"
)

// GormLiteral renders a value the way gorm's logger.ExplainSQL renders a
// bound variable, which is how gorm's Migrator.FullDataTypeOf writes a
// column's DEFAULT clause. A column created by gormgate therefore carries
// the same default definition, character for character, as one created by
// gorm's AutoMigrate.
//
// It is not ExplainSQL's reflection fallback for unknown types: a value
// gormgate cannot render exactly is an error rather than a guess.
func GormLiteral(v any, o GormLiteralOptions) (string, error) {
	quote := func(s string) string {
		return o.Escaper + strings.ReplaceAll(s, o.Escaper, o.Escaper+o.Escaper) + o.Escaper
	}
	switch x := v.(type) {
	case nil:
		return "NULL", nil
	case *m.GoExpr:
		return "", goExprError(x)
	case bool:
		if o.True != "" || o.False != "" {
			if x {
				return o.True, nil
			}
			return o.False, nil
		}
		return strconv.FormatBool(x), nil
	case string:
		return quote(x), nil
	case []byte:
		if o.Bytes != nil {
			return o.Bytes(x), nil
		}
		s := string(x)
		for _, r := range s {
			if !unicode.IsPrint(r) {
				return quote("<binary>"), nil
			}
		}
		return quote(s), nil
	case time.Time:
		if x.IsZero() {
			return quote(gormZeroTime), nil
		}
		return quote(x.Format(gormTimeFormat)), nil
	case int:
		return strconv.FormatInt(int64(x), 10), nil
	case int8:
		return strconv.FormatInt(int64(x), 10), nil
	case int16:
		return strconv.FormatInt(int64(x), 10), nil
	case int32:
		return strconv.FormatInt(int64(x), 10), nil
	case int64:
		return strconv.FormatInt(x, 10), nil
	case uint:
		return strconv.FormatUint(uint64(x), 10), nil
	case uint8:
		return strconv.FormatUint(uint64(x), 10), nil
	case uint16:
		return strconv.FormatUint(uint64(x), 10), nil
	case uint32:
		return strconv.FormatUint(uint64(x), 10), nil
	case uint64:
		return strconv.FormatUint(x, 10), nil
	case float32:
		return strconv.FormatFloat(float64(x), 'f', -1, 32), nil
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64), nil
	case fmt.Stringer:
		return quote(x.String()), nil
	}
	return "", fmt.Errorf("gormgate: cannot render default value %v of type %T", v, v)
}
