package causal

import (
	"context"
	"errors"
	"fmt"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
)

// traverse.go is the deterministic, GATEWAY-FREE read half of the causal layer
// (RFC §5.6/§6b): "why did X lead to Y" as a graph walk over the led_to/caused_by
// edges, with provenance at every hop. It must NOT import internal/gateway (smoke +
// review gate); inference lives in infer.go.

// Direction selects which way the walk follows causality from the root.
type Direction string

const (
	// Backward walks to the CAUSES of the root ("why did this happen").
	Backward Direction = "backward"
	// Forward walks to the EFFECTS of the root ("what did this lead to").
	Forward Direction = "forward"
	// Both walks causes and effects.
	Both Direction = "both"
)

// maxDepth caps the traversal hops (resource guard; not a knob).
const maxDepth = 10

// maxNodes caps the total nodes a single traversal returns (resource guard).
const maxNodes = 200

// ProvRef is a compact provenance span for P1 drill-down at a node.
type ProvRef struct {
	RecordID  string `json:"record_id"`
	SpanStart int    `json:"span_start,omitempty"`
	SpanEnd   int    `json:"span_end,omitempty"`
}

// Node is one memory in the causal graph.
type Node struct {
	MemoryID   string    `json:"memory_id"`
	Kind       string    `json:"kind"`
	Content    string    `json:"content"`
	Context    string    `json:"context,omitempty"`
	EpisodeID  string    `json:"episode_id,omitempty"`
	Provenance []ProvRef `json:"provenance,omitempty"`
}

// Edge is a canonical cause→effect edge (From=cause, To=effect), regardless of
// whether the stored link was led_to or caused_by.
type Edge struct {
	From       string  `json:"from"`
	To         string  `json:"to"`
	Type       string  `json:"type"`
	Confidence float64 `json:"confidence"`
}

// Graph is the traversal result. Truncated is set when the depth/node cap was hit
// (no silent truncation — §11).
type Graph struct {
	Root      string `json:"root"`
	Nodes     []Node `json:"nodes"`
	Edges     []Edge `json:"edges"`
	Truncated bool   `json:"truncated,omitempty"`
}

// canonicalize maps a stored link to a canonical cause→effect edge. led_to(from→to)
// means from caused to. caused_by(from→to) means to caused from (from is "caused by"
// to), so the cause is `to`. Other link types are not causal and are skipped (ok=false).
func canonicalize(l store.Link) (cause, effect string, ok bool) {
	switch l.Type {
	case "led_to":
		return l.FromMemory, l.ToMemory, true
	case "caused_by":
		return l.ToMemory, l.FromMemory, true
	default:
		return "", "", false
	}
}

// Traverse walks the causal graph from startID. dir picks causes/effects/both; depth
// is clamped to [1, maxDepth] (≤0 ⇒ default handled by caller; here we clamp). Only
// ACTIVE memories are included; non-active endpoints are not traversed and their edges
// are omitted. Deterministic and gateway-free. A missing/non-active root ⇒ empty graph,
// no error (parity with memory_episodes get-missing).
func Traverse(ctx context.Context, st store.Store, scope identity.Scope, startID string, dir Direction, depth int) (Graph, error) {
	if startID == "" {
		return Graph{}, fmt.Errorf("causal: traverse: empty start id")
	}
	if dir == "" {
		dir = Backward
	}
	if dir != Backward && dir != Forward && dir != Both {
		return Graph{}, fmt.Errorf("causal: traverse: invalid direction %q", dir)
	}
	if depth <= 0 {
		depth = 3
	}
	g := Graph{Root: startID}
	if depth > maxDepth {
		depth = maxDepth
		g.Truncated = true // requested depth clamped to the hard cap — not silent (§11)
	}

	root, err := loadActive(ctx, st, scope, startID)
	if err != nil {
		return Graph{}, err
	}
	if root == nil {
		return g, nil // missing or non-active root ⇒ empty graph
	}

	visited := map[string]bool{startID: true}
	edgeSeen := map[[3]string]bool{}
	g.Nodes = append(g.Nodes, toNode(ctx, st, scope, *root))

	type qitem struct {
		id    string
		depth int
	}
	queue := []qitem{{startID, 0}}

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if cur.depth >= depth {
			// At the depth frontier. If this node still has an unvisited causal
			// neighbor, the graph extends past the requested depth — flag truncation
			// so the caller can distinguish "complete within N hops" from "more
			// exists beyond N" (§11, no silent truncation). The !Truncated guard
			// bounds the extra expand calls (stops probing once set).
			if !g.Truncated {
				if nbrs, eerr := expand(ctx, st, scope, cur.id, dir); eerr == nil {
					for _, nb := range nbrs {
						if !visited[nb.id] {
							g.Truncated = true
							break
						}
					}
				}
			}
			continue
		}
		neighbors, err := expand(ctx, st, scope, cur.id, dir)
		if err != nil {
			return Graph{}, err
		}
		for _, nb := range neighbors {
			// Edge dedup (from,to,type).
			ek := [3]string{nb.edge.From, nb.edge.To, nb.edge.Type}
			// Resolve the neighbor memory; skip non-active (drop node + edge).
			if !visited[nb.id] {
				mem, lerr := loadActive(ctx, st, scope, nb.id)
				if lerr != nil {
					return Graph{}, lerr
				}
				if mem == nil {
					continue // non-active or missing endpoint — don't traverse, omit edge
				}
				if len(g.Nodes) >= maxNodes {
					g.Truncated = true
					continue
				}
				visited[nb.id] = true
				g.Nodes = append(g.Nodes, toNode(ctx, st, scope, *mem))
				queue = append(queue, qitem{nb.id, cur.depth + 1})
			}
			if !edgeSeen[ek] {
				edgeSeen[ek] = true
				g.Edges = append(g.Edges, nb.edge)
			}
		}
	}
	return g, nil
}

type neighbor struct {
	id   string // the neighbor memory id
	edge Edge
}

// expand returns the causal neighbors of memID in the requested direction, with the
// canonical edge for each. It reads links in both stored orientations once.
func expand(ctx context.Context, st store.Store, scope identity.Scope, memID string, dir Direction) ([]neighbor, error) {
	fromLinks, err := st.Memories().ListLinks(ctx, scope, memID, "") // edges with from=memID
	if err != nil {
		return nil, fmt.Errorf("causal: traverse: list links from %s: %w", memID, err)
	}
	toLinks, err := st.Memories().ListLinks(ctx, scope, "", memID) // edges with to=memID
	if err != nil {
		return nil, fmt.Errorf("causal: traverse: list links to %s: %w", memID, err)
	}

	var out []neighbor
	add := func(l store.Link) {
		cause, effect, ok := canonicalize(l)
		if !ok {
			return
		}
		e := Edge{From: cause, To: effect, Type: l.Type, Confidence: l.Confidence}
		switch dir {
		case Backward:
			// causes of memID: memID is the effect, neighbor is the cause.
			if effect == memID && cause != memID {
				out = append(out, neighbor{id: cause, edge: e})
			}
		case Forward:
			// effects of memID: memID is the cause, neighbor is the effect.
			if cause == memID && effect != memID {
				out = append(out, neighbor{id: effect, edge: e})
			}
		case Both:
			if effect == memID && cause != memID {
				out = append(out, neighbor{id: cause, edge: e})
			}
			if cause == memID && effect != memID {
				out = append(out, neighbor{id: effect, edge: e})
			}
		}
	}
	for _, l := range fromLinks {
		add(l)
	}
	for _, l := range toLinks {
		add(l)
	}
	return out, nil
}

// loadActive returns the memory if it exists and is active, else nil (no error for
// not-found / non-active).
func loadActive(ctx context.Context, st store.Store, scope identity.Scope, id string) (*store.Memory, error) {
	mem, err := st.Memories().Get(ctx, scope, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("causal: traverse: get %s: %w", id, err)
	}
	if mem == nil || mem.Status != "active" {
		return nil, nil
	}
	return mem, nil
}

// toNode builds a graph node, attaching provenance (best-effort; provenance errors
// degrade to no-provenance rather than failing the traversal).
func toNode(ctx context.Context, st store.Store, scope identity.Scope, m store.Memory) Node {
	n := Node{MemoryID: m.ID, Kind: m.Kind, Content: m.Content, Context: m.Context, EpisodeID: m.EpisodeID}
	j, err := st.Memories().GetJunctions(ctx, scope, m.ID)
	if err == nil {
		for _, p := range j.Provenance {
			n.Provenance = append(n.Provenance, ProvRef{RecordID: p.RecordID, SpanStart: p.SpanStart, SpanEnd: p.SpanEnd})
		}
	}
	return n
}
