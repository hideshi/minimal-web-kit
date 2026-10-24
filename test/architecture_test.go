package test

import (
	"os"
	"strings"
	"testing"
)

// TestArchitecture_DependencyAllowedList は go.mod の直接依存が許可リストのみであることを検証します。
func TestArchitecture_DependencyAllowedList(t *testing.T) {
	content, err := os.ReadFile("../go.mod")
	if err != nil {
		t.Fatalf("failed to read go.mod: %v", err)
	}

	allowedDirectDeps := map[string]bool{
		"modernc.org/sqlite": true,
	}

	lines := strings.Split(string(content), "\n")
	foundDirectDeps := make(map[string]bool)

	inRequireBlock := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "require (") {
			inRequireBlock = true
			continue
		}
		if inRequireBlock && trimmed == ")" {
			inRequireBlock = false
			continue
		}

		if inRequireBlock || strings.HasPrefix(trimmed, "require ") {
			// indirect 行はスキップ
			if strings.Contains(trimmed, "// indirect") {
				continue
			}

			parts := strings.Fields(trimmed)
			if len(parts) >= 2 {
				pkg := parts[0]
				if pkg == "require" {
					pkg = parts[1]
				}
				foundDirectDeps[pkg] = true
			}
		}
	}

	if len(foundDirectDeps) == 0 {
		t.Fatal("No direct dependencies found in go.mod. Verification failed to inspect direct dependencies.")
	}

	for dep := range foundDirectDeps {
		if !allowedDirectDeps[dep] {
			t.Errorf("Disallowed direct dependency found: %s. Direct dependencies must be explicitly approved and added to allowed list.", dep)
		}
	}

	for allowed := range allowedDirectDeps {
		if !foundDirectDeps[allowed] {
			t.Errorf("Expected direct dependency %s is missing from go.mod direct requirements.", allowed)
		}
	}
}
