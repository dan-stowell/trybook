package main

import (
	"flag"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// PageData holds the data for the HTML template.
type PageData struct {
	RepoName  string
	BranchName string
}

// getRepoName returns the name of the Git repository.
func getRepoName(dir string) string {
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel")
	output, err := cmd.Output()
	if err != nil {
		return "Unknown Repo"
	}
	repoPath := strings.TrimSpace(string(output))
	return filepath.Base(repoPath)
}

// getBranchName returns the current Git branch name.
func getBranchName(dir string) string {
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--abbrev-ref", "HEAD")
	output, err := cmd.Output()
	if err != nil {
		return "Unknown Branch"
	}
	return strings.TrimSpace(string(output))
}

// indexHandler serves the main HTML page.
func indexHandler(w http.ResponseWriter, r *http.Request, dir string) {
	data := PageData{
		RepoName:  getRepoName(dir),
		BranchName: getBranchName(dir),
	}

	tmpl := template.Must(template.New("index").Parse(`
<!DOCTYPE html>
<html>
<head>
    <title>Sesh</title>
    <style>
        body { font-family: sans-serif; margin: 20px; }
        h1 { margin-bottom: 20px; }
        input[type="text"] {
            width: 100%;
            padding: 10px;
            font-size: 1.2em;
            box-sizing: border-box; /* Include padding in width */
        }
    </style>
</head>
<body>
    <h1>{{.RepoName}} ({{.BranchName}})</h1>
    <input type="text" placeholder="Type your command here...">
</body>
</html>
`))
	tmpl.Execute(w, data)
}

func main() {
	dir := flag.String("dir", ".", "Directory to check (default is current directory)")
	flag.Parse()

	// Check if the directory is a Git project
	gitPath := filepath.Join(*dir, ".git")
	if _, err := os.Stat(gitPath); os.IsNotExist(err) {
		fmt.Printf("Error: Directory '%s' is not a Git project.\n", *dir)
		os.Exit(1)
	}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		indexHandler(w, r, *dir)
	})

	port := ":8080"
	fmt.Printf("Server starting on port %s\n", port)
	log.Fatal(http.ListenAndServe(port, nil))
}
