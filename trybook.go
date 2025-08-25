package main

import (
	"database/sql"
	"flag" // Added flag import
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"os/exec" // For running git commands
	"path/filepath"
	"strings" // For string manipulation
	"time"    // For timestamp in branch name

	_ "modernc.org/sqlite" // Import for SQLite driver
)

const htmlContent = `
<!DOCTYPE html>
<html>
<head>
    <title>trybook: {{.Title}}</title>
    <style>
        body {
            font-family: sans-serif;
            margin: 0;
            display: flex;
            flex-direction: column;
            min-height: 100vh;
        }
        h1 {
            margin: 0 2em;
            padding-top: 1em;
        }
        #content {
            flex-grow: 1;
            padding: 0 2em;
            overflow-y: auto; /* In case content overflows */
        }
        #inputForm {
            display: flex;
            padding: 1em 2em;
            border-top: 1px solid #eee;
            background-color: #f9f9f9;
        }
        input[type="text"] {
            flex-grow: 1;
            padding: 0.8em; /* Increased padding */
            margin-right: 1em;
            border: 1px solid #ccc;
            border-radius: 4px;
            font-size: 1.1em; /* Larger font size */
        }
        button {
            padding: 0.8em 1.5em; /* Increased padding */
            background-color: #007bff;
            color: white;
            border: none;
            border-radius: 4px;
            cursor: pointer;
            font-size: 1.1em; /* Larger font size */
        }
        button:hover {
            background-color: #0056b3;
        }
        #output {
            background-color: #f4f4f4;
            padding: 1em;
            border-radius: 4px;
            white-space: pre-wrap;
            word-break: break-all;
            margin-top: 1em;
        }
    </style>
</head>
<body>
    <h1>trybook: {{.RepoName}}</h1>
    <div id="content">
        <pre id="output"></pre>

        <h2>Worktrees</h2>
        {{if .Worktrees}}
        <ul>
            {{range .Worktrees}}
            <li>
                <strong>Path:</strong> {{.Path}}<br>
                <strong>Branch:</strong> {{.Branch}}<br>
                <strong>HEAD:</strong> {{.HEAD}}
            </li>
            {{end}}
        </ul>
        {{else}}
        <p>No worktrees found for this repository.</p>
        {{end}}
    </div>
    <form id="inputForm">
        <input type="text" id="commandInput" placeholder="Enter command" autofocus>
        <button type="submit">try</button>
    </form>

    <script>
        document.getElementById('inputForm').addEventListener('submit', async function(event) {
            event.preventDefault();
            const command = document.getElementById('commandInput').value;
            const outputElement = document.getElementById('output');
            outputElement.textContent = 'trying...';
            document.getElementById('commandInput').value = ''; // Clear input after submission

            try {
                const response = await fetch('/run', {
                    method: 'POST',
                    headers: {
                        'Content-Type': 'application/x-www-form-urlencoded',
                    },
                    body: 'command=' + encodeURIComponent(command)
                });
                const result = await response.text();
                outputElement.textContent = result;
            } catch (error) {
                outputElement.textContent = 'Error: ' + error.message;
            }
        });
    </script>
</body>
</html>
`

type Worktree struct {
	Path   string
	HEAD   string
	Branch string
}

type PageData struct {
	Title     string
	RepoName  string
	Worktrees []Worktree
}

func handler(w http.ResponseWriter, r *http.Request, repoName string, repoPath string) {
	tmpl, err := template.New("index").Parse(htmlContent)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Get worktree list
	cmd := exec.Command("git", "worktree", "list", "--porcelain")
	cmd.Dir = repoPath
	output, err := cmd.Output()
	if err != nil {
		log.Printf("Failed to list worktrees: %v", err)
		// Don't error out, just don't show worktrees
	}

	var worktrees []Worktree
	lines := strings.Split(string(output), "\n")
	currentWorktree := Worktree{}
	for _, line := range lines {
		if strings.HasPrefix(line, "worktree ") {
			if currentWorktree.Path != "" {
				worktrees = append(worktrees, currentWorktree)
			}
			currentWorktree = Worktree{Path: strings.TrimPrefix(line, "worktree ")}
		} else if strings.HasPrefix(line, "HEAD ") {
			currentWorktree.HEAD = strings.TrimPrefix(line, "HEAD ")
		} else if strings.HasPrefix(line, "branch ") {
			currentWorktree.Branch = strings.TrimPrefix(line, "branch refs/heads/")
		}
	}
	if currentWorktree.Path != "" {
		worktrees = append(worktrees, currentWorktree)
	}

	data := PageData{
		Title:     "trybook: " + repoName,
		RepoName:  repoName,
		Worktrees: worktrees,
	}
	tmpl.Execute(w, data)
}

func notebookHandler(w http.ResponseWriter, r *http.Request) {
	branchName := strings.TrimPrefix(r.URL.Path, "/notebook/")
	if branchName == "" {
		http.Error(w, "Branch name not provided", http.StatusBadRequest)
		return
	}

	tmpl, err := template.New("notebook").Parse(htmlContent)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	data := PageData{
		Title:    branchName,
		RepoName: "", // Not relevant for notebook page title
	}
	tmpl.Execute(w, data)
}

func runHandler(w http.ResponseWriter, r *http.Request, db *sql.DB, repoPath string, tryDir string) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	command := r.FormValue("command")

	// Get current commit hash
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = repoPath
	commitHashBytes, err := cmd.Output()
	if err != nil {
		log.Printf("Failed to get current commit hash: %v", err)
		http.Error(w, "Internal server error: failed to get commit hash", http.StatusInternalServerError)
		return
	}
	commitHash := strings.TrimSpace(string(commitHashBytes))

	// Generate branch and worktree name
	repoBaseName := filepath.Base(repoPath)
	timestamp := time.Now().Format("2006-01-02")
	// Generate ideaSlug using llm
	llmCmd := exec.Command("llm", "--model", "openrouter/openai/gpt-oss-20b", "--system", "Please print a good 3-word branch name for this prompt. Separate words with hyphens. Print only the branch name.")
	llmCmd.Stdin = strings.NewReader(command)
	llmCmd.Env = os.Environ() // Inherit existing environment variables
	llmCmd.Env = append(llmCmd.Env, "OPENROUTER_API_KEY="+os.Getenv("OPENROUTER_API_KEY"))
	llmCmd.Env = append(llmCmd.Env, "OPENROUTER_KEY="+os.Getenv("OPENROUTER_KEY"))

	llmOutputBytes, err := llmCmd.Output()
	if err != nil {
		log.Printf("Failed to get branch name from llm: %v", err)
		http.Error(w, "Internal server error: failed to generate branch name", http.StatusInternalServerError)
		return
	}
	ideaSlug := strings.TrimSpace(string(llmOutputBytes))
	// Sanitize ideaSlug to be a valid branch name part (e.g., replace spaces with hyphens)
	ideaSlug = strings.ReplaceAll(ideaSlug, " ", "-")
	ideaSlug = strings.ReplaceAll(ideaSlug, "_", "-")
	ideaSlug = strings.ToLower(ideaSlug) // Ensure lowercase

	branchName := fmt.Sprintf("%s-%s-%s", repoBaseName, timestamp, ideaSlug)
	worktreePath := filepath.Join(tryDir, "worktree", branchName)

	// Create new branch and worktree
	cmd = exec.Command("git", "worktree", "add", "-b", branchName, worktreePath, "HEAD")
	cmd.Dir = repoPath
	output, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("Failed to create worktree and branch: %v\nOutput: %s", err, output)
		http.Error(w, fmt.Sprintf("Internal server error: failed to create worktree/branch. Output: %s", output), http.StatusInternalServerError)
		return
	}

	// Store the command, repo info, and branch name in the database
	insertSQL := `INSERT INTO commands(command, repo_path, commit_hash, branch_name) VALUES(?, ?, ?, ?)`
	_, err = db.Exec(insertSQL, command, repoPath, commitHash, branchName)
	if err != nil {
		log.Printf("Failed to insert command into DB: %v", err)
		http.Error(w, "Internal server error: failed to store command", http.StatusInternalServerError)
		return
	}

	fmt.Fprintf(w, "You entered: %s\nNew branch '%s' and worktree created at '%s'.\n(Command stored in DB. Execution not yet implemented)", command, branchName, worktreePath)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func main() {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		log.Fatalf("Error getting user home directory: %v", err)
	}
	defaultTryDir := filepath.Join(homeDir, ".trybook")

	tryDirFlag := flag.String("trydir", defaultTryDir, "Directory to store trybook data (SQLite DB)")
	repoFlag := flag.String("repo", ".", "Path to the local git repository")
	flag.Parse()

	repoName := filepath.Base(*repoFlag)
	log.Printf("Using repository: %s", *repoFlag)

	// Ensure the directory exists
	if err := os.MkdirAll(*tryDirFlag, 0755); err != nil {
		log.Fatalf("Failed to create directory %s: %v", *tryDirFlag, err)
	}

	dbPath := filepath.Join(*tryDirFlag, "trybook.db")
	log.Printf("Using SQLite database at: %s", dbPath)

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		log.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	// Create 'commands' table if it doesn't exist
	createTableSQL := `
	CREATE TABLE IF NOT EXISTS commands (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		command TEXT NOT NULL,
		repo_path TEXT NOT NULL,
		commit_hash TEXT NOT NULL,
		branch_name TEXT NOT NULL,
		timestamp DATETIME DEFAULT CURRENT_TIMESTAMP
	);`
	_, err = db.Exec(createTableSQL)
	if err != nil {
		log.Fatalf("Failed to create table: %v", err)
	}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		handler(w, r, repoName, *repoFlag)
	})
	http.HandleFunc("/notebook/", notebookHandler) // New route for notebooks
	http.HandleFunc("/run", func(w http.ResponseWriter, r *http.Request) {
		runHandler(w, r, db, *repoFlag, *tryDirFlag)
	})

	port := ":8080"
	log.Printf("Server starting on port %s\n", port)
	log.Fatal(http.ListenAndServe(port, nil))
}
