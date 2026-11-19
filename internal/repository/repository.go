// Package repository commits complete immutable snapshots under a single writer.
package repository

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"prismplate/internal/scene"
	"sync"
	"time"
)

var ErrConflict = errors.New("revision conflict")
var ErrMissing = errors.New("not found")
var ErrState = errors.New("invalid lifecycle state")

type Revision struct {
	Number   int            `json:"number"`
	Digest   string         `json:"digest"`
	Document scene.Document `json:"document"`
	Created  time.Time      `json:"created"`
}
type Plate struct {
	ID        string     `json:"id"`
	Title     string     `json:"title"`
	State     string     `json:"state"`
	Revisions []Revision `json:"revisions"`
}
type Artifact struct {
	ID          string `json:"id"`
	Plate       string `json:"plate"`
	Revision    int    `json:"revision"`
	SceneDigest string `json:"scene_digest"`
	Format      string `json:"format"`
	Digest      string `json:"digest"`
	State       string `json:"state"`
	Data        []byte `json:"data"`
}
type Proof struct {
	ID       string `json:"id"`
	Artifact string `json:"artifact"`
	State    string `json:"state"`
	Note     string `json:"note"`
}
type State struct {
	Schema     string                    `json:"schema"`
	Plates     map[string]Plate          `json:"plates"`
	Artifacts  map[string]Artifact       `json:"artifacts"`
	Proofs     map[string]Proof          `json:"proofs"`
	Components map[string]scene.Document `json:"components"`
}
type Repo struct {
	mu    sync.RWMutex
	root  string
	state State
	lock  *os.File
}

func Hash(b []byte) string              { v := sha256.Sum256(b); return hex.EncodeToString(v[:]) }
func SceneHash(d scene.Document) string { b, _ := json.Marshal(d); return Hash(b) }
func Fresh() State {
	return State{Schema: "prism-plate-v1", Plates: map[string]Plate{}, Artifacts: map[string]Artifact{}, Proofs: map[string]Proof{}, Components: map[string]scene.Document{}}
}
func Open(root string) (*Repo, error) {
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(filepath.Join(root, "writer.lock"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, fmt.Errorf("data root already locked; after an unclean exit verify no writer remains before removing writer.lock: %w", err)
	}
	r := &Repo{root: root, state: Fresh(), lock: lock}
	fmt.Fprintln(lock, os.Getpid())
	b, err := os.ReadFile(filepath.Join(root, "state.json"))
	if err == nil {
		if len(b) > 128<<20 {
			err = fmt.Errorf("snapshot too large")
		} else {
			err = json.Unmarshal(b, &r.state)
		}
		if err == nil {
			if r.state.Components == nil {
				// Snapshots written before component support have no closure.
				r.state.Components = map[string]scene.Document{}
			}
			err = Check(r.state)
		}
	}
	if err != nil && !os.IsNotExist(err) {
		r.Close()
		return nil, err
	}
	return r, nil
}

// Check verifies internal lineage before a restored state can become authoritative.
func Check(s State) error {
	if s.Schema != "prism-plate-v1" || s.Plates == nil || s.Artifacts == nil || s.Proofs == nil {
		return fmt.Errorf("unsupported snapshot schema")
	}
	if s.Components == nil {
		s.Components = map[string]scene.Document{}
	}
	for digest, d := range s.Components {
		if scene.Hash(d) != digest {
			return fmt.Errorf("invalid component digest")
		}
	}
	for id, p := range s.Plates {
		if p.ID != id || len(p.Revisions) == 0 || (p.State != "active" && p.State != "archived") {
			return fmt.Errorf("invalid plate lineage")
		}
		for i, r := range p.Revisions {
			if r.Number != i+1 || r.Digest != SceneHash(r.Document) {
				return fmt.Errorf("invalid revision digest")
			}
			if err := checkClosure(r.Document, s.Components); err != nil {
				return err
			}
		}
	}
	for id, a := range s.Artifacts {
		p, ok := s.Plates[a.Plate]
		if !ok || a.ID != id || a.Revision < 1 || a.Revision > len(p.Revisions) || a.SceneDigest != p.Revisions[a.Revision-1].Digest || Hash(a.Data) != a.Digest || (a.State != "available" && a.State != "revoked") || (a.Format != "png" && a.Format != "svg") {
			return fmt.Errorf("invalid artifact lineage")
		}
	}
	for id, p := range s.Proofs {
		_, ok := s.Artifacts[p.Artifact]
		if p.ID != id || !ok || (p.State != "open" && p.State != "accepted" && p.State != "rejected") {
			return fmt.Errorf("invalid proof lineage")
		}
	}
	return nil
}

// checkClosure makes the frozen component set self-contained: every mounted
// digest must exist, match its key and be reachable without cycles.
func checkClosure(d scene.Document, store map[string]scene.Document) error {
	if _, err := scene.Closure(d, store); err != nil {
		return fmt.Errorf("invalid component closure: %w", err)
	}
	return nil
}
func clone(s State) State {
	b, _ := json.Marshal(s)
	var d State
	_ = json.Unmarshal(b, &d)
	if d.Components == nil {
		d.Components = map[string]scene.Document{}
	}
	return d
}
func (r *Repo) View() State { r.mu.RLock(); defer r.mu.RUnlock(); return clone(r.state) }

// Components returns only the frozen component documents. Render paths use this
// instead of View so a snapshot clone never copies artifact bytes.
func (r *Repo) Components() map[string]scene.Document {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]scene.Document, len(r.state.Components))
	for k, v := range r.state.Components {
		out[k] = v
	}
	return out
}

// Update keeps failed persistence invisible. Rename is the commit point; directory
// sync is best-effort because failure after rename cannot be represented as rollback.
func (r *Repo) Update(fn func(*State) error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.lock == nil {
		return ErrState
	}
	next := clone(r.state)
	if err := fn(&next); err != nil {
		return err
	}
	if err := Check(next); err != nil {
		return err
	}
	if len(next.Plates) > 128 || len(next.Artifacts) > 256 || len(next.Proofs) > 1024 || len(next.Components) > 4096 {
		return fmt.Errorf("snapshot object capacity reached")
	}
	b, err := json.Marshal(next)
	if err != nil {
		return err
	}
	if len(b) > 128<<20 {
		return fmt.Errorf("snapshot byte capacity reached")
	}
	f, err := os.CreateTemp(r.root, "state-*.tmp")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(name, filepath.Join(r.root, "state.json")); err != nil {
		return err
	}
	r.state = next
	if dir, e := os.Open(r.root); e == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}
func (r *Repo) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.lock == nil {
		return nil
	}
	err := r.lock.Close()
	r.lock = nil
	if e := os.Remove(filepath.Join(r.root, "writer.lock")); err == nil {
		err = e
	}
	return err
}
