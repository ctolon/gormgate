package migrations

import (
	"reflect"
	"runtime"
	"strings"
)

// deconstructStruct lists the exported non-zero fields of a struct, or of a
// pointer to one, in declaration order. It is how a value type states the
// arguments needed to rebuild it, which is what the migration writer emits.
func deconstructStruct(x any) []KV {
	v := reflect.ValueOf(x)
	for v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface {
		if v.IsNil() {
			return nil
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return []KV{{Value: x}}
	}
	t := v.Type()
	var kv []KV
	for i := range t.NumField() {
		sf := t.Field(i)
		if !sf.IsExported() || sf.Anonymous {
			continue
		}
		fv := v.Field(i)
		if fv.IsZero() {
			continue
		}
		val := fv.Interface()
		if fv.Kind() == reflect.Func {
			val = funcRef(FuncName(val))
		}
		kv = append(kv, KV{Key: sf.Name, Value: val})
	}
	return kv
}

// FuncName returns the fully qualified name of a function value, e.g.
// "example.com/app/blog/migrations.forwards".
func FuncName(fn any) string {
	v := reflect.ValueOf(fn)
	if v.Kind() != reflect.Func || v.IsNil() {
		return ""
	}
	f := runtime.FuncForPC(v.Pointer())
	if f == nil {
		return ""
	}
	return strings.TrimSuffix(f.Name(), "-fm")
}
