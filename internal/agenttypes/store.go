package agenttypes

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Definition describes one known agent type for bootstrap, validation, and
// discovery matching.
type Definition struct {
	PromptPattern string `json:"prompt_pattern,omitempty"`
	ResponseWait  int    `json:"response_wait"`
}

// Store manages the runtime ~/.agentbus/agents.json file.
type Store struct {
	Path string
}

// New returns a Store rooted at path.
func New(path string) *Store {
	return &Store{Path: path}
}

// DefaultDefinitions returns the built-in agent types bootstrapped on first run.
func DefaultDefinitions() map[string]Definition {
	return map[string]Definition{
		"claude": {ResponseWait: 2},
		"codex":  {ResponseWait: 2},
	}
}

// Load ensures the store exists, bootstrapping defaults when missing, and then
// returns all definitions.
func (s *Store) Load() (map[string]Definition, error) {
	if err := s.ensureFile(); err != nil {
		return nil, err
	}
	return s.read()
}

// Add validates and writes one custom agent type atomically.
func (s *Store) Add(name string, def Definition) error {
	defs, err := s.Load()
	if err != nil {
		return err
	}
	name, err = normalizeName(name)
	if err != nil {
		return err
	}
	if err := validateDefinition(def); err != nil {
		return err
	}
	defs[name] = def
	return s.write(defs)
}

func normalizeName(name string) (string, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return "", fmt.Errorf("agent type name is required")
	}
	return name, nil
}

func validateDefinition(def Definition) error {
	if def.ResponseWait <= 0 {
		return fmt.Errorf("response wait must be > 0")
	}
	if def.PromptPattern != "" {
		if _, err := regexp.Compile(def.PromptPattern); err != nil {
			return fmt.Errorf("compile prompt pattern: %w", err)
		}
	}
	return nil
}

func (s *Store) ensureFile() error {
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return fmt.Errorf("create agent-types dir: %w", err)
	}
	if _, err := os.Stat(s.Path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat agent-types file: %w", err)
	}
	return s.write(DefaultDefinitions())
}

func (s *Store) read() (map[string]Definition, error) {
	raw, err := os.ReadFile(s.Path)
	if err != nil {
		return nil, fmt.Errorf("read agent-types file: %w", err)
	}
	var defs map[string]Definition
	if err := json.Unmarshal(raw, &defs); err != nil {
		return nil, fmt.Errorf("parse agent-types file: %w", err)
	}
	if defs == nil {
		defs = map[string]Definition{}
	}
	for name, def := range defs {
		normalized, err := normalizeName(name)
		if err != nil {
			return nil, err
		}
		if normalized != name {
			delete(defs, name)
			defs[normalized] = def
		}
		if err := validateDefinition(def); err != nil {
			return nil, fmt.Errorf("validate %s: %w", normalized, err)
		}
	}
	return defs, nil
}

func (s *Store) write(defs map[string]Definition) error {
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return fmt.Errorf("create agent-types dir: %w", err)
	}
	ordered := make(map[string]Definition, len(defs))
	keys := make([]string, 0, len(defs))
	for name := range defs {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	for _, name := range keys {
		ordered[name] = defs[name]
	}
	raw, err := json.MarshalIndent(ordered, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal agent-types file: %w", err)
	}
	raw = append(raw, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(s.Path), "agents-*.json")
	if err != nil {
		return fmt.Errorf("create temp agent-types file: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp agent-types file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp agent-types file: %w", err)
	}
	if err := os.Rename(tmpPath, s.Path); err != nil {
		return fmt.Errorf("replace agent-types file: %w", err)
	}
	return nil
}
