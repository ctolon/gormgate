// Package optimizer shortens a list of migration operations by letting each
// operation reduce the ones that follow it, for example folding an AddField
// into the CreateModel before it.
//
// django: db/migrations/optimizer.py
package optimizer

import m "github.com/ctolon/gormgate/migrations"

// Optimize returns an equal or shorter operation list, merging operations
// where possible (for example CreateModel + AddField into one CreateModel).
//
// django: optimizer.py MigrationOptimizer.optimize
func Optimize(operations []m.Operation, app string) []m.Operation {
	// Each pass applies at most one reduction, which shortens the list or
	// replaces two operations by fewer, so the loop reaches a fixed point.
	// Django compares the pass's result with its input; the pass reports
	// whether it reduced anything instead, which needs no comparison of
	// the operations themselves. Operation is an exported interface, so
	// those values need not be comparable at all.
	for {
		result, reduced := optimizeInner(operations, app)
		if !reduced {
			return result
		}
		operations = result
	}
}

// optimizeInner makes one reduction pass. It returns as soon as it has
// applied a reduction, so Optimize can run it again from the start; reduced
// says whether it did.
//
// django: optimizer.py MigrationOptimizer.optimize_inner
func optimizeInner(operations []m.Operation, app string) (result []m.Operation, reduced bool) {
	var newOps []m.Operation
	for i, op := range operations {
		right := true
		emitted := false
		for j, other := range operations[i+1:] {
			result, kind := op.Reduce(other, app)
			if kind == m.ReduceReplace {
				inBetween := operations[i+1 : i+j+1]
				if right {
					newOps = append(newOps, inBetween...)
					newOps = append(newOps, result...)
				} else if allReduceThrough(inBetween, other, app) {
					newOps = append(newOps, result...)
					newOps = append(newOps, inBetween...)
				} else {
					// op cannot move past what lies between,
					// so it stays where it is and the pass
					// goes on with the next operation.
					newOps = append(newOps, op)
					emitted = true
					break
				}
				newOps = append(newOps, operations[i+j+2:]...)
				return newOps, true
			} else if kind == m.ReduceBlock {
				right = false
			}
		}
		if !emitted {
			newOps = append(newOps, op)
		}
	}
	return newOps, false
}

// allReduceThrough reports whether other can be moved back past every one
// of ops without changing what the migration does.
func allReduceThrough(ops []m.Operation, other m.Operation, app string) bool {
	for _, op := range ops {
		if _, kind := op.Reduce(other, app); kind != m.ReduceThrough {
			return false
		}
	}
	return true
}
