// Package bundle seals portable scene history without private runtime state.
package bundle

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"prismplate/internal/repository"
	"prismplate/internal/scene"
	"prismplate/internal/validate"
)

// Envelope carries a plate and every frozen document its revisions mount, so a
// receiver can reproduce the imagery offline without holding the source plate.
// Components is keyed by content digest and deduplicated across the closure.
type Envelope struct {
	Schema     string                    `json:"schema"`
	Digest     string                    `json:"digest"`
	Plate      repository.Plate          `json:"plate"`
	Components map[string]scene.Document `json:"components"`
}

func Seal(p repository.Plate, store map[string]scene.Document) ([]byte, error) {
	closure := map[string]scene.Document{}
	for _, r := range p.Revisions {
		digests, err := scene.Closure(r.Document, store)
		if err != nil {
			return nil, err
		}
		for _, digest := range digests {
			closure[digest] = store[digest]
		}
	}
	env := Envelope{Schema: "prism-plate-bundle-v2", Plate: p, Components: closure}
	b, err := json.Marshal(env)
	if err != nil {
		return nil, err
	}
	env.Digest = repository.Hash(b)
	return json.Marshal(env)
}

// Open verifies the seal, the frozen closure and every revision before the
// plate can be admitted by a receiving instance.
func Open(raw []byte) (Envelope, error) {
	var e Envelope
	if len(raw) > 8<<20 {
		return e, fmt.Errorf("bundle too large")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&e); err != nil {
		return e, err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return e, fmt.Errorf("trailing bundle content")
	}
	if e.Schema != "prism-plate-bundle-v2" {
		return e, fmt.Errorf("unsupported bundle schema %q: components travel only in prism-plate-bundle-v2", e.Schema)
	}
	seal := e.Digest
	e.Digest = ""
	b, err := json.Marshal(e)
	if err != nil {
		return e, err
	}
	if repository.Hash(b) != seal {
		return e, fmt.Errorf("bundle digest mismatch")
	}
	e.Digest = seal
	if e.Components == nil {
		e.Components = map[string]scene.Document{}
	}
	for digest, doc := range e.Components {
		if scene.Hash(doc) != digest {
			return e, fmt.Errorf("invalid component digest %s", digest)
		}
	}
	if !validate.ID(e.Plate.ID) || len(e.Plate.Revisions) == 0 || len(e.Plate.Revisions) > 64 || len(e.Plate.Title) > 160 || (e.Plate.State != "active" && e.Plate.State != "archived") {
		return e, fmt.Errorf("invalid plate metadata")
	}
	s := repository.Fresh()
	for digest, doc := range e.Components {
		s.Components[digest] = doc
	}
	s.Plates[e.Plate.ID] = e.Plate
	if err = repository.Check(s); err != nil {
		return e, err
	}
	for _, r := range e.Plate.Revisions {
		if _, err = validate.CompileWithStore(r.Document, e.Components); err != nil {
			return e, err
		}
	}
	return e, nil
}
