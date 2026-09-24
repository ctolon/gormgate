//go:build integration

package itest

import (
	"os"
	"slices"
	"strings"
	"testing"

	"gorm.io/gorm"

	"github.com/ctolon/gormgate/itest/internal/conformance"
	"github.com/ctolon/gormgate/itest/internal/dbtest"
)

// conformanceVendors are the vendor keys the conformance suite runs on;
// GORMGATE_ITEST_VENDORS narrows it (comma separated).
func conformanceEnv(key string) conformance.Env {
	open := func(t testing.TB) *gorm.DB {
		db, _ := dbtest.Open(t, key)
		return db
	}
	return conformance.Env{A: open, B: open, Norm: conformance.Normalizers[key]}
}

func TestConformance(t *testing.T) {
	for _, key := range vendorsUnderTest(t) {
		key := key
		t.Run(key, func(t *testing.T) {
			conformance.Run(t, conformanceEnv(key), conformance.Cases)
		})
	}
}

func vendorsUnderTest(t *testing.T) []string {
	if v := os.Getenv("GORMGATE_ITEST_VENDORS"); v != "" {
		return splitComma(v)
	}
	return dbtest.Keys()
}

func splitComma(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// skipUnlessVendor skips a test that speaks to one specific database when
// that vendor is not among the ones under test, so that a run restricted
// with GORMGATE_ITEST_VENDORS does not try to reach a server it was never
// asked about -- which, with GORMGATE_ITEST_REQUIRE set, would be a
// failure rather than a skip.
func skipUnlessVendor(t *testing.T, key string) {
	t.Helper()
	if slices.Contains(vendorsUnderTest(t), key) {
		return
	}
	t.Skipf("%s is not among the vendors under test", key)
}
