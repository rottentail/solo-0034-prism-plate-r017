// Package validate owns structural admission and the shared compilation budget.
package validate

import (
	"fmt"
	"math"
	"prismplate/internal/geom"
	"prismplate/internal/scene"
	"regexp"
)

var identifier = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]{0,63}$`)

func ID(s string) bool         { return identifier.MatchString(s) }
func finite(x float64) bool    { return !math.IsNaN(x) && !math.IsInf(x, 0) && math.Abs(x) <= 1e6 }
func Unit(x float64) bool      { return finite(x) && x >= 0 && x <= 1 }
func Color(c scene.Color) bool { return Unit(c.R) && Unit(c.G) && Unit(c.B) && Unit(c.A) }
func path(ops []geom.Op) (geom.Path, error) {
	if len(ops) == 0 || len(ops) > 256 {
		return nil, fmt.Errorf("contour operation count must be 1..256")
	}
	open := false
	vertices := 0
	for _, o := range ops {
		n, ok := map[string]int{"M": 1, "L": 1, "Q": 2, "C": 3, "Z": 0}[o.Kind]
		if !ok || len(o.Points) != n {
			return nil, fmt.Errorf("invalid contour verb or arity")
		}
		for _, p := range o.Points {
			if !finite(p.X) || !finite(p.Y) {
				return nil, fmt.Errorf("non-finite contour")
			}
		}
		if o.Kind == "M" {
			if open {
				return nil, fmt.Errorf("close each contour before another M")
			}
			open = true
			vertices = 1
		} else {
			if !open {
				return nil, fmt.Errorf("contour must begin with M")
			}
			if o.Kind == "Z" {
				if vertices < 3 {
					return nil, fmt.Errorf("contour requires three vertices")
				}
				open = false
			} else {
				vertices++
			}
		}
	}
	if open {
		return nil, fmt.Errorf("contour must terminate with Z")
	}
	return geom.Flatten(ops), nil
}
func brush(b scene.Brush) error {
	if b.Kind == "solid" {
		if !Color(b.Color) {
			return fmt.Errorf("invalid solid color")
		}
		return nil
	}
	if b.Kind != "linear" {
		return fmt.Errorf("unknown brush")
	}
	for _, v := range []float64{b.From.X, b.From.Y, b.To.X, b.To.Y} {
		if !finite(v) {
			return fmt.Errorf("invalid gradient axis")
		}
	}
	if math.Hypot(b.To.X-b.From.X, b.To.Y-b.From.Y) < 1e-6 {
		return fmt.Errorf("degenerate gradient axis")
	}
	if len(b.Stops) < 2 || len(b.Stops) > 16 {
		return fmt.Errorf("gradient requires 2..16 stops")
	}
	prev := -1.0
	for _, s := range b.Stops {
		if !Unit(s.At) || s.At <= prev || !Color(s.Color) {
			return fmt.Errorf("invalid gradient stops")
		}
		prev = s.At
	}
	if b.Stops[0].At != 0 || prev != 1 {
		return fmt.Errorf("gradient endpoints must be 0 and 1")
	}
	return nil
}

// budget accumulates every expanded node so a scene cannot multiply shapes
// without bound. Counts stop growing once a limit is crossed.
type budget struct {
	shapes    int
	uses      int
	depth     int
	documents map[string]bool
	segments  int64
	serial    int
}

func (b *budget) id(prefix string) string {
	b.serial++
	return fmt.Sprintf("%s%d", prefix, b.serial)
}

// Compile admits a document that declares no component references.
func Compile(d scene.Document) (scene.Compiled, error) {
	return CompileWithStore(d, nil)
}

// CompileWithStore admits a document and resolves its frozen component closure
// from store. Only documents keyed by their own content digest are accepted, so
// rendering never depends on a source plate retaining its name or revision.
func CompileWithStore(d scene.Document, store map[string]scene.Document) (scene.Compiled, error) {
	b := &budget{documents: map[string]bool{}}
	layers, err := compileDocument(d, store, b, 1)
	if err != nil {
		return scene.Compiled{}, err
	}
	// Four samples per pixel; every fill is bounded even under adversarial input.
	if b.segments*int64(d.Width)*int64(d.Height)*4 > 160_000_000 {
		return scene.Compiled{}, fmt.Errorf("scene exceeds bounded rendering work")
	}
	return scene.Compiled{Document: d, Layers: layers, Segments: int(b.segments)}, nil
}

func compileDocument(d scene.Document, store map[string]scene.Document, b *budget, depth int) ([]scene.CompiledLayer, error) {
	if depth > scene.MaxComponentDepth {
		return nil, fmt.Errorf("component nesting exceeds %d levels", scene.MaxComponentDepth)
	}
	if d.Width < 1 || d.Height < 1 || d.Width > 512 || d.Height > 512 || !Color(d.Background) {
		return nil, fmt.Errorf("invalid canvas (maximum 512 square)")
	}
	if len(d.Layers) < 1 || len(d.Layers) > 16 {
		return nil, fmt.Errorf("requires 1..16 layers")
	}
	ids := map[string]bool{}
	add := func(id string) error {
		if !ID(id) || ids[id] {
			return fmt.Errorf("invalid or duplicate node id %q", id)
		}
		ids[id] = true
		return nil
	}
	// Shape ids only need to be unique within their own document; ids inside a
	// mounted child live in the child's local namespace.
	var shapeIDs map[string]bool
	addShape := func(id string) error {
		if shapeIDs == nil {
			shapeIDs = map[string]bool{}
		}
		if !ID(id) || shapeIDs[id] {
			return fmt.Errorf("invalid or duplicate shape id %q", id)
		}
		shapeIDs[id] = true
		return nil
	}
	count := func(p geom.Path) {
		for _, r := range p {
			b.segments += int64(len(r))
		}
	}
	layers := make([]scene.CompiledLayer, 0, len(d.Layers))
	for _, l := range d.Layers {
		if err := add(l.ID); err != nil {
			return nil, err
		}
		if !Unit(l.Opacity) || (l.Blend != "normal" && l.Blend != "multiply" && l.Blend != "screen") {
			return nil, fmt.Errorf("invalid layer compositing")
		}
		cl := scene.CompiledLayer{Layer: l, ClipID: b.id("c"), MaskID: b.id("m")}
		if len(l.Clip) > 0 {
			p, e := path(l.Clip)
			if e != nil {
				return nil, e
			}
			cl.Clip = p
			count(p)
		}
		if len(l.Mask) > 0 {
			p, e := path(l.Mask)
			if e != nil {
				return nil, e
			}
			cl.Mask = p
			count(p)
		}
		if l.Component != nil {
			cc, err := compileComponent(l, store, b, depth)
			if err != nil {
				return nil, err
			}
			cl.Component = cc
		} else {
			if len(l.Shapes) < 1 || len(l.Shapes) > 32 {
				return nil, fmt.Errorf("requires 1..32 shapes per layer")
			}
			for _, s := range l.Shapes {
				if err := addShape(s.ID); err != nil {
					return nil, err
				}
				if s.FillRule != "nonzero" && s.FillRule != "evenodd" {
					return nil, fmt.Errorf("invalid fill rule")
				}
				for _, v := range s.Transform {
					if !finite(v) {
						return nil, fmt.Errorf("invalid transform")
					}
				}
				inv, ok := s.Transform.Inverse()
				if !ok {
					return nil, fmt.Errorf("singular transform")
				}
				for _, v := range inv {
					if !finite(v) {
						return nil, fmt.Errorf("ill-conditioned transform")
					}
				}
				p, e := path(s.Path)
				if e != nil {
					return nil, e
				}
				if e = brush(s.Brush); e != nil {
					return nil, e
				}
				count(p)
				b.shapes++
				if b.shapes > scene.MaxExpandedShapes {
					return nil, fmt.Errorf("expanded component graph exceeds %d shapes", scene.MaxExpandedShapes)
				}
				cl.Shapes = append(cl.Shapes, scene.CompiledShape{Shape: s, Path: p, Inverse: inv, GradientID: b.id("b")})
			}
		}
		layers = append(layers, cl)
	}
	return layers, nil
}

// compileComponent admits one mounted instance of a frozen document. Its
// transform stays in the parent coordinate space; child clips, masks, gradient
// axes and shape transforms keep their local meaning under mirroring/scaling.
func compileComponent(l scene.Layer, store map[string]scene.Document, b *budget, depth int) (*scene.CompiledComponent, error) {
	ref := l.Component
	if len(l.Shapes) != 0 {
		return nil, fmt.Errorf("component layer must not declare shapes")
	}
	if ref.Digest == "" {
		return nil, fmt.Errorf("component reference requires a frozen digest")
	}
	for _, v := range ref.Transform {
		if !finite(v) {
			return nil, fmt.Errorf("invalid component transform")
		}
	}
	inv, ok := ref.Transform.Inverse()
	if !ok {
		return nil, fmt.Errorf("singular component transform")
	}
	for _, v := range inv {
		if !finite(v) {
			return nil, fmt.Errorf("ill-conditioned component transform")
		}
	}
	if depth > scene.MaxComponentDepth {
		return nil, fmt.Errorf("component nesting exceeds %d levels", scene.MaxComponentDepth)
	}
	child, ok := store[ref.Digest]
	if !ok {
		return nil, fmt.Errorf("missing frozen component %s", ref.Digest)
	}
	if scene.Hash(child) != ref.Digest {
		return nil, fmt.Errorf("component digest mismatch for %s", ref.Digest)
	}
	b.uses++
	if b.uses > scene.MaxComponentUses {
		return nil, fmt.Errorf("component instance count exceeds %d", scene.MaxComponentUses)
	}
	if !b.documents[ref.Digest] {
		b.documents[ref.Digest] = true
		if len(b.documents) > scene.MaxClosureDocuments {
			return nil, fmt.Errorf("component closure exceeds %d documents", scene.MaxClosureDocuments)
		}
	}
	layers, err := compileDocument(child, store, b, depth+1)
	if err != nil {
		return nil, err
	}
	return &scene.CompiledComponent{Transform: ref.Transform, Inverse: inv, Layers: layers}, nil
}
