// Package graph holds the dependency graph of a project's migrations and
// turns it into the plans the executor runs: which migrations to apply, in
// what order, to reach a given target.
//
// django: db/migrations/graph.py
package graph

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strings"

	m "github.com/ctolon/gormgate/migrations"
)

// Node is one migration in the graph. Children and Parents are sets, so
// every traversal sorts them to keep the resulting plan deterministic.
type Node struct {
	// Key identifies the migration.
	Key m.Key
	// Children are the nodes that depend on this one.
	Children map[*Node]struct{}
	// Parents are the nodes this one depends on.
	Parents map[*Node]struct{}

	dummy        bool
	origin       string
	errorMessage string
}

func newNode(k m.Key) *Node {
	return &Node{Key: k, Children: map[*Node]struct{}{}, Parents: map[*Node]struct{}{}}
}

// CompareKeys orders migration keys by app label, then by migration name.
// It is the comparator behind SortKeys, for callers sorting a sequence of
// keys of their own.
func CompareKeys(a, b m.Key) int {
	return cmp.Or(cmp.Compare(a.App, b.App), cmp.Compare(a.Name, b.Name))
}

// SortKeys sorts keys by app label, then by migration name.
func SortKeys(keys []m.Key) { slices.SortFunc(keys, CompareKeys) }

func sortedNodes(set map[*Node]struct{}) []*Node {
	out := slices.Collect(maps.Keys(set))
	slices.SortFunc(out, func(a, b *Node) int { return CompareKeys(a.Key, b.Key) })
	return out
}

// FormatKey renders a migration key as the quoted pair Django's error
// messages use, for example ('blog', '0001_initial').
func FormatKey(k m.Key) string { return fmt.Sprintf("('%s', '%s')", k.App, k.Name) }

// Graph is the directed graph of a project's migrations.
type Graph struct {
	// NodeMap holds every node, including the dummy nodes that stand in
	// for missing dependencies.
	NodeMap map[m.Key]*Node
	// Nodes holds the migration behind each node. A key mapped to nil is
	// a dummy node: the graph knows it is referred to but has no
	// migration for it.
	Nodes map[m.Key]*m.Migration
}

// New returns an empty graph.
func New() *Graph {
	return &Graph{NodeMap: map[m.Key]*Node{}, Nodes: map[m.Key]*m.Migration{}}
}

// AddNode adds a migration to the graph. Keys are unique by construction,
// so adding one twice is a programming error and panics.
func (g *Graph) AddNode(k m.Key, mig *m.Migration) {
	if _, ok := g.NodeMap[k]; ok {
		panic("graph: duplicate node " + k.String())
	}
	g.NodeMap[k] = newNode(k)
	g.Nodes[k] = mig
}

// AddDummyNode adds a placeholder for a dependency no migration provides.
// ValidateConsistency turns it into errorMessage, attributed to origin.
func (g *Graph) AddDummyNode(k m.Key, origin, errorMessage string) {
	n := newNode(k)
	n.dummy, n.origin, n.errorMessage = true, origin, errorMessage
	g.NodeMap[k] = n
	g.Nodes[k] = nil
}

// AddDependency adds an edge parent -> child; missing nodes become dummy
// nodes.
//
// django: graph.py MigrationGraph.add_dependency
func (g *Graph) AddDependency(migration string, child, parent m.Key, skipValidation bool) error {
	if _, ok := g.Nodes[child]; !ok {
		g.AddDummyNode(child, migration, fmt.Sprintf("Migration %s dependencies reference nonexistent child node %s", migration, FormatKey(child)))
	}
	if _, ok := g.Nodes[parent]; !ok {
		g.AddDummyNode(parent, migration, fmt.Sprintf("Migration %s dependencies reference nonexistent parent node %s", migration, FormatKey(parent)))
	}
	c, p := g.NodeMap[child], g.NodeMap[parent]
	c.Parents[p] = struct{}{}
	p.Children[c] = struct{}{}
	if !skipValidation {
		return g.ValidateConsistency()
	}
	return nil
}

// RemoveReplacedNodes removes replaced nodes and repoints their
// dependencies to the replacement.
//
// django: graph.py MigrationGraph.remove_replaced_nodes
func (g *Graph) RemoveReplacedNodes(replacement m.Key, replaced []m.Key) error {
	replacedSet := map[m.Key]bool{}
	for _, k := range replaced {
		replacedSet[k] = true
	}
	rn, ok := g.NodeMap[replacement]
	if !ok {
		return &m.NodeNotFoundError{Message: fmt.Sprintf("Unable to find replacement node %s. It was either never added to the migration graph, or has been removed.", FormatKey(replacement)), Node: replacement}
	}
	for _, rk := range slices.SortedFunc(maps.Keys(replacedSet), CompareKeys) {
		delete(g.Nodes, rk)
		node, ok := g.NodeMap[rk]
		if !ok {
			continue
		}
		delete(g.NodeMap, rk)
		for child := range node.Children {
			delete(child.Parents, node)
			if !replacedSet[child.Key] {
				rn.Children[child] = struct{}{}
				child.Parents[rn] = struct{}{}
			}
		}
		for parent := range node.Parents {
			delete(parent.Children, node)
			if !replacedSet[parent.Key] {
				rn.Parents[parent] = struct{}{}
				parent.Children[rn] = struct{}{}
			}
		}
	}
	return nil
}

// RemoveReplacementNode removes the replacement node and remaps its children
// to the replaced nodes.
//
// django: graph.py MigrationGraph.remove_replacement_node
func (g *Graph) RemoveReplacementNode(replacement m.Key, replaced []m.Key) error {
	delete(g.Nodes, replacement)
	rn, ok := g.NodeMap[replacement]
	if !ok {
		return &m.NodeNotFoundError{Message: fmt.Sprintf("Unable to remove replacement node %s. It was either never added to the migration graph, or has been removed already.", FormatKey(replacement)), Node: replacement}
	}
	delete(g.NodeMap, replacement)
	replacedNodes := map[*Node]struct{}{}
	replacedParents := map[*Node]struct{}{}
	for _, k := range replaced {
		if n, ok := g.NodeMap[k]; ok {
			replacedNodes[n] = struct{}{}
			for p := range n.Parents {
				replacedParents[p] = struct{}{}
			}
		}
	}
	for p := range replacedParents {
		delete(replacedNodes, p)
	}
	for child := range rn.Children {
		delete(child.Parents, rn)
		for n := range replacedNodes {
			n.Children[child] = struct{}{}
			child.Parents[n] = struct{}{}
		}
	}
	for parent := range rn.Parents {
		delete(parent.Children, rn)
	}
	return nil
}

// ValidateConsistency ensures there are no dummy nodes remaining.
//
// django: graph.py MigrationGraph.validate_consistency
func (g *Graph) ValidateConsistency() error {
	for _, k := range slices.SortedFunc(maps.Keys(g.NodeMap), CompareKeys) {
		n := g.NodeMap[k]
		if n.dummy {
			return &m.NodeNotFoundError{Message: n.errorMessage, Node: n.Key, Origin: n.origin}
		}
	}
	return nil
}

// Has reports whether k is a migration node.
func (g *Graph) Has(k m.Key) bool {
	_, ok := g.Nodes[k]
	return ok
}

// ForwardsPlan lists the nodes to apply to reach target, ending with it.
//
// django: graph.py MigrationGraph.forwards_plan
func (g *Graph) ForwardsPlan(target m.Key) ([]m.Key, error) {
	if !g.Has(target) {
		return nil, &m.NodeNotFoundError{Message: fmt.Sprintf("Node %s not a valid node", FormatKey(target)), Node: target}
	}
	return g.iterativeDFS(g.NodeMap[target], true), nil
}

// BackwardsPlan lists the nodes to unapply to remove target, ending with it.
//
// django: graph.py MigrationGraph.backwards_plan
func (g *Graph) BackwardsPlan(target m.Key) ([]m.Key, error) {
	if !g.Has(target) {
		return nil, &m.NodeNotFoundError{Message: fmt.Sprintf("Node %s not a valid node", FormatKey(target)), Node: target}
	}
	return g.iterativeDFS(g.NodeMap[target], false), nil
}

// django: graph.py MigrationGraph.iterative_dfs
func (g *Graph) iterativeDFS(start *Node, forwards bool) []m.Key {
	type item struct {
		n         *Node
		processed bool
	}
	var visited []m.Key
	seen := map[*Node]bool{}
	stack := []item{{start, false}}
	for len(stack) > 0 {
		it := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		switch {
		case seen[it.n]:
		case it.processed:
			seen[it.n] = true
			visited = append(visited, it.n.Key)
		default:
			stack = append(stack, item{it.n, true})
			next := it.n.Parents
			if !forwards {
				next = it.n.Children
			}
			for _, n := range sortedNodes(next) {
				stack = append(stack, item{n, false})
			}
		}
	}
	return visited
}

// RootNodes returns nodes with no dependencies inside their app.
//
// django: graph.py MigrationGraph.root_nodes
func (g *Graph) RootNodes(app string) []m.Key {
	var roots []m.Key
	for k := range g.Nodes {
		if app != "" && app != k.App {
			continue
		}
		root := true
		for p := range g.NodeMap[k].Parents {
			if p.Key.App == k.App {
				root = false
				break
			}
		}
		if root {
			roots = append(roots, k)
		}
	}
	SortKeys(roots)
	return roots
}

// LeafNodes returns nodes with no dependents inside their app.
//
// django: graph.py MigrationGraph.leaf_nodes
func (g *Graph) LeafNodes(app string) []m.Key {
	var leaves []m.Key
	for k := range g.Nodes {
		if app != "" && app != k.App {
			continue
		}
		leaf := true
		for c := range g.NodeMap[k].Children {
			if c.Key.App == k.App {
				leaf = false
				break
			}
		}
		if leaf {
			leaves = append(leaves, k)
		}
	}
	SortKeys(leaves)
	return leaves
}

// EnsureNotCyclic returns a *migrations.CircularDependencyError when the
// graph contains a cycle, and nil otherwise.
//
// django: graph.py MigrationGraph.ensure_not_cyclic
func (g *Graph) EnsureNotCyclic() error {
	todo := map[m.Key]bool{}
	for k := range g.Nodes {
		todo[k] = true
	}
	for _, start := range slices.SortedFunc(maps.Keys(g.Nodes), CompareKeys) {
		if !todo[start] {
			continue
		}
		delete(todo, start)
		stack := []m.Key{start}
		for len(stack) > 0 {
			top := stack[len(stack)-1]
			pushed := false
			for _, child := range sortedNodes(g.NodeMap[top].Children) {
				ck := child.Key
				for i, s := range stack {
					if s == ck {
						var parts []string
						for _, c := range stack[i:] {
							parts = append(parts, c.App+"."+c.Name)
						}
						return &m.CircularDependencyError{Msg: strings.Join(parts, ", ")}
					}
				}
				if todo[ck] {
					stack = append(stack, ck)
					delete(todo, ck)
					pushed = true
					break
				}
			}
			if !pushed {
				stack = stack[:len(stack)-1]
			}
		}
	}
	return nil
}

// String summarizes the graph's size.
func (g *Graph) String() string {
	edges := 0
	for _, n := range g.NodeMap {
		edges += len(n.Parents)
	}
	return fmt.Sprintf("Graph: %d nodes, %d edges", len(g.Nodes), edges)
}

// generatePlan returns the migrations needed to reach nodes, in dependency
// order. With atEnd set the plan includes everything the targets depend on;
// otherwise it stops short of the targets themselves.
//
// django: graph.py MigrationGraph._generate_plan
func (g *Graph) generatePlan(nodes []m.Key, atEnd bool) ([]m.Key, error) {
	var plan []m.Key
	inPlan := map[m.Key]bool{}
	isTarget := map[m.Key]bool{}
	for _, n := range nodes {
		isTarget[n] = true
	}
	for _, n := range nodes {
		fp, err := g.ForwardsPlan(n)
		if err != nil {
			return nil, err
		}
		for _, k := range fp {
			if !inPlan[k] && (atEnd || !isTarget[k]) {
				plan = append(plan, k)
				inPlan[k] = true
			}
		}
	}
	return plan, nil
}

// MakeState returns the ProjectState after (atEnd) or before the given
// nodes; nil nodes means all leaf nodes.
//
// django: graph.py MigrationGraph.make_state
func (g *Graph) MakeState(nodes []m.Key, atEnd bool, realApps map[string]bool, realModels []*m.ModelState) (*m.ProjectState, error) {
	if nodes == nil {
		nodes = g.LeafNodes("")
	}
	if len(nodes) == 0 {
		return m.NewProjectState(), nil
	}
	ps := m.NewProjectState()
	if realApps != nil {
		ps.RealApps = realApps
	}
	ps.RealModels = realModels
	plan, err := g.generatePlan(nodes, atEnd)
	if err != nil {
		return nil, err
	}
	for _, k := range plan {
		// A key whose node is a dummy has no migration. Every in-tree
		// caller runs after ValidateConsistency, which rejects those,
		// but Graph is exported and can be built by hand.
		mig := g.Nodes[k]
		if mig == nil {
			return nil, &m.NodeNotFoundError{Message: fmt.Sprintf("no migration for node %s", FormatKey(k)), Node: k}
		}
		ps, err = mig.MutateState(ps, false)
		if err != nil {
			return nil, err
		}
	}
	return ps, nil
}
