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
	ProjectName         string
	GitBranch           string
	GitCommit           string
	GitHubURL           string // New field for GitHub commit URL
	SuggestedBuildCommand string
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
				githubURL = fmt.Sprintf("https://github.com/%s", repoPath)
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
    {{if .GitHubURL}}
        <h1><a href="{{.GitHubURL}}" target="_blank">{{.ProjectName}}</a></h1>
    {{else}}
        <h1>{{.ProjectName}}</h1>
    {{end}}
    <p><i>{{.GitBranch}} ({{.GitCommit}})</i></p>
    {{if .SuggestedBuildCommand}}
        <button onclick="copyToClipboard('{{.SuggestedBuildCommand}}')" style="font-size: 2em; padding: 20px 40px; cursor: pointer;">
            {{.SuggestedBuildCommand}}
        </button>
    {{else}}
        <p>No suggested build command found.</p>
    {{end}}

    <script>
        function copyToClipboard(text) {
            navigator.clipboard.writeText(text).then(function() {
                alert('Copied to clipboard: ' + text);
            }, function(err) {
                console.error('Could not copy text: ', err);
            });
        }
    </script>
</body>
</html>
`

// buildFilePatterns maps build system names to a list of their common file patterns and a suggested build command.
var buildFilePatterns = map[string]struct {
	Files   []string
	Command string
}{
	"Node.js":       {Files: []string{"package.json"}, Command: "npm install && npm run build"},
	"Python":        {Files: []string{"pyproject.toml", "setup.py", "setup.cfg", "requirements.txt", "poetry.lock", "hatch.toml"}, Command: "pip install -r requirements.txt && python setup.py install"},
	"Bazel":         {Files: []string{"MODULE.bazel", "WORKSPACE.bazel", "WORKSPACE", ".bazelrc"}, Command: "bazel build //..."},
	"Go":            {Files: []string{"go.mod"}, Command: "go build ./..."},
	"Rust":          {Files: []string{"Cargo.toml"}, Command: "cargo build"},
	"Gradle (Java)": {Files: []string{"gradlew", "build.gradle", "build.gradle.kts"}, Command: "./gradlew build"},
	"Maven (Java)":  {Files: []string{"pom.xml"}, Command: "mvn clean install"},
	"CMake":         {Files: []string{"CMakeLists.txt"}, Command: "cmake . && make"},
	"Autotools":     {Files: []string{"configure.ac", "configure.in", "Makefile.am"}, Command: "./configure && make"},
	"Docker":        {Files: []string{"Dockerfile", "docker/Dockerfile"}, Command: "docker build . -t my-image"},
	"Make":          {Files: []string{"Makefile", "makefile", "GNUmakefile"}, Command: "make"},
	"Pants":         {Files: []string{"pants.toml", "pants.ini"}, Command: "pants package ::"},
	"Buck":          {Files: []string{".buckconfig"}, Command: "buck build //..."},
	"Ninja":         {Files: []string{"build.ninja"}, Command: "ninja"},
	"Meson":         {Files: []string{"meson.build"}, Command: "meson setup build && ninja -C build"},
	"SCons":         {Files: []string{"SConstruct", "sconstruct"}, Command: "scons"},
	"Swift":         {Files: []string{"Package.swift"}, Command: "swift build"},
	"Zig":           {Files: []string{"build.zig"}, Command: "zig build"},
	"Haskell":       {Files: []string{"stack.yaml"}, Command: "stack build"},
	".NET":          {Files: []string{".sln", ".csproj"}, Command: "dotnet build"},
	"Nix":           {Files: []string{"flake.nix", "default.nix"}, Command: "nix build"},
	"Just":          {Files: []string{"Justfile"}, Command: "just"},
	"Task":          {Files: []string{"Taskfile.yml"}, Command: "task build"},
}

// getSuggestedBuildCommand checks for the presence of build files and returns a suggested command.
func getSuggestedBuildCommand(rootDir string) string {
	files, err := os.ReadDir(rootDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading current directory for build command suggestion: %v\n", err)
		return ""
	}

	fileNames := make(map[string]bool)
	for _, file := range files {
		if !file.IsDir() {
			fileNames[file.Name()] = true
		}
	}

	for system, info := range buildFilePatterns {
		for _, pattern := range info.Files {
			if strings.HasPrefix(pattern, "*.") {
				suffix := strings.TrimPrefix(pattern, "*")
				for fileName := range fileNames {
					if strings.HasSuffix(fileName, suffix) {
						return fmt.Sprintf("build (%s): %s", system, info.Command)
					}
				}
			} else if fileNames[pattern] {
				return fmt.Sprintf("build (%s): %s", system, info.Command)
			}
		}
	}
	return ""
}

func main() {
	rootDir, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error getting current working directory: %v\n", err)
		os.Exit(1)
	}

	projectName, gitBranch, gitCommit, githubURL := getGitInfo()
	suggestedBuildCommand := getSuggestedBuildCommand(rootDir)

	tmpl, err := template.New("bldmenu").Parse(htmlTemplate)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing HTML template: %v\n", err)
		os.Exit(1)
	}

	data := ProjectInfo{
		ProjectName:         projectName,
		GitBranch:           gitBranch,
		GitCommit:           gitCommit,
		GitHubURL:           githubURL,
		SuggestedBuildCommand: suggestedBuildCommand,
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
