// Package vector exports the compiled contour representation without raw markup.
package vector

import (
	"bytes"
	"fmt"
	"prismplate/internal/geom"
	"prismplate/internal/scene"
	"strings"
)

func number(x float64) string { return fmt.Sprintf("%.9g", x) }
func path(p geom.Path) string {
	var b strings.Builder
	for _, ring := range p {
		for i, v := range ring {
			verb := "L"
			if i == 0 {
				verb = "M"
			}
			fmt.Fprintf(&b, "%s%s %s", verb, number(v.X), number(v.Y))
		}
		b.WriteString("Z")
	}
	return b.String()
}
func color(c scene.Color) string {
	return fmt.Sprintf("rgb(%s%%,%s%%,%s%%)", number(c.R*100), number(c.G*100), number(c.B*100))
}
func blend(mode string) string {
	return map[string]string{"normal": "normal", "multiply": "multiply", "screen": "screen"}[mode]
}

// defs walks the expanded component tree so mounted closures receive fresh,
// document-local gradient and clip ids even when a component is reused.
func defs(b *bytes.Buffer, layers []scene.CompiledLayer) {
	for _, l := range layers {
		if len(l.Clip) > 0 {
			fmt.Fprintf(b, `<clipPath id="%s" clipPathUnits="userSpaceOnUse"><path d="%s"/></clipPath>`, l.ClipID, path(l.Clip))
		}
		if len(l.Mask) > 0 {
			fmt.Fprintf(b, `<clipPath id="%s" clipPathUnits="userSpaceOnUse"><path d="%s"/></clipPath>`, l.MaskID, path(l.Mask))
		}
		if l.Component != nil {
			defs(b, l.Component.Layers)
			continue
		}
		for _, s := range l.Shapes {
			br := s.Shape.Brush
			if br.Kind != "linear" {
				continue
			}
			fmt.Fprintf(b, `<linearGradient id="%s" gradientUnits="userSpaceOnUse" x1="%s" y1="%s" x2="%s" y2="%s">`, s.GradientID, number(br.From.X), number(br.From.Y), number(br.To.X), number(br.To.Y))
			for _, stop := range br.Stops {
				fmt.Fprintf(b, `<stop offset="%s" stop-color="%s" stop-opacity="%s"/>`, number(stop.At), color(stop.Color), number(stop.Color.A))
			}
			b.WriteString(`</linearGradient>`)
		}
	}
}

// group emits one layer list. Each layer keeps its own opacity, blend, clip and
// mask in the coordinate space it was declared in; a component layer adds one
// transformed nested group so child geometry keeps its local meaning.
func group(b *bytes.Buffer, layers []scene.CompiledLayer) {
	for _, l := range layers {
		attrs := fmt.Sprintf(`<g opacity="%s" style="isolation:isolate;mix-blend-mode:%s"`, number(l.Layer.Opacity), blend(l.Layer.Blend))
		if len(l.Clip) > 0 {
			attrs += fmt.Sprintf(` clip-path="url(#%s)"`, l.ClipID)
		}
		if len(l.Mask) > 0 {
			attrs += fmt.Sprintf(` clip-path="url(#%s)"`, l.MaskID)
		}
		b.WriteString(attrs)
		if l.Component != nil {
			m := l.Component.Transform
			fmt.Fprintf(b, `><g transform="matrix(%s %s %s %s %s %s)">`, number(m[0]), number(m[1]), number(m[2]), number(m[3]), number(m[4]), number(m[5]))
			group(b, l.Component.Layers)
			b.WriteString(`</g></g>`)
			continue
		}
		b.WriteString(`><g>`)
		for _, s := range l.Shapes {
			m := s.Shape.Transform
			br := s.Shape.Brush
			fill := color(br.Color)
			alpha := br.Color.A
			if br.Kind == "linear" {
				fill = fmt.Sprintf("url(#%s)", s.GradientID)
				alpha = 1
			}
			fmt.Fprintf(b, `<path d="%s" fill="%s" fill-opacity="%s" fill-rule="%s" transform="matrix(%s %s %s %s %s %s)"/>`, path(s.Path), fill, number(alpha), s.Shape.FillRule, number(m[0]), number(m[1]), number(m[2]), number(m[3]), number(m[4]), number(m[5]))
		}
		b.WriteString(`</g></g>`)
	}
}

// SVG uses the same flattened geometry. Browser gradient alpha interpolation and
// pixel coverage may differ from the canonical PNG renderer; SVG is a vector proof.
func Export(c scene.Compiled) []byte {
	var b bytes.Buffer
	d := c.Document
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d" color-interpolation="linearRGB"><defs>`, d.Width, d.Height, d.Width, d.Height)
	defs(&b, c.Layers)
	b.WriteString(`</defs>`)
	fmt.Fprintf(&b, `<rect width="100%%" height="100%%" fill="%s" fill-opacity="%s"/>`, color(d.Background), number(d.Background.A))
	group(&b, c.Layers)
	b.WriteString(`</svg>`)
	return b.Bytes()
}
