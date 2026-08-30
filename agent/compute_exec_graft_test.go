package agent

import "testing"

// The exec graft is what turns a shallow compute node — one that wrote a script
// and said how to run it — into a node with an `output` field. Without it the
// code is written and never run, and the reflector concludes on metadata.
//
// The graft used to be guarded on `node.SpawnedBy == ""`. That reads as "the
// planner made this node", and on a first plan it is true. On a replan it is
// not: replan-grafted nodes carry the reflection that ordered them as their
// parent (scheduler.go, `nn.SpawnedBy = comp.NodeID`), so every compute node
// from round two onwards failed the guard and its script never ran. Observed on
// session 32bc4e76: three compute nodes, 72 seconds, no output between them.
//
// The one thing the guard has to keep out is an architect's coder children.
// Those get their run command from the blueprint graft's own phase, and a second
// one here would run the script twice. Their parent is the architect, which is a
// compute node — and that, not the presence of a parent, is what the guard now
// asks about.
func TestArchitectChildOnlyExcludesCoderChildren(t *testing.T) {
	g := NewGraph()

	architect := &Node{Type: NodeCompute, Tag: "architect"}
	architectID := g.AddNode(architect)
	reflection := &Node{Type: NodeReflection, Tag: "reflect"}
	reflectionID := g.AddNode(reflection)

	cases := []struct {
		name string
		node *Node
		want bool
	}{
		{
			name: "first plan — no parent at all",
			node: &Node{Type: NodeCompute, Tag: "parse", SpawnedBy: ""},
			want: false,
		},
		{
			name: "replan — the reflection that ordered it is the parent",
			node: &Node{Type: NodeCompute, Tag: "parse", SpawnedBy: reflectionID},
			want: false,
		},
		{
			name: "architect coder child — the parent is the architect",
			node: &Node{Type: NodeCompute, Tag: "task_0", SpawnedBy: architectID},
			want: true,
		},
		{
			name: "a parent that has left the graph",
			node: &Node{Type: NodeCompute, Tag: "parse", SpawnedBy: "n404"},
			want: false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g.AddNode(c.node)
			if got := architectChild(g, c.node); got != c.want {
				t.Errorf("architectChild = %v, want %v — %s", got, c.want, c.name)
			}
		})
	}
}
