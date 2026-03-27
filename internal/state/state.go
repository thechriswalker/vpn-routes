package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type RouteKind string

const (
	RouteKindHost RouteKind = "host"
	RouteKindNet  RouteKind = "net"
)

type OwnedRoute struct {
	Kind    RouteKind `json:"kind"`
	Dest    string    `json:"dest"` // IPv4 string or CIDR string
	Dev     string    `json:"dev"`
	Sources []string  `json:"sources,omitempty"` // hostnames or descriptors that produced this dest
}

type OwnedState struct {
	Routes []OwnedRoute `json:"routes"`
}

type Store interface {
	Load() (OwnedState, error)
	Save(st OwnedState) error
}

type FileStore struct {
	path string
}

func NewFileStore(path string) (*FileStore, error) {
	if path == "" {
		return nil, errors.New("state path is empty")
	}
	return &FileStore{path: path}, nil
}

func (s *FileStore) Load() (OwnedState, error) {
	b, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return OwnedState{}, nil
		}
		return OwnedState{}, fmt.Errorf("read state: %w", err)
	}
	if len(b) == 0 {
		return OwnedState{}, nil
	}
	var st OwnedState
	if err := json.Unmarshal(b, &st); err != nil {
		return OwnedState{}, fmt.Errorf("parse state: %w", err)
	}
	return st, nil
}

func (s *FileStore) Save(st OwnedState) error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir state dir: %w", err)
	}

	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}
	b = append(b, '\n')

	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return fmt.Errorf("write temp state: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename temp state: %w", err)
	}
	return nil
}

