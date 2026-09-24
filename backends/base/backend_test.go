package base

import (
	"errors"
	"strings"
	"testing"
)

// TestNoBackendError checks the diagnostic a user gets when they forget to
// import a backend: it must be matchable with errors.Is, name the
// dialector they asked for, and list the backends the program does have.
func TestNoBackendError(t *testing.T) {
	Register(Detector{Name: "examplesql", Dialector: "examplesql", Backend: &Backend{Vendor: "examplesql"}})

	err := noBackendError("nosuchdialector")
	if !errors.Is(err, ErrNoBackend) {
		t.Errorf("errors.Is(err, ErrNoBackend) = false, err = %v", err)
	}
	for _, want := range []string{"nosuchdialector", "examplesql"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q: %v", want, err)
		}
	}
}

// TestRegisteredNamesAreSortedAndUnique pins the list the error prints:
// two backends may register the same name for different dialectors, and
// the order must not depend on import order.
func TestRegisteredNamesAreSortedAndUnique(t *testing.T) {
	Register(Detector{Name: "zzz", Dialector: "zzz", Backend: &Backend{Vendor: "zzz"}})
	Register(Detector{Name: "aaa", Dialector: "aaa1", Backend: &Backend{Vendor: "aaa"}})
	Register(Detector{Name: "aaa", Dialector: "aaa2", Backend: &Backend{Vendor: "aaa"}})

	names := registeredNames()
	if !sortedUnique(names) {
		t.Errorf("registeredNames() = %v, want sorted and deduplicated", names)
	}
	var aaa int
	for _, n := range names {
		if n == "aaa" {
			aaa++
		}
	}
	if aaa != 1 {
		t.Errorf("the name registered twice appears %d times in %v", aaa, names)
	}
}

func sortedUnique(names []string) bool {
	for i := 1; i < len(names); i++ {
		if names[i-1] >= names[i] {
			return false
		}
	}
	return true
}
