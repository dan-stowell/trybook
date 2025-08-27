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
	"runtime"
	"bufio"
	"io"
	"strings"
	"sync"
)

// PageData holds the data for the HTML template.
type PageData struct {
	RepoName   string
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
		RepoName:   getRepoName(dir),
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
        .command-output {
            background-color: #f0f0f0;
            border: 1px solid #ccc;
            padding: 10px;
            margin-top: 5px;
            margin-bottom: 10px;
            min-height: 60px; /* Minimum height for output */
            font-family: monospace;
            white-space: pre-wrap; /* Preserve whitespace and wrap text */
            overflow-y: auto; /* Enable scrolling for overflow */
            max-height: 200px; /* Example max height, adjust as needed */
        }
        .output-line {
            padding: 2px 0;
        }
        .command-entry {
            margin-bottom: 15px;
        }
    </style>
</head>
<body>
    <h1>{{.RepoName}} ({{.BranchName}})</h1>
    <div id="session-log">
        <div class="command-entry">
            <input type="text" class="sesh-input" placeholder="Type your command here..." autofocus>
            <div class="command-output"></div>
        </div>
    </div>

    <script>
        document.addEventListener('DOMContentLoaded', function() {
            const sessionLog = document.getElementById('session-log');
            let currentEventSource = null; // To hold the SSE connection for the active command

            function setupInput(inputElement) {
                inputElement.addEventListener('keydown', function(event) {
                    if (event.key === 'Enter') {
                        event.preventDefault();
                        const currentInput = event.target;
                        const inputValue = currentInput.value.trim();

                        if (inputValue === '') {
                            return; // Don't process empty commands
                        }

                        // Make current input read-only
                        currentInput.setAttribute('readonly', true);
                        currentInput.blur();

                        // Get the output container for this specific command entry
                        const currentCommandEntry = currentInput.closest('.command-entry');
                        const outputContainer = currentCommandEntry.querySelector('.command-output');
                        outputContainer.innerHTML = ''; // Clear previous output for this entry

                        // Close existing EventSource if any
                        if (currentEventSource) {
                            currentEventSource.close();
                        }

                        // Send command to backend and open SSE for output
                        currentEventSource = new EventSource("/execute?cmd=" + encodeURIComponent(inputValue));
                        currentEventSource.onmessage = function(event) {
                            const lineDiv = document.createElement('div');
                            lineDiv.className = 'output-line';
                            lineDiv.textContent = event.data;
                            outputContainer.appendChild(lineDiv); // Append to display all lines
                        };
                        currentEventSource.onerror = function(err) {
                            console.error('EventSource failed:', err);
                            // Only add a "Command failed" message if it's not a graceful close
                            if (currentEventSource.readyState !== EventSource.CLOSED) {
                                const lineDiv = document.createElement('div');
                                lineDiv.className = 'output-line';
                                lineDiv.textContent = "EventSource connection failed.";
                                outputContainer.appendChild(lineDiv);
                            }
                            currentEventSource.close();
                            currentEventSource = null; // Clear the reference
                        };

                        // Listen for the custom 'end' event from the server
                        currentEventSource.addEventListener('end', function(event) {
                            const lineDiv = document.createElement('div');
                            lineDiv.className = 'output-line';
                            lineDiv.textContent = event.data; // This will be "Command finished successfully." or "Command exited with error: ..."
                            outputContainer.appendChild(lineDiv);
                            currentEventSource.close();
                            currentEventSource = null;
                        });

                        // Create a new command entry (input + output div)
                        const newCommandEntry = document.createElement('div');
                        newCommandEntry.className = 'command-entry';

                        const newInput = document.createElement('input');
                        newInput.type = 'text';
                        newInput.className = 'sesh-input';
                        newInput.placeholder = 'Type your command here...';

                        const newOutputDiv = document.createElement('div');
                        newOutputDiv.className = 'command-output';

                        newCommandEntry.appendChild(newInput);
                        newCommandEntry.appendChild(newOutputDiv);

                        // Append the new command entry to the session log
                        sessionLog.appendChild(newCommandEntry);

                        // Focus on the new input field
                        newInput.focus();
                        setupInput(newInput); // Setup event listener for the new input
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

// executeHandler handles command execution and streams output via SSE.
func executeHandler(w http.ResponseWriter, r *http.Request, dir string) {
	cmdStr := r.URL.Query().Get("cmd")
	if cmdStr == "" {
		http.Error(w, "Command not provided", http.StatusBadRequest)
		return
	}

	// Set up SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*") // Allow CORS for development

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported!", http.StatusInternalServerError)
		return
	}

	parts := strings.Fields(cmdStr)
	if len(parts) == 0 {
		return // Should be caught by cmdStr == "" check, but good for safety
	}

	cmd := exec.Command(parts[0], parts[1:]...)
	cmd.Dir = dir // Set the working directory for the command

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		log.Printf("Error creating stdout pipe: %v", err)
		fmt.Fprintf(w, "data: Error: %v\n\n", err)
		flusher.Flush()
		return
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		log.Printf("Error creating stderr pipe: %v", err)
		fmt.Fprintf(w, "data: Error: %v\n\n", err)
		flusher.Flush()
		return
	}

	if err := cmd.Start(); err != nil {
		log.Printf("Error starting command: %v", err)
		fmt.Fprintf(w, "data: Error: %v\n\n", err)
		flusher.Flush()
		return
	}

	var wg sync.WaitGroup
	wg.Add(2)

	// Function to read from a pipe and send as SSE
	streamOutput := func(reader io.Reader) {
		defer wg.Done()
		scanner := bufio.NewScanner(reader)
		for scanner.Scan() {
			line := scanner.Text()
			fmt.Fprintf(w, "data: %s\n\n", line)
			flusher.Flush()
		}
		if err := scanner.Err(); err != nil {
			log.Printf("Error reading from pipe: %v", err)
		}
	}

	go streamOutput(stdout)
	go streamOutput(stderr)

	wg.Wait() // Wait for both stdout and stderr to finish

	if err := cmd.Wait(); err != nil {
		log.Printf("Command finished with error: %v", err)
		fmt.Fprintf(w, "event: end\ndata: Command exited with error: %v\n\n", err)
	} else {
		fmt.Fprintf(w, "event: end\ndata: Command finished successfully.\n\n")
	}
	flusher.Flush()
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
	http.HandleFunc("/execute", func(w http.ResponseWriter, r *http.Request) {
		executeHandler(w, r, *dir)
	})

	port := ":8080"
	url := "http://localhost" + port
	fmt.Printf("Server starting on %s\n", url)

	// Open the URL in the default browser
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", url)
	default: // Linux and other Unix-like systems
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		log.Printf("Error opening browser: %v", err)
	}

	log.Fatal(http.ListenAndServe(port, nil))
}
