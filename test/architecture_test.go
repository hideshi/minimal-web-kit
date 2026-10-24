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
	inRequireBlock := false

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "require (") {
			inRequireBlock = true
			continue
		}
		if inRequireBlock && line == ")" {
			inRequireBlock = false
			continue
		}

		if inRequireBlock || strings.HasPrefix(line, "require ") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				pkg := parts[0]
				if parts[0] == "require" {
					pkg = parts[1]
				}
				// 間接依存(// indirect)は除外
				if strings.Contains(line, "// indirect") {
					continue
				}

				if !allowedDirectDeps[pkg] {
					t.Errorf("Disallowed direct dependency found: %s. Direct dependencies must be explicitly approved.", pkg)
				}
			}
		}
	}
}
