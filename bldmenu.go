package main

import (
	"fmt"
	"html/template"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// ProjectInfo holds all the data to be displayed on the web page.
type ProjectInfo struct {
	ProjectName string
	GitBranch   string
	GitCommit   string
	GitHubURL   string // New field for GitHub commit URL
	BuildFiles  map[string][]string
}

// getGitInfo retrieves the project name, current Git branch, commit hash, and GitHub URL.
func getGitInfo() (projectName, branch, commit, githubURL string) {
	// Get project name from current directory
	wd, err := os.Getwd()
	if err == nil {
		projectName = filepath.Base(wd)
	}

	// Get Git branch
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	output, err := cmd.Output()
	if err == nil {
		branch = strings.TrimSpace(string(output))
	}

	// Get Git commit
	cmd = exec.Command("git", "rev-parse", "HEAD")
	output, err = cmd.Output()
	if err == nil {
		commit = strings.TrimSpace(string(output))
	}
	// Get Git remote origin URL using 'git remote get-url origin'
	cmd = exec.Command("git", "remote", "get-url", "origin")
	output, err = cmd.Output()
	if err == nil {
		remoteURL := strings.TrimSpace(string(output))
		// Normalize URL to HTTPS for easier parsing
		if strings.HasPrefix(remoteURL, "git@github.com:") {
			remoteURL = "https://github.com/" + strings.TrimPrefix(remoteURL, "git@github.com:")
		}
		// Extract owner/repo from the URL and construct the GitHub commit URL
		if strings.Contains(remoteURL, "github.com/") {
			// Find the part after "github.com/"
			parts := strings.SplitN(remoteURL, "github.com/", 2)
			if len(parts) > 1 {
				repoPath := parts[1]
				// Remove .git suffix if present
				repoPath = strings.TrimSuffix(repoPath, ".git")
				githubURL = fmt.Sprintf("https://github.com/%s/commit/%s", repoPath, commit)
			}
		}
	}
	return
}

// openBrowser tries to open the URL in a default web browser.
func openBrowser(url string) {
	var err error
	switch runtime.GOOS {
	case "linux":
		err = exec.Command("xdg-open", url).Start()
	case "windows":
		err = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		err = exec.Command("open", url).Start()
	default:
		err = fmt.Errorf("unsupported platform")
	}
	if err != nil {
		fmt.Printf("Error opening browser: %v\nPlease open %s manually.\n", err, url)
	}
}

// htmlTemplate is the HTML content for the web page.
const htmlTemplate = `
<!DOCTYPE html>
<html>
<head>
    <title>{{.ProjectName}} Build Info</title>
    <style>
        body { font-family: sans-serif; margin: 2em; }
        h1 { color: #333; }
        ul { list-style-type: none; padding: 0; }
        li { margin-bottom: 0.5em; }
        .category { font-weight: bold; margin-top: 1em; }
    </style>
</head>
<body>
    <h1>{{.ProjectName}}</h1>
    <p>
    {{if .GitHubURL}}
        <a href="{{.GitHubURL}}" target="_blank">{{.GitBranch}} ({{.GitCommit}})</a>
    {{else}}
        {{.GitBranch}} ({{.GitCommit}})
    {{end}}
    </p>
    <h2>Detected Build Files:</h2>
    {{if .BuildFiles}}
        <ul>
            {{range $system, $paths := .BuildFiles}}
                <li class="category">{{$system}}:</li>
                <ul>
                    {{range $path := $paths}}
                        <li>{{$path}}</li>
                    {{end}}
                </ul>
            {{end}}
        </ul>
    {{else}}
        <p>No common build files found in the current directory.</p>
    {{end}}
</body>
</html>
`

// buildFilePatterns maps build system names to a list of their common file patterns.
var buildFilePatterns = map[string][]string{
	"Node.js":   {"package.json"}, // yarn.lock, pnpm-lock.yaml can also indicate, but package.json is primary
	"Python":    {"pyproject.toml", "setup.py", "setup.cfg", "requirements.txt", "poetry.lock", "hatch.toml"},
	"Bazel":     {"MODULE.bazel", "WORKSPACE.bazel", "WORKSPACE", ".bazelrc"},
	"Go":        {"go.mod"},
	"Rust":      {"Cargo.toml"},
	"Gradle (Java)": {"gradlew", "build.gradle", "build.gradle.kts"},
	"Maven (Java)": {"pom.xml"},
	"CMake":     {"CMakeLists.txt"},
	"Autotools": {"configure.ac", "configure.in", "Makefile.am"},
	"Docker":    {"Dockerfile", "docker/Dockerfile"},
	"Make":      {"Makefile", "makefile", "GNUmakefile"},
	"Pants":     {"pants.toml", "pants.ini"},
	"Buck":      {".buckconfig"},
	"Ninja":     {"build.ninja"},
	"Meson":     {"meson.build"},
	"SCons":     {"SConstruct", "sconstruct"},
	"Swift":     {"Package.swift"},
	"Zig":       {"build.zig"},
	"Haskell":   {"stack.yaml"}, // *.cabal can also indicate
	".NET":      {".sln", ".csproj"},
	"Nix":       {"flake.nix", "default.nix"},
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

	projectName, gitBranch, gitCommit, githubURL := getGitInfo()

	tmpl, err := template.New("bldmenu").Parse(htmlTemplate)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing HTML template: %v\n", err)
		os.Exit(1)
	}

	data := ProjectInfo{
		ProjectName: projectName,
		GitBranch:   gitBranch,
		GitCommit:   gitCommit,
		GitHubURL:   githubURL,
		BuildFiles:  foundFiles,
	}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		err := tmpl.Execute(w, data)
		if err != nil {
			http.Error(w, "Error rendering template", http.StatusInternalServerError)
			fmt.Fprintf(os.Stderr, "Error executing template: %v\n", err)
		}
	})

	port := "8080"
	url := "http://localhost:" + port
	fmt.Printf("Starting server on %s\n", url)

	openBrowser(url)

	err = http.ListenAndServe(":"+port, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error starting server: %v\n", err)
		os.Exit(1)
	}
}
