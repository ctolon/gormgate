package graph

import (
	"errors"
	"strconv"
	"testing"

	m "github.com/ctolon/gormgate/migrations"
)

// django: tests/migrations/test_graph.py
//
// Django builds its graphs from (app_label, name) tuples; the Go port uses
// m.Key, and every assertion below is on the same values Django asserts.

func mk(app, name string) m.Key { return m.Key{App: app, Name: name} }

func keyList(keys ...m.Key) []m.Key { return keys }

func fmtKeys(keys []m.Key) string {
	if len(keys) == 0 {
		return "[]"
	}
	s := "["
	for i, key := range keys {
		if i > 0 {
			s += ", "
		}
		s += FormatKey(key)
	}
	return s + "]"
}

func assertKeys(t *testing.T, what string, got, want []m.Key) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s:\n got %s\nwant %s", what, fmtKeys(got), fmtKeys(want))
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("%s:\n got %s\nwant %s", what, fmtKeys(got), fmtKeys(want))
		}
	}
}

func mustNodes(t *testing.T, g *Graph, keys ...m.Key) {
	t.Helper()
	for _, key := range keys {
		g.AddNode(key, nil)
	}
}

func mustDep(t *testing.T, g *Graph, migration string, child, parent m.Key) {
	t.Helper()
	if err := g.AddDependency(migration, child, parent, false); err != nil {
		t.Fatalf("AddDependency(%s, %s, %s): %v", migration, FormatKey(child), FormatKey(parent), err)
	}
}

func skipDep(t *testing.T, g *Graph, migration string, child, parent m.Key) {
	t.Helper()
	if err := g.AddDependency(migration, child, parent, true); err != nil {
		t.Fatalf("AddDependency(%s, %s, %s, skipValidation): %v", migration, FormatKey(child), FormatKey(parent), err)
	}
}

// assertNodeNotFound is assertRaisesMessage(NodeNotFoundError, msg).
func assertNodeNotFound(t *testing.T, err error, msg string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected NodeNotFoundError %q, got nil", msg)
	}
	var nf *m.NodeNotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("expected NodeNotFoundError %q, got %T: %v", msg, err, err)
	}
	if nf.Error() != msg {
		t.Fatalf("NodeNotFoundError message:\n got %q\nwant %q", nf.Error(), msg)
	}
}

func mustForwards(t *testing.T, g *Graph, target m.Key) []m.Key {
	t.Helper()
	plan, err := g.ForwardsPlan(target)
	if err != nil {
		t.Fatalf("ForwardsPlan(%s): %v", FormatKey(target), err)
	}
	return plan
}

func mustBackwards(t *testing.T, g *Graph, target m.Key) []m.Key {
	t.Helper()
	plan, err := g.BackwardsPlan(target)
	if err != nil {
		t.Fatalf("BackwardsPlan(%s): %v", FormatKey(target), err)
	}
	return plan
}

// TestGraph_SimpleGraph tests a basic dependency graph:
//
//	app_a:  0001 <-- 0002 <--- 0003 <-- 0004
//	                         /
//	app_b:  0001 <-- 0002 <-/
//
// django: tests/migrations/test_graph.py GraphTests.test_simple_graph
func TestGraph_SimpleGraph(t *testing.T) {
	g := New()
	mustNodes(t, g,
		mk("app_a", "0001"), mk("app_a", "0002"), mk("app_a", "0003"), mk("app_a", "0004"),
		mk("app_b", "0001"), mk("app_b", "0002"))
	mustDep(t, g, "app_a.0004", mk("app_a", "0004"), mk("app_a", "0003"))
	mustDep(t, g, "app_a.0003", mk("app_a", "0003"), mk("app_a", "0002"))
	mustDep(t, g, "app_a.0002", mk("app_a", "0002"), mk("app_a", "0001"))
	mustDep(t, g, "app_a.0003", mk("app_a", "0003"), mk("app_b", "0002"))
	mustDep(t, g, "app_b.0002", mk("app_b", "0002"), mk("app_b", "0001"))

	// Root migration case.
	assertKeys(t, "forwards_plan(app_a.0001)", mustForwards(t, g, mk("app_a", "0001")),
		keyList(mk("app_a", "0001")))
	// Branch B only.
	assertKeys(t, "forwards_plan(app_b.0002)", mustForwards(t, g, mk("app_b", "0002")),
		keyList(mk("app_b", "0001"), mk("app_b", "0002")))
	// Whole graph.
	assertKeys(t, "forwards_plan(app_a.0004)", mustForwards(t, g, mk("app_a", "0004")),
		keyList(
			mk("app_b", "0001"), mk("app_b", "0002"),
			mk("app_a", "0001"), mk("app_a", "0002"), mk("app_a", "0003"), mk("app_a", "0004")))
	// Reverse to b:0002.
	assertKeys(t, "backwards_plan(app_b.0002)", mustBackwards(t, g, mk("app_b", "0002")),
		keyList(mk("app_a", "0004"), mk("app_a", "0003"), mk("app_b", "0002")))
	// Roots and leaves.
	assertKeys(t, "root_nodes()", g.RootNodes(""), keyList(mk("app_a", "0001"), mk("app_b", "0001")))
	assertKeys(t, "leaf_nodes()", g.LeafNodes(""), keyList(mk("app_a", "0004"), mk("app_b", "0002")))
}

// TestGraph_ComplexGraph tests a complex dependency graph:
//
//	app_a:  0001 <-- 0002 <--- 0003 <-- 0004
//	              \        \ /         /
//	app_b:  0001 <-\ 0002 <-X         /
//	              \          \       /
//	app_c:         \ 0001 <-- 0002 <-
//
// django: tests/migrations/test_graph.py GraphTests.test_complex_graph
func TestGraph_ComplexGraph(t *testing.T) {
	g := New()
	mustNodes(t, g,
		mk("app_a", "0001"), mk("app_a", "0002"), mk("app_a", "0003"), mk("app_a", "0004"),
		mk("app_b", "0001"), mk("app_b", "0002"),
		mk("app_c", "0001"), mk("app_c", "0002"))
	mustDep(t, g, "app_a.0004", mk("app_a", "0004"), mk("app_a", "0003"))
	mustDep(t, g, "app_a.0003", mk("app_a", "0003"), mk("app_a", "0002"))
	mustDep(t, g, "app_a.0002", mk("app_a", "0002"), mk("app_a", "0001"))
	mustDep(t, g, "app_a.0003", mk("app_a", "0003"), mk("app_b", "0002"))
	mustDep(t, g, "app_b.0002", mk("app_b", "0002"), mk("app_b", "0001"))
	mustDep(t, g, "app_a.0004", mk("app_a", "0004"), mk("app_c", "0002"))
	mustDep(t, g, "app_c.0002", mk("app_c", "0002"), mk("app_c", "0001"))
	mustDep(t, g, "app_c.0001", mk("app_c", "0001"), mk("app_b", "0001"))
	mustDep(t, g, "app_c.0002", mk("app_c", "0002"), mk("app_a", "0002"))

	// Branch C only.
	assertKeys(t, "forwards_plan(app_c.0002)", mustForwards(t, g, mk("app_c", "0002")),
		keyList(mk("app_b", "0001"), mk("app_c", "0001"), mk("app_a", "0001"), mk("app_a", "0002"), mk("app_c", "0002")))
	// Whole graph.
	assertKeys(t, "forwards_plan(app_a.0004)", mustForwards(t, g, mk("app_a", "0004")),
		keyList(
			mk("app_b", "0001"), mk("app_c", "0001"), mk("app_a", "0001"), mk("app_a", "0002"),
			mk("app_c", "0002"), mk("app_b", "0002"), mk("app_a", "0003"), mk("app_a", "0004")))
	// Reverse to b:0001.
	assertKeys(t, "backwards_plan(app_b.0001)", mustBackwards(t, g, mk("app_b", "0001")),
		keyList(
			mk("app_a", "0004"), mk("app_c", "0002"), mk("app_c", "0001"),
			mk("app_a", "0003"), mk("app_b", "0002"), mk("app_b", "0001")))
	// Roots and leaves.
	assertKeys(t, "root_nodes()", g.RootNodes(""),
		keyList(mk("app_a", "0001"), mk("app_b", "0001"), mk("app_c", "0001")))
	assertKeys(t, "leaf_nodes()", g.LeafNodes(""),
		keyList(mk("app_a", "0004"), mk("app_b", "0002"), mk("app_c", "0002")))
}

// django: tests/migrations/test_graph.py GraphTests.test_circular_graph
func TestGraph_CircularGraph(t *testing.T) {
	g := New()
	mustNodes(t, g, mk("app_a", "0001"), mk("app_a", "0002"), mk("app_a", "0003"),
		mk("app_b", "0001"), mk("app_b", "0002"))
	mustDep(t, g, "app_a.0003", mk("app_a", "0003"), mk("app_a", "0002"))
	mustDep(t, g, "app_a.0002", mk("app_a", "0002"), mk("app_a", "0001"))
	mustDep(t, g, "app_a.0001", mk("app_a", "0001"), mk("app_b", "0002"))
	mustDep(t, g, "app_b.0002", mk("app_b", "0002"), mk("app_b", "0001"))
	mustDep(t, g, "app_b.0001", mk("app_b", "0001"), mk("app_a", "0003"))

	err := g.EnsureNotCyclic()
	var cd *m.CircularDependencyError
	if !errors.As(err, &cd) {
		t.Fatalf("EnsureNotCyclic: expected CircularDependencyError, got %v", err)
	}
}

// django: tests/migrations/test_graph.py GraphTests.test_circular_graph_2
func TestGraph_CircularGraph2(t *testing.T) {
	g := New()
	mustNodes(t, g, mk("A", "0001"), mk("C", "0001"), mk("B", "0001"))
	mustDep(t, g, "A.0001", mk("A", "0001"), mk("B", "0001"))
	mustDep(t, g, "B.0001", mk("B", "0001"), mk("A", "0001"))
	mustDep(t, g, "C.0001", mk("C", "0001"), mk("B", "0001"))

	err := g.EnsureNotCyclic()
	var cd *m.CircularDependencyError
	if !errors.As(err, &cd) {
		t.Fatalf("EnsureNotCyclic: expected CircularDependencyError, got %v", err)
	}
}

// django: tests/migrations/test_graph.py GraphTests.test_iterative_dfs
func TestGraph_IterativeDFS(t *testing.T) {
	g := New()
	root := mk("app_a", "1")
	g.AddNode(root, nil)
	expected := []m.Key{root}
	for i := 2; i < 750; i++ {
		parent := mk("app_a", strconv.Itoa(i-1))
		child := mk("app_a", strconv.Itoa(i))
		g.AddNode(child, nil)
		mustDep(t, g, strconv.Itoa(i), child, parent)
		expected = append(expected, child)
	}
	leaf := expected[len(expected)-1]

	assertKeys(t, "forwards_plan(leaf)", mustForwards(t, g, leaf), expected)

	reversed := make([]m.Key, len(expected))
	for i, key := range expected {
		reversed[len(expected)-1-i] = key
	}
	assertKeys(t, "backwards_plan(root)", mustBackwards(t, g, root), reversed)
}

// TestGraph_IterativeDFSComplexity checks that in a graph with merge
// migrations iterativeDFS traverses each node only once even if there are
// multiple paths leading to it.
//
// django: tests/migrations/test_graph.py GraphTests.test_iterative_dfs_complexity
func TestGraph_IterativeDFSComplexity(t *testing.T) {
	const n = 50
	g := New()
	for i := 1; i <= n; i++ {
		s := strconv.Itoa(i)
		mustNodes(t, g, mk("app_a", s), mk("app_b", s), mk("app_c", s))
	}
	for i := 1; i < n; i++ {
		s, next := strconv.Itoa(i), strconv.Itoa(i+1)
		// Django passes migration=None here; the Go signature takes a string.
		skipDep(t, g, "", mk("app_b", s), mk("app_a", s))
		skipDep(t, g, "", mk("app_c", s), mk("app_a", s))
		skipDep(t, g, "", mk("app_a", next), mk("app_b", s))
		skipDep(t, g, "", mk("app_a", next), mk("app_c", s))
	}
	if err := g.ValidateConsistency(); err != nil {
		t.Fatal(err)
	}
	var expected []m.Key
	for i := 1; i < n; i++ {
		s := strconv.Itoa(i)
		expected = append(expected, mk("app_a", s), mk("app_c", s), mk("app_b", s))
	}
	expected = append(expected, mk("app_a", strconv.Itoa(n)))
	assertKeys(t, "forwards_plan(app_a.50)", mustForwards(t, g, mk("app_a", strconv.Itoa(n))), expected)
}

// django: tests/migrations/test_graph.py GraphTests.test_plan_invalid_node
func TestGraph_PlanInvalidNode(t *testing.T) {
	g := New()
	const msg = "Node ('app_b', '0001') not a valid node"

	_, err := g.ForwardsPlan(mk("app_b", "0001"))
	assertNodeNotFound(t, err, msg)

	_, err = g.BackwardsPlan(mk("app_b", "0001"))
	assertNodeNotFound(t, err, msg)
}

// django: tests/migrations/test_graph.py GraphTests.test_missing_parent_nodes
func TestGraph_MissingParentNodes(t *testing.T) {
	g := New()
	mustNodes(t, g, mk("app_a", "0001"), mk("app_a", "0002"), mk("app_a", "0003"), mk("app_b", "0001"))
	mustDep(t, g, "app_a.0003", mk("app_a", "0003"), mk("app_a", "0002"))
	mustDep(t, g, "app_a.0002", mk("app_a", "0002"), mk("app_a", "0001"))

	err := g.AddDependency("app_a.0001", mk("app_a", "0001"), mk("app_b", "0002"), false)
	assertNodeNotFound(t, err,
		"Migration app_a.0001 dependencies reference nonexistent parent node ('app_b', '0002')")
}

// django: tests/migrations/test_graph.py GraphTests.test_missing_child_nodes
func TestGraph_MissingChildNodes(t *testing.T) {
	g := New()
	mustNodes(t, g, mk("app_a", "0001"))

	err := g.AddDependency("app_a.0002", mk("app_a", "0002"), mk("app_a", "0001"), false)
	assertNodeNotFound(t, err,
		"Migration app_a.0002 dependencies reference nonexistent child node ('app_a', '0002')")
}

// django: tests/migrations/test_graph.py GraphTests.test_validate_consistency_missing_parent
func TestGraph_ValidateConsistencyMissingParent(t *testing.T) {
	g := New()
	mustNodes(t, g, mk("app_a", "0001"))
	skipDep(t, g, "app_a.0001", mk("app_a", "0001"), mk("app_b", "0002"))

	assertNodeNotFound(t, g.ValidateConsistency(),
		"Migration app_a.0001 dependencies reference nonexistent parent node ('app_b', '0002')")
}

// django: tests/migrations/test_graph.py GraphTests.test_validate_consistency_missing_child
func TestGraph_ValidateConsistencyMissingChild(t *testing.T) {
	g := New()
	mustNodes(t, g, mk("app_b", "0002"))
	skipDep(t, g, "app_b.0002", mk("app_a", "0001"), mk("app_b", "0002"))

	assertNodeNotFound(t, g.ValidateConsistency(),
		"Migration app_b.0002 dependencies reference nonexistent child node ('app_a', '0001')")
}

// django: tests/migrations/test_graph.py GraphTests.test_validate_consistency_no_error
func TestGraph_ValidateConsistencyNoError(t *testing.T) {
	g := New()
	mustNodes(t, g, mk("app_a", "0001"), mk("app_b", "0002"))
	skipDep(t, g, "app_a.0001", mk("app_a", "0001"), mk("app_b", "0002"))

	if err := g.ValidateConsistency(); err != nil {
		t.Fatalf("ValidateConsistency: unexpected error %v", err)
	}
}

// TestGraph_ValidateConsistencyDummy checks that ValidateConsistency reports
// an isolated dummy node.
//
// django: tests/migrations/test_graph.py GraphTests.test_validate_consistency_dummy
func TestGraph_ValidateConsistencyDummy(t *testing.T) {
	const msg = "app_a.0001 (req'd by app_b.0002) is missing!"
	g := New()
	g.AddDummyNode(mk("app_a", "0001"), "app_b.0002", msg)

	assertNodeNotFound(t, g.ValidateConsistency(), msg)
}

// TestGraph_RemoveReplacedNodes checks that replaced nodes are removed and
// dependencies remapped.
//
// django: tests/migrations/test_graph.py GraphTests.test_remove_replaced_nodes
func TestGraph_RemoveReplacedNodes(t *testing.T) {
	// Add some dummy nodes to be replaced.
	g := New()
	g.AddDummyNode(mk("app_a", "0001"), "app_a.0002", "BAD!")
	g.AddDummyNode(mk("app_a", "0002"), "app_b.0001", "BAD!")
	skipDep(t, g, "app_a.0002", mk("app_a", "0002"), mk("app_a", "0001"))
	// Add some normal parent and child nodes to test dependency remapping.
	mustNodes(t, g, mk("app_c", "0001"), mk("app_b", "0001"))
	skipDep(t, g, "app_a.0001", mk("app_a", "0001"), mk("app_c", "0001"))
	skipDep(t, g, "app_b.0001", mk("app_b", "0001"), mk("app_a", "0002"))

	// Try replacing before the replacement node exists.
	replacement := mk("app_a", "0001_squashed_0002")
	replaced := keyList(mk("app_a", "0001"), mk("app_a", "0002"))
	err := g.RemoveReplacedNodes(replacement, replaced)
	assertNodeNotFound(t, err, "Unable to find replacement node ('app_a', '0001_squashed_0002'). "+
		"It was either never added to the migration graph, or has been removed.")

	g.AddNode(replacement, nil)
	// ValidateConsistency() still raises an error at this stage.
	assertNodeNotFound(t, g.ValidateConsistency(), "BAD!")

	// Remove the dummy nodes.
	if err := g.RemoveReplacedNodes(replacement, replaced); err != nil {
		t.Fatalf("RemoveReplacedNodes: %v", err)
	}
	// The graph is now consistent and dependencies have been remapped.
	if err := g.ValidateConsistency(); err != nil {
		t.Fatalf("ValidateConsistency after replacement: %v", err)
	}
	parentNode := g.NodeMap[mk("app_c", "0001")]
	replacementNode := g.NodeMap[replacement]
	childNode := g.NodeMap[mk("app_b", "0001")]
	if parentNode == nil || replacementNode == nil || childNode == nil {
		t.Fatal("expected app_c.0001, the replacement and app_b.0001 to be in the node map")
	}
	assertEdge(t, "parent in replacement.Parents", replacementNode.Parents, parentNode, true)
	assertEdge(t, "replacement in parent.Children", parentNode.Children, replacementNode, true)
	assertEdge(t, "child in replacement.Children", replacementNode.Children, childNode, true)
	assertEdge(t, "replacement in child.Parents", childNode.Parents, replacementNode, true)
}

// TestGraph_RemoveReplacementNode checks that a replacement node is removed
// and child dependencies remapped; parent dependencies are assumed correct.
//
// django: tests/migrations/test_graph.py GraphTests.test_remove_replacement_node
func TestGraph_RemoveReplacementNode(t *testing.T) {
	g := New()
	mustNodes(t, g, mk("app_a", "0001"), mk("app_a", "0002"))
	mustDep(t, g, "app_a.0002", mk("app_a", "0002"), mk("app_a", "0001"))

	replacement := mk("app_a", "0001_squashed_0002")
	replaced := keyList(mk("app_a", "0001"), mk("app_a", "0002"))
	// Try removing the replacement node before it exists.
	err := g.RemoveReplacementNode(replacement, replaced)
	assertNodeNotFound(t, err, "Unable to remove replacement node ('app_a', '0001_squashed_0002'). "+
		"It was either never added to the migration graph, or has been removed already.")

	g.AddNode(replacement, nil)
	// Add a child node to test dependency remapping.
	mustNodes(t, g, mk("app_b", "0001"))
	mustDep(t, g, "app_b.0001", mk("app_b", "0001"), replacement)

	if err := g.RemoveReplacementNode(replacement, replaced); err != nil {
		t.Fatalf("RemoveReplacementNode: %v", err)
	}
	if err := g.ValidateConsistency(); err != nil {
		t.Fatalf("ValidateConsistency after removal: %v", err)
	}
	replacedNode := g.NodeMap[mk("app_a", "0002")]
	childNode := g.NodeMap[mk("app_b", "0001")]
	otherReplacedNode := g.NodeMap[mk("app_a", "0001")]
	if replacedNode == nil || childNode == nil || otherReplacedNode == nil {
		t.Fatal("expected app_a.0001, app_a.0002 and app_b.0001 to be in the node map")
	}
	assertEdge(t, "child in replaced.Children", replacedNode.Children, childNode, true)
	assertEdge(t, "replaced in child.Parents", childNode.Parents, replacedNode, true)
	// The child dependency hasn't also gotten remapped to the other replaced node.
	assertEdge(t, "child in other_replaced.Children", otherReplacedNode.Children, childNode, false)
	assertEdge(t, "other_replaced in child.Parents", childNode.Parents, otherReplacedNode, false)
}

func assertEdge(t *testing.T, what string, set map[*Node]struct{}, node *Node, want bool) {
	t.Helper()
	_, got := set[node]
	if got != want {
		t.Fatalf("%s: got %v, want %v", what, got, want)
	}
}

// TestGraph_InfiniteLoop tests a complex dependency graph with squashing
// applied on app_c, which makes the graph cyclic:
//
//	app_a:        0001 <-
//	                     \
//	app_b:        0001 <- x 0002 <-
//	               /               \
//	app_c:   0001<-  <------------- x 0002
//
// django: tests/migrations/test_graph.py GraphTests.test_infinite_loop
func TestGraph_InfiniteLoop(t *testing.T) {
	g := New()
	mustNodes(t, g, mk("app_a", "0001"), mk("app_b", "0001"), mk("app_b", "0002"),
		mk("app_c", "0001_squashed_0002"))
	mustDep(t, g, "app_b.0001", mk("app_b", "0001"), mk("app_c", "0001_squashed_0002"))
	mustDep(t, g, "app_b.0002", mk("app_b", "0002"), mk("app_a", "0001"))
	mustDep(t, g, "app_b.0002", mk("app_b", "0002"), mk("app_b", "0001"))
	mustDep(t, g, "app_c.0001_squashed_0002", mk("app_c", "0001_squashed_0002"), mk("app_b", "0002"))

	err := g.EnsureNotCyclic()
	var cd *m.CircularDependencyError
	if !errors.As(err, &cd) {
		t.Fatalf("EnsureNotCyclic: expected CircularDependencyError, got %v", err)
	}
}

// TestGraph_Stringify ports Django's str(graph) assertions. Django's
// repr(graph) ("<MigrationGraph: nodes=5, edges=3>") has no Go equivalent:
// gormgate's Graph only implements String().
//
// django: tests/migrations/test_graph.py GraphTests.test_stringify
func TestGraph_Stringify(t *testing.T) {
	g := New()
	if got, want := g.String(), "Graph: 0 nodes, 0 edges"; got != want {
		t.Fatalf("String(): got %q, want %q", got, want)
	}

	mustNodes(t, g, mk("app_a", "0001"), mk("app_a", "0002"), mk("app_a", "0003"),
		mk("app_b", "0001"), mk("app_b", "0002"))
	mustDep(t, g, "app_a.0002", mk("app_a", "0002"), mk("app_a", "0001"))
	mustDep(t, g, "app_a.0003", mk("app_a", "0003"), mk("app_a", "0002"))
	mustDep(t, g, "app_a.0003", mk("app_a", "0003"), mk("app_b", "0002"))

	if got, want := g.String(), "Graph: 5 nodes, 3 edges"; got != want {
		t.Fatalf("String(): got %q, want %q", got, want)
	}
}

// TestGraph_NodeStr ports Django's str(Node) == "('app_a', '0001')". gormgate
// renders node keys with FormatKey, which is what the error messages use.
//
// django: tests/migrations/test_graph.py NodeTests.test_node_str
func TestGraph_NodeStr(t *testing.T) {
	g := New()
	mustNodes(t, g, mk("app_a", "0001"))
	node := g.NodeMap[mk("app_a", "0001")]
	if got, want := FormatKey(node.Key), "('app_a', '0001')"; got != want {
		t.Fatalf("FormatKey(node.Key): got %q, want %q", got, want)
	}
}

// TestGraph_MakeStateEmpty checks make_state() on an empty graph: Django
// returns an empty ProjectState when there are no nodes.
//
// django: db/migrations/graph.py MigrationGraph.make_state
func TestGraph_MakeStateEmpty(t *testing.T) {
	g := New()
	state, err := g.MakeState(nil, true, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Models) != 0 {
		t.Fatalf("MakeState on an empty graph: got %d models, want 0", len(state.Models))
	}
}

// TestGraph_MakeState checks that make_state() applies the migrations of the
// forwards plan, and that at_end=false stops before the target nodes.
//
// django: db/migrations/graph.py MigrationGraph.make_state / _generate_plan
func TestGraph_MakeState(t *testing.T) {
	first := &m.Migration{App: "app_a", Name: "0001", Operations: []m.Operation{
		&m.CreateModel{Name: "Author", Table: "app_a_author", Fields: m.Fields{
			{Name: "id", Field: m.Field{Type: m.Int, Size: 64, PrimaryKey: true, AutoIncrement: true}},
		}},
	}}
	second := &m.Migration{App: "app_a", Name: "0002",
		Dependencies: []m.Key{mk("app_a", "0001")},
		Operations: []m.Operation{
			&m.CreateModel{Name: "Book", Table: "app_a_book", Fields: m.Fields{
				{Name: "id", Field: m.Field{Type: m.Int, Size: 64, PrimaryKey: true, AutoIncrement: true}},
			}},
		}}
	g := New()
	g.AddNode(first.Key(), first)
	g.AddNode(second.Key(), second)
	mustDep(t, g, second.String(), second.Key(), first.Key())

	atEnd, err := g.MakeState(nil, true, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(atEnd.Models) != 2 {
		t.Fatalf("make_state(at_end=True): got %d models, want 2", len(atEnd.Models))
	}
	before, err := g.MakeState([]m.Key{second.Key()}, false, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Models) != 1 {
		t.Fatalf("make_state(at_end=False): got %d models, want 1", len(before.Models))
	}
	if _, err := before.Model("app_a", "Author"); err != nil {
		t.Fatalf("make_state(at_end=False): expected app_a.Author, got %v", err)
	}
}

// TestMakeStateWithDummyNode checks that a graph holding a dummy node — one
// the graph knows is referred to but has no migration for — reports an error
// instead of dereferencing the missing migration. Every in-tree caller runs
// after ValidateConsistency, but Graph is exported and can be built by hand.
func TestMakeStateWithDummyNode(t *testing.T) {
	g := New()
	g.AddDummyNode(m.Key{App: "app", Name: "0001_initial"}, "app.0002_later", "missing")
	st, err := g.MakeState([]m.Key{{App: "app", Name: "0001_initial"}}, true, nil, nil)
	if err == nil {
		t.Fatalf("expected an error, got state %v", st)
	}
	var nf *m.NodeNotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("got %T (%v), want *migrations.NodeNotFoundError", err, err)
	}
}
