package gormgate_test

import (
	"bytes"
	"sync"

	"gorm.io/gorm"
)

// yourDriver stands in for the gorm driver of your database. gormgate
// itself depends only on gorm.io/gorm, so the examples cannot import one;
// in a real project this call is postgres.Open(dsn), mysql.Open(dsn),
// sqlite.Open(path) and so on.
func yourDriver(dsn string) gorm.Dialector { return nil }

// safeBuffer is a bytes.Buffer a command may write to from any goroutine.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
