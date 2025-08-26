package main

import (
	"fmt"
	"os"
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

	files, err := os.ReadDir(rootDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading current directory: %v\n", err)
		os.Exit(1)
	}

	for _, file := range files {
		if file.IsDir() {
			continue
		}

		fileName := file.Name()
		for system, patterns := range buildFilePatterns {
			for _, pattern := range patterns {
				if strings.HasPrefix(pattern, "*.") { // Handle wildcard extensions like *.cabal, *.sln, *.csproj
					suffix := strings.TrimPrefix(pattern, "*")
					if strings.HasSuffix(fileName, suffix) {
						foundFiles[system] = append(foundFiles[system], fileName)
						break // Found a match for this system, move to next system
					}
				} else if fileName == pattern {
					foundFiles[system] = append(foundFiles[system], fileName)
					break // Found a match for this system, move to next system
				}
			}
		}
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
