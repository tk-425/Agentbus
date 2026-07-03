package agenttypes

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadBootstrapsDefaultsWhenFileIsMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".agentbus", "agents.json")
	s := New(path)

	defs, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("agents.json should be created, got: %v", err)
	}
	if _, ok := defs["claude"]; !ok {
		t.Fatalf("bootstrapped definitions should include claude")
	}
	if _, ok := defs["codex"]; !ok {
		t.Fatalf("bootstrapped definitions should include codex")
	}
	if defs["claude"].ResponseWait <= 0 {
		t.Fatalf("claude response wait must be positive, got %d", defs["claude"].ResponseWait)
	}
}

func TestAddRejectsInvalidDefinitions(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".agentbus", "agents.json")
	s := New(path)

	if err := s.Add("gemini", Definition{ResponseWait: 0}); err == nil {
		t.Fatalf("Add should reject non-positive response wait")
	}
	if err := s.Add("gemini", Definition{ResponseWait: 2, PromptPattern: "("}); err == nil {
		t.Fatalf("Add should reject an invalid prompt pattern")
	}
}

func TestAddStoresValidCustomTypeWithoutPromptPattern(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".agentbus", "agents.json")
	s := New(path)

	if err := s.Add("Gemini", Definition{ResponseWait: 5}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	defs, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	def, ok := defs["gemini"]
	if !ok {
		t.Fatalf("custom type gemini should be stored")
	}
	if def.ResponseWait != 5 {
		t.Fatalf("gemini response wait = %d, want 5", def.ResponseWait)
	}
	if def.PromptPattern != "" {
		t.Fatalf("gemini prompt pattern = %q, want empty", def.PromptPattern)
	}
}
