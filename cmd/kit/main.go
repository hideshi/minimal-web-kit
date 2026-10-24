package main

import (
	"fmt"
	"os"
	"strings"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		return
	}

	cmd := os.Args[1]
	switch cmd {
	case "add-site":
		if err := addSite(); err != nil {
			fmt.Fprintf(os.Stderr, "Error enabling site feature: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Successfully enabled LP/site template! Run 'go test ./...' or 'go run ./cmd/server' to try it.")
	case "remove-site":
		if err := removeSite(); err != nil {
			fmt.Fprintf(os.Stderr, "Error disabling site feature: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Successfully disabled LP/site template! The app is now back to clean base without affecting any custom features.")
	default:
		printUsage()
	}
}

func printUsage() {
	fmt.Println("minimal-web-kit CLI tool")
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Println("  go run ./cmd/kit <command>")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  add-site     Enable LP / homepage template module in cmd/server/features.go")
	fmt.Println("  remove-site  Disable LP / homepage template module without touching custom features")
}

func addSite() error {
	// cmd/server/features.go に登録行を追記
	featuresGoBytes, err := os.ReadFile("cmd/server/features.go")
	if err != nil {
		return fmt.Errorf("read features.go: %w", err)
	}
	content := string(featuresGoBytes)

	if !strings.Contains(content, `"minimal-web-kit/features/site"`) {
		content = strings.Replace(content,
			"// [FEATURE_IMPORTS]",
			"\"minimal-web-kit/features/site\"\n\t// [FEATURE_IMPORTS]",
			1)
	}
	if !strings.Contains(content, "site.NewFeature") {
		content = strings.Replace(content,
			"// [FEATURE_REGISTRATIONS]",
			"r.Register(site.NewFeature(logger))\n\t// [FEATURE_REGISTRATIONS]",
			1)
	}
	if err := os.WriteFile("cmd/server/features.go", []byte(content), 0644); err != nil {
		return fmt.Errorf("write features.go: %w", err)
	}

	return nil
}

func removeSite() error {
	// cmd/server/features.go から site の登録を解除
	featuresGoBytes, err := os.ReadFile("cmd/server/features.go")
	if err != nil {
		return fmt.Errorf("read features.go: %w", err)
	}
	content := string(featuresGoBytes)

	content = strings.Replace(content, "\t\"minimal-web-kit/features/site\"\n", "", 1)
	content = strings.Replace(content, "\tr.Register(site.NewFeature(logger))\n", "", 1)

	if err := os.WriteFile("cmd/server/features.go", []byte(content), 0644); err != nil {
		return fmt.Errorf("write features.go: %w", err)
	}

	return nil
}
