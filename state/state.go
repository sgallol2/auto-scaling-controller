// Package state persists the controller knowledge in a local JSON file.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"autoscaler-controller/types"
)

type Store struct {
	path string
}

func New(path string) *Store {
	return &Store{path: path}
}

func (s *Store) Load() (types.KnowledgeState, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return types.KnowledgeState{}, nil
	}
	if err != nil {
		return types.KnowledgeState{}, fmt.Errorf("read state file: %w", err)
	}

	var knowledge types.KnowledgeState
	if err := json.Unmarshal(data, &knowledge); err != nil {
		return types.KnowledgeState{}, fmt.Errorf("decode state file: %w", err)
	}
	return knowledge, nil
}

func (s *Store) Save(knowledge types.KnowledgeState) error {
	data, err := json.MarshalIndent(knowledge, "", "  ")
	if err != nil {
		return fmt.Errorf("encode state file: %w", err)
	}

	if dir := filepath.Dir(s.path); dir != "." {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return fmt.Errorf("create state directory: %w", err)
		}
	}

	temporaryPath := s.path + ".tmp"
	if err := os.WriteFile(temporaryPath, data, 0600); err != nil {
		return fmt.Errorf("write temporary state file: %w", err)
	}
	if err := os.Rename(temporaryPath, s.path); err != nil {
		_ = os.Remove(temporaryPath)
		return fmt.Errorf("replace state file: %w", err)
	}
	return nil
}
