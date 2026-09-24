package writer

// This file is named like a migration file on purpose: the serializer treats
// a function whose source file matches `^\d{4}_.*\.go$` and that lives in the
// migrations package being written as a function that will disappear when the
// squashed migrations are deleted, and flags the migration as needing manual
// porting. TestWriter_NeedsManualPorting relies on that.

import m "github.com/ctolon/gormgate/migrations"

func sampleForwards(apps *m.Apps, ed m.SchemaEditor) error { return nil }
