// Package service joins revision facts, render computation and publication rights.
package service

import (
	"context"
	"fmt"
	"prismplate/internal/bundle"
	"prismplate/internal/exposure"
	"prismplate/internal/raster"
	"prismplate/internal/repository"
	"prismplate/internal/scene"
	"prismplate/internal/validate"
	"prismplate/internal/vector"
	"strconv"
	"time"
)

type Service struct {
	Repo  *repository.Repo
	Pool  *exposure.Pool
	slots chan struct{}
}

func New(r *repository.Repo) *Service {
	return &Service{Repo: r, Pool: exposure.New(2, 8), slots: make(chan struct{}, 2)}
}
func (s *Service) Close() { s.Pool.Close() }

// freezeRefs pins every component mount to the digest of the exact revision it
// names and records that document in the snapshot. A stored revision is then
// self-sufficient: later edits to the source plate can never alter it.
//
// A digest alone (as found in branches and imports) is authoritative and never
// resolved through plate names, so a receiver needs no same-named plate. Plate
// and revision, when also present, are provenance and must agree with it.
//
// self identifies the plate being written and next the revision being created.
// A mount naming a revision that does not exist yet at that plate is an explicit
// cycle rather than a transient miss.
func freezeRefs(st *repository.State, d scene.Document, self string, next int) (scene.Document, error) {
	if st.Components == nil {
		st.Components = map[string]scene.Document{}
	}
	for i := range d.Layers {
		ref := d.Layers[i].Component
		if ref == nil {
			continue
		}
		if ref.Plate == self && ref.Revision >= next {
			return d, fmt.Errorf("component reference cycle: %s cannot mount its own revision %d while writing revision %d", self, ref.Revision, next)
		}
		var source scene.Document
		if ref.Digest != "" {
			doc, ok := st.Components[ref.Digest]
			if !ok {
				return d, fmt.Errorf("component %s: %w", ref.Digest, repository.ErrMissing)
			}
			source = doc
			// Provenance is informational, but when the named plate is present it
			// must still point at the same frozen content.
			if ref.Plate != "" {
				if p, ok := st.Plates[ref.Plate]; ok && ref.Revision >= 1 && ref.Revision <= len(p.Revisions) && p.Revisions[ref.Revision-1].Digest != ref.Digest {
					return d, fmt.Errorf("component digest does not match named revision: %w", repository.ErrConflict)
				}
			}
		} else {
			if !validate.ID(ref.Plate) || ref.Revision < 1 {
				return d, fmt.Errorf("component reference requires plate+revision or digest")
			}
			p, ok := st.Plates[ref.Plate]
			if !ok {
				return d, fmt.Errorf("component source plate %q: %w", ref.Plate, repository.ErrMissing)
			}
			if ref.Revision > len(p.Revisions) {
				return d, fmt.Errorf("component source revision %d: %w", ref.Revision, repository.ErrMissing)
			}
			source = p.Revisions[ref.Revision-1].Document
		}
		digest := scene.Hash(source)
		if ref.Digest != "" && ref.Digest != digest {
			return d, fmt.Errorf("component digest does not match named revision: %w", repository.ErrConflict)
		}
		if err := freezeClosure(st, source); err != nil {
			return d, err
		}
		st.Components[digest] = source
		ref.Digest = digest
	}
	return d, nil
}

// freezeClosure proves a mounted document's own references are already frozen in
// the snapshot, so the copy about to be stored is offline-complete. Documents
// are content-addressed and shared, so a component mounted by several plates is
// stored once.
func freezeClosure(st *repository.State, d scene.Document) error {
	if _, err := scene.Closure(d, st.Components); err != nil {
		return fmt.Errorf("component closure is incomplete: %w", err)
	}
	return nil
}

func (s *Service) Create(id, title string, d scene.Document) (repository.Plate, error) {
	if !validate.ID(id) || len(title) == 0 || len(title) > 160 {
		return repository.Plate{}, fmt.Errorf("invalid plate identity")
	}
	var plate repository.Plate
	err := s.Repo.Update(func(st *repository.State) error {
		if _, ok := st.Plates[id]; ok {
			return repository.ErrConflict
		}
		frozen, err := freezeRefs(st, d, id, 1)
		if err != nil {
			return err
		}
		if _, err := validate.CompileWithStore(frozen, st.Components); err != nil {
			return err
		}
		plate = repository.Plate{ID: id, Title: title, State: "active", Revisions: []repository.Revision{{Number: 1, Digest: repository.SceneHash(frozen), Document: frozen, Created: time.Now().UTC()}}}
		st.Plates[id] = plate
		return nil
	})
	return plate, err
}
func (s *Service) Plate(id string) (repository.Plate, error) {
	p, ok := s.Repo.View().Plates[id]
	if !ok {
		return p, repository.ErrMissing
	}
	return p, nil
}
func (s *Service) Revise(id string, expected int, d scene.Document) (repository.Plate, error) {
	var p repository.Plate
	err := s.Repo.Update(func(st *repository.State) error {
		var ok bool
		p, ok = st.Plates[id]
		if !ok {
			return repository.ErrMissing
		}
		if p.State != "active" {
			return repository.ErrState
		}
		if len(p.Revisions) != expected {
			return repository.ErrConflict
		}
		if len(p.Revisions) >= 64 {
			return fmt.Errorf("revision capacity reached")
		}
		frozen, err := freezeRefs(st, d, id, expected+1)
		if err != nil {
			return err
		}
		if _, err := validate.CompileWithStore(frozen, st.Components); err != nil {
			return err
		}
		p.Revisions = append(p.Revisions, repository.Revision{Number: expected + 1, Digest: repository.SceneHash(frozen), Document: frozen, Created: time.Now().UTC()})
		st.Plates[id] = p
		return nil
	})
	return p, err
}
func (s *Service) Archive(id string) error {
	return s.Repo.Update(func(st *repository.State) error {
		p, ok := st.Plates[id]
		if !ok {
			return repository.ErrMissing
		}
		p.State = "archived"
		st.Plates[id] = p
		return nil
	})
}
func (s *Service) Branch(id string, revision int, target, title string) (repository.Plate, error) {
	p, err := s.Plate(id)
	if err != nil {
		return p, err
	}
	if revision < 1 || revision > len(p.Revisions) {
		return p, repository.ErrMissing
	}
	return s.Create(target, title, p.Revisions[revision-1].Document)
}
func (s *Service) capture(id string, revision int) (repository.Revision, error) {
	p, err := s.Plate(id)
	if err != nil {
		return repository.Revision{}, err
	}
	if revision < 1 || revision > len(p.Revisions) {
		return repository.Revision{}, repository.ErrMissing
	}
	return p.Revisions[revision-1], nil
}
func (s *Service) render(ctx context.Context, id string, r repository.Revision, format string) (repository.Artifact, error) {
	var a repository.Artifact
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	case <-ctx.Done():
		return a, ctx.Err()
	default:
		return a, exposure.ErrBusy
	}
	c, err := validate.CompileWithStore(r.Document, s.Repo.Components())
	if err != nil {
		return a, err
	}
	var data []byte
	switch format {
	case "png":
		data, err = raster.Render(ctx, c)
	case "svg":
		data = vector.Export(c)
	default:
		return a, fmt.Errorf("format must be png or svg")
	}
	if err != nil {
		return a, err
	}
	digest := repository.Hash(data)
	key := repository.Hash([]byte(id + ":" + strconv.Itoa(r.Number) + ":" + r.Digest + ":" + format + ":" + digest))
	a = repository.Artifact{ID: key, Plate: id, Revision: r.Number, SceneDigest: r.Digest, Format: format, Digest: digest, State: "available", Data: data}
	err = s.Repo.Update(func(st *repository.State) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if old, ok := st.Artifacts[key]; ok {
			if old.State != "available" {
				return repository.ErrState
			}
			a = old
			return nil
		}
		st.Artifacts[key] = a
		return nil
	})
	return a, err
}
func (s *Service) Render(ctx context.Context, id string, revision int, format string) (repository.Artifact, error) {
	r, err := s.capture(id, revision)
	if err != nil {
		return repository.Artifact{}, err
	}
	return s.render(ctx, id, r, format)
}
func (s *Service) Submit(id string, revision int, format string) (exposure.Job, error) {
	r, err := s.capture(id, revision)
	if err != nil {
		return exposure.Job{}, err
	}
	if format != "png" && format != "svg" {
		return exposure.Job{}, fmt.Errorf("unknown format")
	}
	return s.Pool.Submit(func(ctx context.Context) (string, error) {
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		a, e := s.render(ctx, id, r, format)
		return a.ID, e
	})
}
func (s *Service) Artifact(id string) (repository.Artifact, error) {
	a, ok := s.Repo.View().Artifacts[id]
	if !ok {
		return a, repository.ErrMissing
	}
	if a.State != "available" {
		return a, repository.ErrState
	}
	return a, nil
}
func (s *Service) Revoke(id string) error {
	return s.Repo.Update(func(st *repository.State) error {
		a, ok := st.Artifacts[id]
		if !ok {
			return repository.ErrMissing
		}
		for _, p := range st.Proofs {
			if p.Artifact == id && p.State == "accepted" {
				return repository.ErrState
			}
		}
		a.State = "revoked"
		st.Artifacts[id] = a
		return nil
	})
}
func (s *Service) OpenProof(id, artifact string) (repository.Proof, error) {
	p := repository.Proof{ID: id, Artifact: artifact, State: "open"}
	if !validate.ID(id) {
		return p, fmt.Errorf("invalid proof identity")
	}
	err := s.Repo.Update(func(st *repository.State) error {
		if _, ok := st.Proofs[id]; ok {
			return repository.ErrConflict
		}
		a, ok := st.Artifacts[artifact]
		if !ok {
			return repository.ErrMissing
		}
		if a.State != "available" {
			return repository.ErrState
		}
		st.Proofs[id] = p
		return nil
	})
	return p, err
}
func (s *Service) Decide(id, state, note string) (repository.Proof, error) {
	var p repository.Proof
	if (state != "accepted" && state != "rejected") || len(note) > 1000 {
		return p, fmt.Errorf("invalid proof decision")
	}
	err := s.Repo.Update(func(st *repository.State) error {
		var ok bool
		p, ok = st.Proofs[id]
		if !ok {
			return repository.ErrMissing
		}
		if p.State != "open" {
			return repository.ErrState
		}
		if st.Artifacts[p.Artifact].State != "available" {
			return repository.ErrState
		}
		p.State = state
		p.Note = note
		st.Proofs[id] = p
		return nil
	})
	return p, err
}
func (s *Service) Export(id string) ([]byte, error) {
	st := s.Repo.View()
	p, ok := st.Plates[id]
	if !ok {
		return nil, repository.ErrMissing
	}
	return bundle.Seal(p, st.Components)
}
func (s *Service) Import(raw []byte) (repository.Plate, error) {
	env, e := bundle.Open(raw)
	if e != nil {
		return repository.Plate{}, e
	}
	p := env.Plate
	e = s.Repo.Update(func(st *repository.State) error {
		if _, ok := st.Plates[p.ID]; ok {
			return repository.ErrConflict
		}
		for digest, d := range env.Components {
			if existing, ok := st.Components[digest]; ok {
				if scene.Hash(existing) != digest || scene.Hash(d) != digest {
					return fmt.Errorf("imported component %s conflicts with local content", digest)
				}
				continue
			}
			st.Components[digest] = d
		}
		for _, r := range p.Revisions {
			if _, err := scene.Closure(r.Document, st.Components); err != nil {
				return err
			}
			if _, err := validate.CompileWithStore(r.Document, st.Components); err != nil {
				return err
			}
		}
		st.Plates[p.ID] = p
		return nil
	})
	if e != nil {
		return repository.Plate{}, e
	}
	return p, nil
}
