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
    <div id="input-container">
        <input type="text" class="sesh-input" placeholder="Type your command here..." autofocus>
    </div>

    <script>
        document.addEventListener('DOMContentLoaded', function() {
            function setupInput(inputElement) {
                inputElement.addEventListener('keydown', function(event) {
                    if (event.key === 'Enter') {
                        event.preventDefault(); // Prevent default form submission
                        const currentInput = event.target;
                        const inputValue = currentInput.value;

                        // Make current input read-only
                        currentInput.setAttribute('readonly', true);
                        currentInput.blur(); // Remove focus from the current input

                        // Echo the input value
                        const echoDiv = document.createElement('div');
                        echoDiv.textContent = '> ' + inputValue;
                        currentInput.parentNode.insertBefore(echoDiv, currentInput.nextSibling);

                        // Create a new input field
                        const newInput = document.createElement('input');
                        newInput.type = 'text';
                        newInput.className = 'sesh-input';
                        newInput.placeholder = 'Type your command here...';

                        // Append the new input field
                        const inputContainer = document.getElementById('input-container');
                        inputContainer.appendChild(newInput);

                        // Focus on the new input field
                        newInput.focus();
                    }
                });
            }

            // Setup the initial input field
            const initialInput = document.querySelector('.sesh-input');
            if (initialInput) {
                setupInput(initialInput);
            }
        });
    </script>
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
