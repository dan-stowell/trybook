package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// buildFilePatterns maps build system names to a list of their common file patterns.
var buildFilePatterns = map[string][]string{
	"Bazel":     {"MODULE.bazel", "WORKSPACE.bazel", "WORKSPACE", ".bazelrc"},
	"Pants":     {"pants.toml", "pants.ini"},
	"Buck":      {".buckconfig"},
	"CMake":     {"CMakeLists.txt"},
	"Ninja":     {"build.ninja"},
	"Meson":     {"meson.build"},
	"Autotools": {"configure.ac", "configure.in", "Makefile.am"},
	"Make":      {"Makefile", "makefile", "GNUmakefile"},
	"SCons":     {"SConstruct", "sconstruct"},
	"Maven (Java)": {"pom.xml"},
	"Gradle (Java)": {"gradlew", "build.gradle", "build.gradle.kts"},
	"Node.js":   {"package.json"}, // yarn.lock, pnpm-lock.yaml can also indicate, but package.json is primary
	"Rust":      {"Cargo.toml"},
	"Go":        {"go.mod"},
	"Python":    {"pyproject.toml", "setup.py", "setup.cfg", "requirements.txt", "poetry.lock", "hatch.toml"},
	"Swift":     {"Package.swift"},
	"Zig":       {"build.zig"},
	"Haskell":   {"stack.yaml"}, // *.cabal can also indicate
	".NET":      {".sln", ".csproj"},
	"Nix":       {"flake.nix", "default.nix"},
	"Docker":    {"Dockerfile", "docker/Dockerfile"},
	"Just":      {"Justfile"},
	"Task":      {"Taskfile.yml"},
}

func main() {
	rootDir, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error getting current working directory: %v\n", err)
		os.Exit(1)
	}

	foundFiles := make(map[string][]string)

	err = filepath.Walk(rootDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			// If there's an error accessing a path, just skip it and continue
			return nil
		}

		if info.IsDir() {
			// Skip common version control and build output directories
			if info.Name() == ".git" || info.Name() == "node_modules" || info.Name() == "target" || info.Name() == "build" || info.Name() == "dist" {
				return filepath.SkipDir
			}
			return nil
		}

		fileName := info.Name()
		for system, patterns := range buildFilePatterns {
			for _, pattern := range patterns {
				if strings.HasPrefix(pattern, "*.") { // Handle wildcard extensions like *.cabal, *.sln, *.csproj
					suffix := strings.TrimPrefix(pattern, "*")
					if strings.HasSuffix(fileName, suffix) {
						relPath, _ := filepath.Rel(rootDir, path)
						foundFiles[system] = append(foundFiles[system], relPath)
						break // Found a match for this system, move to next system
					}
				} else if fileName == pattern {
					relPath, _ := filepath.Rel(rootDir, path)
					foundFiles[system] = append(foundFiles[system], relPath)
					break // Found a match for this system, move to next system
				}
			}
		}
		return nil
	})

	if err != nil {
		fmt.Fprintf(os.Stderr, "Error walking the directory: %v\n", err)
		os.Exit(1)
	}

	if len(foundFiles) == 0 {
		fmt.Println("No common build files found.")
		return
	}

	for system, paths := range foundFiles {
		for _, p := range paths {
			fmt.Printf("%s %s\n", p, system)
		}
	}
}
