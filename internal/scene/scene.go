// Package scene defines the portable prism-plate-v1 document contract.
package scene

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	"prismplate/internal/geom"
)

// Expansion limits are shared by admission, bundles and recovery so a crafted
// snapshot cannot create unbounded work at compile time.
const (
	MaxComponentDepth   = 8
	MaxComponentUses    = 64
	MaxExpandedShapes   = 1024
	MaxClosureDocuments = 128
)

type Color struct {
	R float64 `json:"r"`
	G float64 `json:"g"`
	B float64 `json:"b"`
	A float64 `json:"a"`
}
type Stop struct {
	At    float64 `json:"at"`
	Color Color   `json:"color"`
}
type Brush struct {
	Kind  string     `json:"kind"`
	Color Color      `json:"color"`
	From  geom.Point `json:"from"`
	To    geom.Point `json:"to"`
	Stops []Stop     `json:"stops,omitempty"`
}

// ComponentRef mounts one frozen revision of another plate. Plate and Revision
// are provenance metadata captured at freeze time; Digest is authoritative and
// is the only field consulted while compiling or rendering.
type ComponentRef struct {
	Plate     string      `json:"plate,omitempty"`
	Revision  int         `json:"revision,omitempty"`
	Digest    string      `json:"digest"`
	Transform geom.Matrix `json:"transform"`
}

type Shape struct {
	ID        string      `json:"id"`
	Path      []geom.Op   `json:"path"`
	FillRule  string      `json:"fill_rule"`
	Brush     Brush       `json:"brush"`
	Transform geom.Matrix `json:"transform"`
}
type Layer struct {
	ID        string        `json:"id"`
	Opacity   float64       `json:"opacity"`
	Blend     string        `json:"blend"`
	Clip      []geom.Op     `json:"clip,omitempty"`
	Mask      []geom.Op     `json:"mask,omitempty"`
	Shapes    []Shape       `json:"shapes"`
	Component *ComponentRef `json:"component,omitempty"`
}
type Document struct {
	Width      int     `json:"width"`
	Height     int     `json:"height"`
	Background Color   `json:"background"`
	Layers     []Layer `json:"layers"`
}
type CompiledShape struct {
	Shape      Shape
	Path       geom.Path
	Inverse    geom.Matrix
	GradientID string
}
type CompiledComponent struct {
	Transform geom.Matrix
	Inverse   geom.Matrix
	Layers    []CompiledLayer
}
type CompiledLayer struct {
	Layer  Layer
	Clip   geom.Path
	Mask   geom.Path
	ClipID string
	MaskID string
	Shapes []CompiledShape
	// Component is the expanded frozen document mounted by this layer.
	Component *CompiledComponent
}
type Compiled struct {
	Document Document
	Layers   []CompiledLayer
	Segments int
}

// Hash is the canonical content address of a frozen document. Documents only
// contain ordered structs and slices, so json.Marshal is deterministic.
func Hash(d Document) string {
	b, err := json.Marshal(d)
	if err != nil {
		return ""
	}
	v := sha256.Sum256(b)
	return hex.EncodeToString(v[:])
}

// Ref returns the component digest mounted by a layer, if any.
func (l Layer) Ref() (string, bool) {
	if l.Component == nil {
		return "", false
	}
	return l.Component.Digest, true
}

// Closure returns the sorted set of component digests reachable from d through
// store. The root digest is never included. It rejects missing documents,
// digest-key mismatches, reference cycles (including a plate citing its own
// digest) and nesting deeper than the shared limit.
func Closure(d Document, store map[string]Document) ([]string, error) {
	seen := map[string]bool{}
	state := map[string]int{} // 0 unseen, 1 on stack, 2 done
	state[Hash(d)] = 1
	var visit func(doc Document, level int) error
	visit = func(doc Document, level int) error {
		for _, l := range doc.Layers {
			ref := l.Component
			if ref == nil {
				continue
			}
			dg := ref.Digest
			if dg == "" {
				return fmt.Errorf("component reference requires a digest")
			}
			if state[dg] == 1 {
				return fmt.Errorf("component reference cycle at %s", dg)
			}
			child, ok := store[dg]
			if !ok {
				return fmt.Errorf("missing component %s", dg)
			}
			if Hash(child) != dg {
				return fmt.Errorf("component digest mismatch for %s", dg)
			}
			seen[dg] = true
			if state[dg] == 2 {
				continue
			}
			if level >= MaxComponentDepth {
				return fmt.Errorf("component nesting exceeds %d levels", MaxComponentDepth)
			}
			state[dg] = 1
			if err := visit(child, level+1); err != nil {
				return err
			}
			state[dg] = 2
		}
		return nil
	}
	if err := visit(d, 1); err != nil {
		return nil, err
	}
	if len(seen) > MaxClosureDocuments {
		return nil, fmt.Errorf("component closure exceeds %d documents", MaxClosureDocuments)
	}
	out := make([]string, 0, len(seen))
	for dg := range seen {
		out = append(out, dg)
	}
	sort.Strings(out)
	return out, nil
}
