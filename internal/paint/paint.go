// Package paint implements linear-light, premultiplied-alpha compositing.
package paint

import (
	"math"
	"prismplate/internal/geom"
	"prismplate/internal/scene"
)

type Pixel struct{ R, G, B, A float64 }

func linear(v float64) float64 {
	if v <= 0.04045 {
		return v / 12.92
	}
	return math.Pow((v+0.055)/1.055, 2.4)
}
func srgb(v float64) float64 {
	v = math.Max(0, math.Min(1, v))
	if v <= 0.0031308 {
		return v * 12.92
	}
	return 1.055*math.Pow(v, 1/2.4) - 0.055
}
func From(c scene.Color) Pixel {
	return Pixel{linear(c.R) * c.A, linear(c.G) * c.A, linear(c.B) * c.A, c.A}
}
func (p Pixel) Scale(a float64) Pixel { return Pixel{p.R * a, p.G * a, p.B * a, p.A * a} }
func (p Pixel) RGBA8() (uint8, uint8, uint8, uint8) {
	if p.A <= 0 {
		return 0, 0, 0, 0
	}
	return uint8(math.Round(srgb(p.R/p.A) * 255)), uint8(math.Round(srgb(p.G/p.A) * 255)), uint8(math.Round(srgb(p.B/p.A) * 255)), uint8(math.Round(math.Min(1, p.A) * 255))
}
func mix(a, b, t float64) float64 { return a + (b-a)*t }
func Sample(b scene.Brush, p geom.Point) Pixel {
	if b.Kind == "solid" {
		return From(b.Color)
	}
	dx, dy := b.To.X-b.From.X, b.To.Y-b.From.Y
	t := ((p.X-b.From.X)*dx + (p.Y-b.From.Y)*dy) / (dx*dx + dy*dy)
	t = math.Max(0, math.Min(1, t))
	for i := 1; i < len(b.Stops); i++ {
		a, z := b.Stops[i-1], b.Stops[i]
		if t <= z.At {
			u := (t - a.At) / (z.At - a.At)
			x, y := From(a.Color), From(z.Color)
			return Pixel{mix(x.R, y.R, u), mix(x.G, y.G, u), mix(x.B, y.B, u), mix(x.A, y.A, u)}
		}
	}
	return From(b.Stops[len(b.Stops)-1].Color)
}
func Over(dst, src Pixel, mode string) Pixel {
	blend := func(d, s float64) float64 {
		if mode == "normal" || src.A == 0 || dst.A == 0 {
			return s + d*(1-src.A)
		}
		a, b := d/dst.A, s/src.A
		v := a * b
		if mode == "screen" {
			v = a + b - a*b
		}
		return (1-src.A)*d + (1-dst.A)*s + src.A*dst.A*v
	}
	return Pixel{blend(dst.R, src.R), blend(dst.G, src.G), blend(dst.B, src.B), src.A + dst.A*(1-src.A)}
}

// Content returns one layer's source pixel at parent-space point p. Component
// layers recurse through their frozen transform: the inverse mapping keeps
// child clips, masks and gradient axes in the child's local coordinates, so a
// mirrored or scaled instance preserves their local meaning.
func Content(p geom.Point, l scene.CompiledLayer) Pixel {
	if l.Component != nil {
		return Layers(l.Component.Inverse.Apply(p), l.Component.Layers)
	}
	src := Pixel{}
	for _, s := range l.Shapes {
		q := s.Inverse.Apply(p)
		if s.Path.Contains(q, s.Shape.FillRule) {
			src = Over(src, Sample(s.Shape.Brush, q), "normal")
		}
	}
	return src
}

// Layers composites a layer list onto transparency at p.
func Layers(p geom.Point, layers []scene.CompiledLayer) Pixel {
	dst := Pixel{}
	for _, l := range layers {
		if len(l.Clip) > 0 && !l.Clip.Contains(p, "nonzero") {
			continue
		}
		if len(l.Mask) > 0 && !l.Mask.Contains(p, "nonzero") {
			continue
		}
		dst = Over(dst, Content(p, l).Scale(l.Layer.Opacity), l.Layer.Blend)
	}
	return dst
}
