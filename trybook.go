package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"os/user"
	"os/exec"
	"os/signal"
	"math/rand"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// r is a global random number generator for generating unique names.
var r *rand.Rand

func init() {
	r = rand.New(rand.NewSource(time.Now().UnixNano()))
}

const indexHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>trybook</title>
<style>
  #suggestions { max-width: 40rem; margin: 0.5rem auto 0; text-align: left; }
  .sugg-item { padding: 0.4rem 0.5rem; border: 1px solid #ddd; border-top: none; cursor: pointer; background: #fff; }
  .sugg-item:first-child { border-top: 1px solid #ddd; border-top-left-radius: 6px; border-top-right-radius: 6px; }
  .sugg-item:last-child { border-bottom-left-radius: 6px; border-bottom-right-radius: 6px; }
  .sugg-item:hover { background: #f7f7f7; }
  .sugg-title { font-weight: 600; }
  .sugg-desc { color: #555; font-size: 0.9rem; }
</style>
</head>
<body style="text-align:center;">
  <h1>trybook</h1>
  <form method="GET" action="/">
    <div style="display: flex; max-width: 40rem; margin: 0 auto; gap: 0.5rem;">
      <input type="url" id="repoUrl" name="repo" placeholder="github repo" value="{{.Query}}" autofocus style="flex-grow: 1; font-size: 1.25rem; padding: 0.6rem 0.75rem;">
      <button type="submit" style="font-size: 1.1rem; padding: 0.6rem 1rem;">Open</button>
    </div>
  </form>
  <div id="suggestions"></div>

  {{if .Error}}
  <p style="color: #b00020; font-size: 0.95rem; margin-top: 1rem; white-space: pre-wrap;">Error: {{.Error}}</p>
  {{end}}
  {{if .Result}}
  <p style="color: #0a7; font-size: 0.95rem; margin-top: 1rem; white-space: pre-wrap;">{{.Result}}</p>
  {{end}}
<script>
(function(){
  const input = document.getElementById('repoUrl');
  const box = document.getElementById('suggestions');
  let controller = null;
  let debounceTimer = null;

  function clearBox() { box.innerHTML = ''; }

  function render(items) {
    if (!items || items.length === 0) { clearBox(); return; }
    box.innerHTML = items.map(function(it) {
      var desc = it.description ? it.description : '';
      var escFullName = it.fullName.replace(/&/g,'&amp;').replace(/</g,'&lt;');
      var escDesc = desc.replace(/&/g,'&amp;').replace(/</g,'&lt;');
      return '<div class="sugg-item" data-repo="' + escFullName + '">' +
               '<div class="sugg-title">' + escFullName + '</div>' +
               '<div class="sugg-desc">' + escDesc + '</div>' +
             '</div>';
    }).join('');
    Array.prototype.forEach.call(box.querySelectorAll('.sugg-item'), function(el) {
      el.addEventListener('click', function() {
        const repoFullName = el.getAttribute('data-repo');
        window.location.href = '/repo/' + repoFullName;
      });
    });
  }

  async function search(q){
    if (controller) controller.abort();
    controller = new AbortController();
    try{
      const resp = await fetch('/api/search?query=' + encodeURIComponent(q), { signal: controller.signal });
      if (!resp.ok) { clearBox(); return; }
      const data = await resp.json();
      render(Array.isArray(data) ? data.slice(0,5) : []);
    } catch(e) {
      if (!(e && e.name === 'AbortError')) clearBox();
    }
  }

  input.addEventListener('input', function() {
    const q = input.value.trim();
    if (q.length < 2) { clearBox(); if (controller) controller.abort(); return; }
    clearTimeout(debounceTimer);
    debounceTimer = setTimeout(function(){ search(q); }, 250);
  });

  document.addEventListener('click', function(e) {
    if (!box.contains(e.target) && e.target !== input) {
      clearBox();
    }
  });
})();
</script>
</body>
</html>
`

const repoHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>trybook - {{.RepoName}}</title>
</head>
<body style="text-align:center;">
  <h1>trybook</h1>
  <p>Repository: <strong><a href="https://github.com/{{.Owner}}/{{.Repo}}">{{.RepoName}}</a></strong></p>
  <p>Cloned Commit: <code>{{.CommitHash}}</code></p>

  <form method="POST" action="/create-notebook/{{.Owner}}/{{.Repo}}" style="margin-top: 2rem;">
    <button type="submit" style="font-size: 1.1rem; padding: 0.6rem 1rem;">Create Notebook</button>
  </form>

  {{if .Error}}
  <p style="color: #b00020; font-size: 0.95rem; margin-top: 1rem; white-space: pre-wrap;">Error: {{.Error}}</p>
  {{end}}
  <p style="margin-top: 2rem;"><a href="/">Back to search</a></p>
</body>
</html>
`

// Task represents a long-running gemini process.
type Task struct {
	mu     sync.RWMutex
	// output stores complete JSON messages, one per line.
	output []string
	done   bool
	err    error
}

var (
	tasks   = make(map[string]*Task)
	tasksMu sync.RWMutex
)

// generateTaskID creates a unique ID for a task.
func generateTaskID() string {
	return fmt.Sprintf("%x", r.Int63())
}

const notebookHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>trybook - {{.NotebookName}}</title>
</head>
<body>
  <div style="max-width: 60rem; margin: 0 auto; padding: 1rem; text-align: left;">
    <h1>{{.NotebookName}}</h1>
    <div style="margin-bottom: 1.5rem; padding: 0.5rem 1rem; background-color: #f8f8f8; border: 1px solid #eee; border-radius: 4px; font-size: 0.9rem;">
      <p style="margin: 0.2rem 0;">Repository: <strong><a href="https://github.com/{{.Owner}}/{{.Repo}}">{{.RepoName}}</a></strong></p>
      <p style="margin: 0.2rem 0;">Branch: <code>{{.BranchName}}</code></p>
      <p style="margin: 0.2rem 0;">Worktree Path: <code>{{.WorktreePath}}</code></p>
    </div>

    <form id="promptForm" method="POST" action="/api/run-prompt/{{.Owner}}/{{.Repo}}/{{.NotebookName}}" style="margin-top: 2rem;">
      <div style="display: flex; gap: 0.5rem;">
        <input type="text" id="promptInput" name="prompt" placeholder="question? or tell me to do something" style="flex-grow: 1; font-size: 1.25rem; padding: 0.6rem 0.75rem; box-sizing: border-box;">
        <button type="submit" style="font-size: 1.1rem; padding: 0.6rem 1rem;">run</button>
      </div>
    </form>

    <div id="statusMessage" style="margin-top: 1rem; color: #555;">{{.ThinkingMessage}}</div>
    <div id="summaryOutput" style="margin-top: 1rem; padding: 0.5rem 1rem; background-color: #e6ffe6; border: 1px solid #ccffcc; border-radius: 4px; display: {{if .Summary}}block{{else}}none{{end}};">
        <strong>Summary:</strong> {{.Summary}}
    </div>

    <script>
    (function() {
      const promptInput = document.getElementById('promptInput');
      const promptForm = document.getElementById('promptForm');
      const statusMessage = document.getElementById('statusMessage');
      const summaryOutput = document.getElementById('summaryOutput');
      let isSubmitting = false; // Flag to prevent multiple submissions

      function showStatus(message, isError = false) {
        statusMessage.textContent = message;
        statusMessage.style.color = isError ? '#b00020' : '#555';
      }

      function showSummary(summary) {
        summaryOutput.querySelector('strong').nextSibling.textContent = ' ' + summary;
        summaryOutput.style.display = 'block';
      }

      function clearSummary() {
        summaryOutput.querySelector('strong').nextSibling.textContent = '';
        summaryOutput.style.display = 'none';
      }

      promptForm.addEventListener('submit', async function(event) {
        event.preventDefault(); // Prevent default form submission

        if (isSubmitting) {
          return; // Prevent multiple submissions
        }

        const prompt = promptInput.value.trim();
        if (!prompt) {
          showStatus("Prompt cannot be empty.", true);
          return;
        }

        isSubmitting = true; // Set flag
        clearSummary();
        showStatus("Gemini is thinking...", false);
        promptInput.disabled = true;
        promptForm.querySelector('button[type="submit"]').disabled = true;

        let taskId;
        try {
          const response = await fetch(promptForm.action, {
            method: 'POST',
            headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
            body: new URLSearchParams({ prompt: prompt }).toString(),
          });
          const data = await response.json();
          if (!response.ok) {
            throw new Error(data.error || 'Failed to start task');
          }
          taskId = data.taskId;
        } catch (error) {
          showStatus('Error starting task: ' + error.message, true);
          isSubmitting = false;
          promptInput.disabled = false;
          promptForm.querySelector('button[type="submit"]').disabled = false;
          promptInput.focus();
          return;
        }

        if (!taskId) {
          showStatus('Error: Did not receive a task ID from server.', true);
          isSubmitting = false;
          promptInput.disabled = false;
          promptForm.querySelector('button[type="submit"]').disabled = false;
          promptInput.focus();
          return;
        }

        const eventSource = new EventSource('/api/stream-updates/' + taskId);

        eventSource.onmessage = function(event) {
          const data = JSON.parse(event.data);

          if (data.error) {
            showStatus('Error: ' + data.error, true);
            eventSource.close();
            isSubmitting = false;
            promptInput.disabled = false;
            promptForm.querySelector('button[type="submit"]').disabled = false;
            promptInput.focus();
            return;
          }

          if (data.summary) {
            showSummary(data.summary);
          }

          if (data.done) {
            showStatus("Gemini is done.", false);
            eventSource.close();
            isSubmitting = false;
            promptInput.disabled = false;
            promptForm.querySelector('button[type="submit"]').disabled = false;
            promptInput.focus();
          }
        };

        eventSource.onerror = function(e) {
          showStatus('Request failed: connection error to server.', true);
          eventSource.close();
          isSubmitting = false;
          promptInput.disabled = false;
          promptForm.querySelector('button[type="submit"]').disabled = false;
          promptInput.focus();
        };
      });
    })();
    </script>

    {{if .Error}}
    <p style="color: #b00020; font-size: 0.95rem; margin-top: 1rem; white-space: pre-wrap;">Error: {{.Error}}</p>
    {{end}}
    <p style="margin-top: 2rem;"><a href="/repo/{{.Owner}}/{{.Repo}}">Back to repository</a> | <a href="/">Back to search</a></p>
  </div>
</body>
</html>
`

var (
	indexTmpl   = template.Must(template.New("index").Parse(indexHTML))
	repoTmpl    = template.Must(template.New("repo").Parse(repoHTML))
	notebookTmpl = template.Must(template.New("notebook").Parse(notebookHTML))
	workDir     string
)

type IndexData struct {
	Query  string
	Result string
	Error  string
}

type RepoPageData struct {
	Owner      string
	Repo       string
	RepoName   string // owner/repo
	CommitHash string
	Error      string
}

type NotebookPageData struct {
	Owner         string
	Repo          string
	RepoName      string // owner/repo
	NotebookName  string
	WorktreePath  string
	BranchName    string
	Summary       string // The last generated summary
	ThinkingMessage string // Message to display while processing
	Error         string
}

func defaultWorkDir() string {
	usr, err := user.Current()
	if err != nil {
		log.Fatalf("could not get current user: %v", err)
	}
	return filepath.Join(usr.HomeDir, ".trybook")
}

func main() {
	flag.StringVar(&workDir, "workdir", defaultWorkDir(), "working directory for repo clones")
	flag.Parse()

	mux := http.NewServeMux()
	mux.HandleFunc("/", indexHandler)
	mux.HandleFunc("/api/search", apiSearchHandler)
	mux.HandleFunc("/repo/", repoHandler)                 // Handle /repo/{owner}/{repo}
	mux.HandleFunc("/create-notebook/", createNotebookHandler) // POST /create-notebook/{owner}/{repo}
	mux.HandleFunc("/notebook/", notebookHandler)         // GET /notebook/{owner}/{repo}/{notebook_name}
	mux.HandleFunc("/api/run-prompt/", apiRunPromptHandler)      // POST /api/run-prompt/{owner}/{repo}/{notebook_name}
	mux.HandleFunc("/api/stream-updates/", apiStreamUpdatesHandler) // GET /api/stream-updates/{task_id}

	addr := "127.0.0.1:8080"

	srv := &http.Server{
		Addr:              addr,
		Handler:           logRequest(mux),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		log.Printf("trybook listening on http://%s", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server error: %v", err)
		}
	}()

	// Graceful shutdown on SIGINT/SIGTERM
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("graceful shutdown failed: %v", err)
	}
	log.Println("trybook stopped")
}

func indexHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	data := IndexData{
		Query: r.URL.Query().Get("repo"),
	}

	if err := indexTmpl.Execute(w, data); err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}

// runCommandInWorktree executes a command within the specified worktree directory.
// It returns stdout, stderr, and any error.
func runCommandInWorktree(ctx context.Context, worktreePath, name string, arg ...string) (string, string, error) {
	cmd := exec.CommandContext(ctx, name, arg...)
	cmd.Dir = worktreePath
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0") // Avoid interactive prompts

	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		return stdout.String(), stderr.String(), fmt.Errorf("command %q failed: %w (stdout: %s, stderr: %s)", name, err, stdout.String(), stderr.String())
	}
	return stdout.String(), stderr.String(), nil
}

// runGemini invokes the gemini command with the given prompt in the worktree.
func runGemini(ctx context.Context, worktreePath, prompt string) (stdout, stderr string, err error) {
	log.Printf("Running gemini for worktree %s with prompt: %s", worktreePath, prompt)
	return runCommandInWorktree(ctx, worktreePath, "gemini", "--prompt", prompt)
}

// runGeminiStreaming starts the gemini command and returns pipes to its stdout and stderr.
func runGeminiStreaming(ctx context.Context, worktreePath, prompt string) (*exec.Cmd, io.ReadCloser, io.ReadCloser, error) {
	log.Printf("Streaming gemini for worktree %s with prompt: %s", worktreePath, prompt)
	cmd := exec.CommandContext(ctx, "gemini", "--prompt", prompt)
	cmd.Dir = worktreePath
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("getting stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("getting stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, nil, nil, fmt.Errorf("starting gemini command: %w", err)
	}

	return cmd, stdout, stderr, nil
}

// summarizeOutput uses llm to summarize the given output.
func summarizeOutput(ctx context.Context, output string) (string, error) {
	summaryPrompt := fmt.Sprintf("Please summarize this output in a single sentence: %s", output)
	log.Printf("Running llm to summarize output (first 100 chars): %q...", output[:min(100, len(output))])
	stdout, stderr, err := runCommandInWorktree(ctx, "", "llm", "--model", "gpt-5-nano", summaryPrompt)
	if err != nil {
		return "", fmt.Errorf("llm summarization failed: %w (stderr: %s)", err, stderr)
	}
	return strings.TrimSpace(stdout), nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// apiRunPromptHandler starts a long-running Gemini task.
func apiRunPromptHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		http.Error(w, `{"error": "Method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	parts := strings.Split(r.URL.Path, "/")
	if len(parts) < 6 || parts[1] != "api" || parts[2] != "run-prompt" {
		http.Error(w, `{"error": "Invalid API URL"}`, http.StatusBadRequest)
		return
	}
	owner, repo, notebookName := parts[3], parts[4], parts[5]

	prompt := r.FormValue("prompt")
	if prompt == "" {
		http.Error(w, `{"error": "Prompt cannot be empty"}`, http.StatusBadRequest)
		return
	}

	worktreePath := filepath.Join(workDir, "worktree", owner, repo, notebookName)
	if _, err := os.Stat(worktreePath); os.IsNotExist(err) {
		log.Printf("Worktree path does not exist: %s", worktreePath)
		http.Error(w, `{"error": "Worktree not found"}`, http.StatusNotFound)
		return
	}

	taskID := generateTaskID()
	task := &Task{
		output: make([]string, 0),
	}

	tasksMu.Lock()
	tasks[taskID] = task
	tasksMu.Unlock()

	go executePromptTask(task, worktreePath, prompt, notebookName)

	log.Printf("Started task %s for prompt on %s", taskID, notebookName)
	json.NewEncoder(w).Encode(map[string]string{"taskId": taskID})
}

// executePromptTask runs the Gemini command and periodic summarization for a task.
func executePromptTask(task *Task, worktreePath, prompt, notebookName string) {
	defer func() {
		task.mu.Lock()
		task.done = true
		task.mu.Unlock()
		log.Printf("Task for %s finished.", notebookName)
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cmd, stdoutPipe, stderrPipe, err := runGeminiStreaming(ctx, worktreePath, prompt)
	if err != nil {
		log.Printf("Starting Gemini command failed for %s: %v", notebookName, err)
		jsonData, _ := json.Marshal(map[string]interface{}{"error": "Failed to start gemini"})
		task.mu.Lock()
		task.output = append(task.output, string(jsonData))
		task.err = err
		task.mu.Unlock()
		return
	}

	var combinedOutputMu sync.Mutex
	var combinedOutput strings.Builder

	go func() {
		scanner := bufio.NewScanner(io.MultiReader(stdoutPipe, stderrPipe))
		for scanner.Scan() {
			combinedOutputMu.Lock()
			combinedOutput.WriteString(scanner.Text() + "\n")
			combinedOutputMu.Unlock()
		}
	}()

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	doneChan := make(chan error, 1)
	go func() { doneChan <- cmd.Wait() }()

	for {
		select {
		case err := <-doneChan:
			combinedOutputMu.Lock()
			output := combinedOutput.String()
			combinedOutputMu.Unlock()

			summary, sErr := summarizeOutput(context.Background(), output)
			if sErr != nil {
				log.Printf("LLM final summarization failed for %s: %v", notebookName, sErr)
				jsonData, _ := json.Marshal(map[string]interface{}{"error": "Final summarization failed."})
				task.mu.Lock()
				task.output = append(task.output, string(jsonData))
				task.mu.Unlock()
			} else {
				log.Printf("Final summary for %s: %s", notebookName, summary)
				jsonData, _ := json.Marshal(map[string]interface{}{"summary": summary, "done": true})
				task.mu.Lock()
				task.output = append(task.output, string(jsonData))
				task.mu.Unlock()
			}

			if err != nil {
				log.Printf("Gemini command for %s finished with error: %v", notebookName, err)
				task.mu.Lock()
				task.err = err
				task.mu.Unlock()
			} else {
				log.Printf("Gemini command for %s finished successfully.", notebookName)
			}
			return
		case <-ticker.C:
			combinedOutputMu.Lock()
			output := combinedOutput.String()
			combinedOutputMu.Unlock()

			if len(output) > 0 {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				summary, err := summarizeOutput(ctx, output)
				cancel()
				if err != nil {
					log.Printf("Periodic summarization failed for %s: %v", notebookName, err)
				} else {
					log.Printf("Periodic summary for %s: %s", notebookName, summary)
					jsonData, _ := json.Marshal(map[string]interface{}{"summary": summary})
					task.mu.Lock()
					task.output = append(task.output, string(jsonData))
					task.mu.Unlock()
				}
			}
		}
	}
}

// apiStreamUpdatesHandler streams updates for a task using SSE.
func apiStreamUpdatesHandler(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(r.URL.Path, "/")
	if len(parts) < 4 || parts[2] != "stream-updates" {
		http.Error(w, "Invalid stream URL", http.StatusBadRequest)
		return
	}
	taskID := parts[3]

	tasksMu.RLock()
	task, ok := tasks[taskID]
	tasksMu.RUnlock()

	if !ok {
		http.Error(w, "Task not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	sentLines := 0
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	log.Printf("Client connected to stream for task %s", taskID)

	for {
		select {
		case <-ticker.C:
			task.mu.RLock()
			outputLines := task.output
			isDone := task.done
			task.mu.RUnlock()

			if len(outputLines) > sentLines {
				for i := sentLines; i < len(outputLines); i++ {
					fmt.Fprintf(w, "data: %s\n\n", outputLines[i])
				}
				flusher.Flush()
				sentLines = len(outputLines)
			}

			if isDone {
				log.Printf("Task %s is done, closing stream.", taskID)
				// Note: we don't clean up the task here, maybe add a TTL later.
				return
			}
		case <-r.Context().Done():
			log.Printf("Client disconnected from task %s", taskID)
			return
		}
	}
}


// getHeadCommit returns the SHA of the HEAD commit in the given repo directory.
func getHeadCommit(ctx context.Context, repoDir string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "HEAD")
	cmd.Dir = repoDir
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get HEAD commit: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func manageRepo(ctx context.Context, input string) (string, string, error) { // Added string for commit hash
	owner, repo, err := parseGitHubInput(input)
	if err != nil {
		return "", "", err
	}

	repoDir := filepath.Join(workDir, "clone", owner, repo)
	sshURL := "ssh://git@github.com/" + owner + "/" + repo

	// Timeout the git operation to avoid hanging connections.
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	var cmd *exec.Cmd
	var operation string
	var opStart time.Time

	_, err = os.Stat(repoDir)
	if err == nil { // Directory exists, perform pull
		operation = "git pull"
		log.Printf("Starting git pull for %s in %s", sshURL, repoDir)
		opStart = time.Now()
		cmd = exec.CommandContext(ctx, "git", "pull")
		cmd.Dir = repoDir // Set working directory for pull
	} else if os.IsNotExist(err) { // Directory does not exist, perform clone
		operation = "git clone"
		log.Printf("Starting git clone of %s into %s", sshURL, repoDir)
		opStart = time.Now()
		if err := os.MkdirAll(repoDir, 0o755); err != nil {
			return "", "", fmt.Errorf("create repo directory %q: %w", repoDir, err)
		}
		cmd = exec.CommandContext(ctx, "git", "clone", "--depth=1", "--single-branch", sshURL, repoDir)
	} else {
		return "", "", fmt.Errorf("stat %q: %w", repoDir, err)
	}

	// Avoid interactive prompts in server context.
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("Failed %s for %s after %s: %v\n%s", operation, sshURL, time.Since(opStart), err, string(out))
		return "", "", fmt.Errorf("%s failed: %v\n%s", operation, err, string(out))
	}
	log.Printf("Completed %s for %s in %s", operation, sshURL, time.Since(opStart))

	// Get the HEAD commit hash after successful operation
	commitHash, err := getHeadCommit(ctx, repoDir)
	if err != nil {
		return repoDir, "", fmt.Errorf("could not get HEAD commit after %s: %w", operation, err)
	}
	return repoDir, commitHash, nil
}

func repoHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	// Expecting URL path like /repo/{owner}/{repo}
	parts := strings.Split(r.URL.Path, "/")
	if len(parts) < 4 || parts[1] != "repo" {
		http.Error(w, "Invalid repository URL", http.StatusBadRequest)
		return
	}
	owner := parts[2]
	repo := parts[3]
	repoFullName := owner + "/" + repo

	data := RepoPageData{
		Owner:    owner,
		Repo:     repo,
		RepoName: repoFullName,
	}

	repoDir, commitHash, err := manageRepo(r.Context(), repoFullName)
	if err != nil {
		data.Error = err.Error()
		log.Printf("Error managing repo %s in %s: %v", repoFullName, repoDir, err)
	} else {
		data.CommitHash = commitHash
		log.Printf("Successfully managed repo %s, commit %s in %s", repoFullName, commitHash, repoDir)
	}

	if err := repoTmpl.Execute(w, data); err != nil {
		log.Printf("Template execution error for repo page: %v", err)
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}

// generateNotebookName creates a unique name for a notebook/worktree/branch.
// Format: REPO-DATE-RANDOM_6_CHARS
func generateNotebookName(repoFullName string) string {
	date := time.Now().Format("20060102")
	randomPart := fmt.Sprintf("%x", r.Int63n(1<<24)) // 6 hex characters
	return fmt.Sprintf("%s-%s-%s", strings.ReplaceAll(repoFullName, "/", "-"), date, randomPart)
}

// createWorktree adds a new git worktree for a given base repository.
// It returns the path to the new worktree and any error.
func createWorktree(ctx context.Context, baseRepoDir, owner, repo, notebookName, branchName string) (string, error) {
	worktreePath := filepath.Join(workDir, "worktree", owner, repo, notebookName)

	log.Printf("Starting git worktree add for %s on branch %s at %s", notebookName, branchName, worktreePath)
	opStart := time.Now()

	// git worktree add -b <branch_name> <worktree_path>
	cmd := exec.CommandContext(ctx, "git", "worktree", "add", "-b", branchName, worktreePath)
	cmd.Dir = baseRepoDir // Execute command from the base cloned repository
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")

	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("Failed git worktree add for %s after %s: %v\n%s", notebookName, time.Since(opStart), err, string(out))
		return "", fmt.Errorf("git worktree add failed for %s: %v\n%s", notebookName, err, string(out))
	}
	log.Printf("Completed git worktree add for %s in %s", notebookName, time.Since(opStart))
	return worktreePath, nil
}

func createNotebookHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Expecting URL path like /create-notebook/{owner}/{repo}
	parts := strings.Split(r.URL.Path, "/")
	if len(parts) < 4 || parts[1] != "create-notebook" {
		http.Error(w, "Invalid URL for creating notebook", http.StatusBadRequest)
		return
	}
	owner := parts[2]
	repo := parts[3]
	repoFullName := owner + "/" + repo

	// First, ensure the base repository is cloned/pulled
	baseRepoDir, _, err := manageRepo(r.Context(), repoFullName)
	if err != nil {
		log.Printf("Error ensuring base repo for notebook creation %s: %v", repoFullName, err)
		http.Error(w, fmt.Sprintf("Error preparing base repository: %v", err), http.StatusInternalServerError)
		return
	}

	notebookName := generateNotebookName(repoFullName)
	branchName := notebookName // Use notebook name as branch name

	worktreePath, err := createWorktree(r.Context(), baseRepoDir, owner, repo, notebookName, branchName)
	if err != nil {
		log.Printf("Error creating worktree for %s/%s: %v", repoFullName, notebookName, err)
		http.Error(w, fmt.Sprintf("Error creating notebook worktree: %v", err), http.StatusInternalServerError)
		return
	}

	log.Printf("Successfully created notebook %s (branch %s) at %s", notebookName, branchName, worktreePath)
	http.Redirect(w, r, fmt.Sprintf("/notebook/%s/%s/%s", owner, repo, notebookName), http.StatusSeeOther)
}

func notebookHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	// Expecting URL path like /notebook/{owner}/{repo}/{notebook_name}
	parts := strings.Split(r.URL.Path, "/")
	if len(parts) < 5 || parts[1] != "notebook" {
		http.Error(w, "Invalid notebook URL", http.StatusBadRequest)
		return
	}
	owner := parts[2]
	repo := parts[3]
	notebookName := parts[4]
	repoFullName := owner + "/" + repo

	worktreePath := filepath.Join(workDir, "worktree", owner, repo, notebookName)

	data := NotebookPageData{
		Owner:        owner,
		Repo:         repo,
		RepoName:     repoFullName,
		NotebookName: notebookName,
		WorktreePath: worktreePath,
		BranchName:   notebookName, // The branch name is the same as the notebook name
	}

	// Verify the worktree directory actually exists
	_, err := os.Stat(worktreePath)
	if os.IsNotExist(err) {
		data.Error = fmt.Sprintf("Notebook worktree not found at %s", worktreePath)
		log.Printf("Notebook worktree not found: %s", worktreePath)
	} else if err != nil {
		data.Error = fmt.Sprintf("Error accessing worktree path %s: %v", worktreePath, err)
		log.Printf("Error accessing worktree path %s: %v", worktreePath, err)
	}

	if err := notebookTmpl.Execute(w, data); err != nil {
		log.Printf("Template execution error for notebook page: %v", err)
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}

func parseGitHubInput(s string) (string, string, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, ".git")
	s = strings.TrimSuffix(s, "/")

	if s == "" {
		return "", "", fmt.Errorf("empty repo")
	}

	switch {
	case strings.HasPrefix(s, "https://github.com/"):
		s = strings.TrimPrefix(s, "https://github.com/")
	case strings.HasPrefix(s, "http://github.com/"):
		s = strings.TrimPrefix(s, "http://github.com/")
	case strings.HasPrefix(s, "ssh://git@github.com/"):
		s = strings.TrimPrefix(s, "ssh://git@github.com/")
	case strings.HasPrefix(s, "git@github.com:"):
		s = strings.TrimPrefix(s, "git@github.com:")
	case strings.HasPrefix(s, "github.com/"):
		s = strings.TrimPrefix(s, "github.com/")
	}

	parts := strings.Split(s, "/")
	if len(parts) < 2 {
		return "", "", fmt.Errorf("invalid GitHub repo; expected owner/repo")
	}
	owner := parts[0]
	repo := parts[1]
	if owner == "" || repo == "" {
		return "", "", fmt.Errorf("invalid GitHub repo; expected owner/repo")
	}
	return owner, repo, nil
}


type Repo struct {
	FullName       string `json:"fullName"`
	Description    string `json:"description"`
	URL            string `json:"url"`
	StargazersCount int    `json:"stargazersCount"`
}

func apiSearchHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "method not allowed"})
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("query"))
	if q == "" || len(q) < 2 {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("[]"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	results, err := searchRepos(ctx, q)
	if err != nil {
		log.Printf("search error for %q: %v", q, err)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("[]"))
		return
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(results)
}

func searchRepos(ctx context.Context, q string) ([]Repo, error) {
	start := time.Now()
	cmd := exec.CommandContext(ctx, "gh", "search", "repos", q, "--limit", "5", "--json", "fullName,description,url,stargazersCount")
	cmd.Env = append(os.Environ(),
		"GH_NO_UPDATE_NOTIFIER=1",
		"GIT_TERMINAL_PROMPT=0",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			if ctxErr == context.DeadlineExceeded {
				duration := time.Since(start)
				return nil, fmt.Errorf("gh search repos timed out after %s: %w", duration, ctxErr)
			}
			return nil, fmt.Errorf("gh search repos failed due to context cancellation (%s): %w", ctxErr, err)
		}
		return nil, fmt.Errorf("gh search repos failed: %v\n%s", err, string(out))
	}
	var repos []Repo
	if err := json.Unmarshal(out, &repos); err != nil {
		return nil, fmt.Errorf("parse gh json: %w", err)
	}
	if len(repos) > 5 {
		repos = repos[:5]
	}
	return repos, nil
}

func logRequest(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start))
	})
}

