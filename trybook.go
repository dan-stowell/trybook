package main

import (
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
	"bufio" // Added for streaming command output
	"os/exec"
	"os/signal"
	"math/rand"
	"path/filepath"
	"strings"
	"sync" // Already present
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
<body style="padding: 1rem; text-align: left;">
  <div>
    <h1>trybook</h1>
    <form method="GET" action="/">
      <div style="display: flex; gap: 0.5rem;">
        <input type="url" id="repoUrl" name="repo" placeholder="github repo" value="{{.Query}}" autofocus style="flex-grow: 1; font-size: 1.25rem; padding: 0.6rem 0.75rem;">
        <button type="submit" style="font-size: 1.1rem; padding: 0.6rem 1rem;">Open</button>
      </div>
    </form>
    <div id="suggestions" style="margin-top: 0.5rem; text-align: left;"></div>

    {{if .Error}}
    <p style="color: #b00020; font-size: 0.95rem; margin-top: 1rem; white-space: pre-wrap;">Error: {{.Error}}</p>
    {{end}}
    {{if .Result}}
    <p style="color: #0a7; font-size: 0.95rem; margin-top: 1rem; white-space: pre-wrap;">{{.Result}}</p>
    {{end}}
  </div>
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
<body style="padding: 1rem; text-align: left;">
  <div>
    <h1>trybook</h1>
    <p>Repository: <strong><a href="https://github.com/{{.Owner}}/{{.Repo}}" style="color: #007bff;">{{.RepoName}}</a></strong></p>
    <p>Cloned Commit: <code>{{.CommitHash}}</code></p>

    <form method="POST" action="/create-notebook/{{.Owner}}/{{.Repo}}" style="margin-top: 2rem;">
      <button type="submit" style="font-size: 1.1rem; padding: 0.6rem 1rem;">Create Notebook</button>
    </form>

    {{if .Error}}
    <p style="color: #b00020; font-size: 0.95rem; margin-top: 1rem; white-space: pre-wrap;">Error: {{.Error}}</p>
    {{end}}
    <p style="margin-top: 2rem;"><a href="/">Back to search</a></p>
  </div>
</body>
</html>
`

// Task represents a long-running gemini process.
type Task struct {
	mu     sync.RWMutex
	output string // Stores combined stdout/stderr
	status string // "running", "success", "error"
	done   bool   // if true, the task has finished processing (either success or error)
	err    error  // Stores the Go error if task failed
	finalSummary string // Stores the one-time generated final summary
	hasFinalSummary bool // Indicates if finalSummary has been generated
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
<style>
  html, body { margin: 0; padding: 0; }
  body { display: flex; flex-direction: column; min-height: 100vh; }
  .content-wrapper { flex-grow: 1; padding: 1rem; text-align: left; }
  #promptForm { padding: 1rem; background-color: #f0f0f0; border-top: 1px solid #ccc; }
  /* Ensure no extra margins push content away */
  #taskLog { margin-top: 1rem; padding: 0.5rem 1rem; border: 1px solid #ddd; border-radius: 4px; background-color: #fcfcfc; text-align: left; display: none; }
</style>
</head>
<body>
  <div class="content-wrapper">
    <h1><a href="https://github.com/{{.Owner}}/{{.Repo}}" style="color: #007bff;">{{.RepoName}}</a> / {{.NotebookName}}</h1>

    <div id="taskLogContainer"></div>

    <template id="promptLogTemplate">
      <div class="prompt-log-entry" style="margin-top: 1rem; padding: 0.5rem 1rem; border: 1px solid #64B5F6; border-radius: 4px; background-color: #E3F2FD; text-align: left; font-style: italic; color: #3F51B5; word-wrap: break-word;"></div>
    </template>

    <template id="taskLogTemplate">
      <div class="task-log-entry" style="margin-top: 1rem; padding: 0.5rem 1rem; border: 1px solid #ddd; border-radius: 4px; background-color: #fcfcfc; text-align: left; position: relative;">
        <span class="toggle-raw-output" style="position: absolute; left: 0.5rem; top: 0.7rem; cursor: pointer; font-size: 0.8em; color: #666; transition: transform 0.2s; user-select: none; display: none;">▶</span>
        <pre class="output-area" style="white-space: pre-wrap; font-family: monospace; text-align: left; margin: 0; padding-left: 1.2em;"></pre>
        <pre class="raw-output-area" style="white-space: pre-wrap; font-family: monospace; text-align: left; margin: 0; background-color: #eee; padding: 0.5rem; border-radius: 4px; display: none; max-height: 200px; overflow-y: auto;"></pre>
      </div>
    </template>

    {{if .Error}}
    <p style="color: #b00020; font-size: 0.95rem; margin-top: 1rem; white-space: pre-wrap;">Error: {{.Error}}</p>
    {{end}}
  </div>

  <form id="promptForm" method="POST" action="/api/run-prompt/{{.Owner}}/{{.Repo}}/{{.NotebookName}}">
      <div style="display: flex; gap: 0.5rem;">
        <input type="text" id="promptInput" name="prompt" placeholder="question? or tell me to do something" style="flex-grow: 1; font-size: 1.25rem; padding: 0.6rem 0.75rem; box-sizing: border-box;">
        <button type="submit" style="font-size: 1.1rem; padding: 0.6rem 1rem;">run</button>
      </div>
    </form>

    <script>
    (function() {
      const promptInput = document.getElementById('promptInput');
      const promptForm = document.getElementById('promptForm');
      const taskLogContainer = document.getElementById('taskLogContainer');
      const taskLogTemplate = document.getElementById('taskLogTemplate');

      let isSubmitting = false; // Flag to prevent multiple submissions
      // taskId -> {promptLogEntry, taskLogEntry, outputArea, toggleButton, rawOutputArea, pollingIntervalId}
      const activeTasks = {}; 

      function createTaskLogUI(promptText) {
        // Create prompt log entry
        const promptClone = document.importNode(promptLogTemplate.content, true);
        const promptLogEntry = promptClone.querySelector('.prompt-log-entry');
        promptLogEntry.textContent = 'Prompt: "' + promptText + '"';
        taskLogContainer.append(promptLogEntry); // Append prompt box first

        // Create task log entry
        const taskClone = document.importNode(taskLogTemplate.content, true);
        const taskLogEntry = taskClone.querySelector('.task-log-entry');
        const toggleButton = taskLogEntry.querySelector('.toggle-raw-output');
        const outputArea = taskLogEntry.querySelector('.output-area');
        const rawOutputArea = taskLogEntry.querySelector('.raw-output-area');
        taskLogContainer.append(taskLogEntry); // Append task box after prompt box

        return { promptLogEntry, taskLogEntry, outputArea, toggleButton, rawOutputArea };
      }

      function updateOutput(outputAreaElement, output) {
        outputAreaElement.textContent = output;
      }

      function updateRawOutput(rawOutputAreaElement, output) {
        rawOutputAreaElement.textContent = output;
      }

      function setTaskLogStyle(taskLogElement, statusType) {
        switch (statusType) {
          case 'running':
            taskLogElement.style.backgroundColor = '#fff3e0'; // Light orange background
            taskLogElement.style.borderColor = '#ff9800';   // Sharper orange border
            break;
          case 'success':
            taskLogElement.style.backgroundColor = '#e8f5e9'; // Light green background
            taskLogElement.style.borderColor = '#4caf50';   // Sharper green border
            break;
          case 'error':
            taskLogElement.style.backgroundColor = '#ffebee'; // Light red background
            taskLogElement.style.borderColor = '#f44336';   // Sharper red border
            break;
          default: // Default or initial state
            taskLogElement.style.backgroundColor = '#fcfcfc';
            taskLogElement.style.borderColor = '#ddd';
            break;
        }
      }

      function enableForm() {
        promptInput.disabled = false;
        promptForm.querySelector('button[type="submit"]').disabled = false;
        promptInput.focus();
        isSubmitting = false;
      }

      function disableForm() {
        promptInput.disabled = true;
        promptForm.querySelector('button[type="submit"]').disabled = true;
        isSubmitting = true;
      }

      async function pollTask(taskId) {
        const taskUI = activeTasks[taskId];
        if (!taskUI) {
          console.error('UI elements not found for task:', taskId);
          // If pollingIntervalId exists, clear it before deleting
          if (activeTasks[taskId] && activeTasks[taskId].pollingIntervalId) {
            clearInterval(activeTasks[taskId].pollingIntervalId);
          }
          delete activeTasks[taskId];
          return;
        }

        try {
          const response = await fetch('/api/summarize-task/' + taskId);
          const data = await response.json();

          updateRawOutput(taskUI.rawOutputArea, data.output || "");

          if (data.output && data.output.trim() !== "") {
            taskUI.toggleButton.style.display = 'inline-block'; // Show the caret
          } else {
            taskUI.toggleButton.style.display = 'none'; // Hide the caret
          }

          if (!response.ok) {
            console.error('Failed to fetch task summary:', data.error || 'Unknown error');
            updateOutput(taskUI.outputArea, 'Error fetching summary: ' + (data.error || 'Unknown error'));
          } else {
            updateOutput(taskUI.outputArea, data.summary || "No summary available yet.");
          }

          switch (data.status) {
            case 'running':
              setTaskLogStyle(taskUI.taskLogEntry, 'running');
              break;
            case 'success':
              setTaskLogStyle(taskUI.taskLogEntry, 'success');
              clearInterval(taskUI.pollingIntervalId);
              delete activeTasks[taskId]; // Remove from active tasks
              enableForm(); // Re-enable form once this task is done
              break;
            case 'error':
              setTaskLogStyle(taskUI.taskLogEntry, 'error');
              clearInterval(taskUI.pollingIntervalId);
              delete activeTasks[taskId]; // Remove from active tasks
              enableForm(); // Re-enable form once this task is done
              break;
            default: // Fallback for unknown states
              setTaskLogStyle(taskUI.taskLogEntry, 'default');
              updateOutput(taskUI.outputArea, "Unknown task status: " + data.status);
              clearInterval(taskUI.pollingIntervalId);
              delete activeTasks[taskId]; // Remove from active tasks
              enableForm(); // Re-enable form once this task is done
          }

        } catch (error) {
          setTaskLogStyle(taskUI.taskLogEntry, 'error');
          updateOutput(taskUI.outputArea, 'Summarization polling failed: ' + error.message);
          clearInterval(taskUI.pollingIntervalId);
          delete activeTasks[taskId]; // Remove from active tasks
          enableForm(); // Re-enable form once this task is done
        }
      }

      promptForm.addEventListener('submit', async function(event) {
        event.preventDefault();

        if (isSubmitting) {
          return;
        }

        const prompt = promptInput.value.trim();
        if (!prompt) {
          alert("Prompt cannot be empty.");
          return;
        }

        disableForm();

        const newUI = createTaskLogUI(prompt);
        updateOutput(newUI.outputArea, "Starting task...");
        updateRawOutput(newUI.rawOutputArea, "No raw output yet."); // Initialize raw output
        newUI.toggleButton.style.display = 'none'; // Initially hide toggle button
        newUI.toggleButton.style.transform = 'rotate(0deg)'; // Ensure caret starts pointing right
        newUI.rawOutputArea.style.display = 'none'; // Initially hide raw output area
        setTaskLogStyle(newUI.taskLogEntry, 'running');

        // Add event listener for the new toggle button
        newUI.toggleButton.addEventListener('click', function() {
          if (newUI.rawOutputArea.style.display === 'none') {
            newUI.rawOutputArea.style.display = 'block';
            newUI.toggleButton.style.transform = 'rotate(90deg)'; // Point down
            newUI.toggleButton.style.color = '#333'; // Make it a bit darker when open
          } else {
            newUI.rawOutputArea.style.display = 'none';
            newUI.toggleButton.style.transform = 'rotate(0deg)'; // Point right
            newUI.toggleButton.style.color = '#666'; // Back to default color
          }
        });

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
          setTaskLogStyle(newUI.taskLogEntry, 'error');
          updateOutput(newUI.outputArea, 'Error starting task: ' + error.message);
          enableForm();
          // Remove both the prompt and task log entries if the task couldn't even start
          newUI.promptLogEntry.remove();
          newUI.taskLogEntry.remove();
          return;
        }

        if (!taskId) {
          updateOutput(newUI.outputArea, 'Error: Did not receive a task ID from server.');
          enableForm();
          // Remove both entries if no task ID was received
          newUI.promptLogEntry.remove();
          newUI.taskLogEntry.remove(); 
          return;
        }

        activeTasks[taskId] = {
          promptLogEntry: newUI.promptLogEntry,
          taskLogEntry: newUI.taskLogEntry,
          outputArea: newUI.outputArea,
          toggleButton: newUI.toggleButton,
          rawOutputArea: newUI.rawOutputArea,
          pollingIntervalId: null,
        };

        updateOutput(newUI.outputArea, "Task started, waiting for updates...");
        pollTask(taskId);
        activeTasks[taskId].pollingIntervalId = setInterval(() => pollTask(taskId), 1000);

        promptInput.value = ''; // Clear prompt input after submission
      });

      // Initialize state on page load
      enableForm();
    })();
    </script>
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
	Owner        string
	Repo         string
	RepoName     string // owner/repo
	NotebookName string
	WorktreePath string
	BranchName   string
	Error        string
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
	mux.HandleFunc("/api/run-prompt/", apiRunPromptHandler) // POST /api/run-prompt/{owner}/{repo}/{notebook_name}
	mux.HandleFunc("/api/poll-task/", apiPollTaskHandler)   // GET /api/poll-task/{task_id}
	mux.HandleFunc("/api/summarize-task/", apiSummarizeTaskHandler) // GET /api/summarize-task/{task_id}

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

// runGeminiSummary invokes the llm command with a summarization prompt.
// It uses the gpt-5-nano model and asks for a single-sentence summary.
func runGeminiSummary(ctx context.Context, textToSummarize string) (string, error) {
	if textToSummarize == "" {
		return "", nil // Nothing to summarize
	}
	log.Printf("Running llm for summary of text length %d", len(textToSummarize))

	// Define the command to use 'llm' with 'gpt-5-nano' model and a summarization system prompt.
	systemPrompt := `
		This is the output from a coding agent.
		Can you summarize the output in a single sentence?
		The agent may still be thinking or reading files, in which case you can summarize what the agent has thought or done so far.
		The agent may have provided a partial or complete answer to a question, in which case you should summarize that answer and ignore the thinking and tool use.
		Agents may print diagnostic information such as 'Data collection disabled.' Please ignore diagnostic information in your summary.
		If there is nothing worth summarizing, please responding 'Running...' or some other pithy response.
	`
	cmd := exec.CommandContext(ctx, "llm", "--model", "gpt-5-nano", "-s", systemPrompt)

	// Set up stdin for the llm command to pass the textToSummarize
	cmd.Stdin = strings.NewReader(textToSummarize)

	// Pass through OPENAI_API_KEY if it's set in the parent environment.
	// os.Environ() already includes parent environment variables, so we only
	// explicitly add it here if we want to ensure its presence or override it.
	openaiKey := os.Getenv("OPENAI_API_KEY")
	if openaiKey != "" {
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "OPENAI_API_KEY="+openaiKey)
	} else {
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	}

	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("LLM summarization failed: %v\nOutput:\n%s", err, string(out))
		return "", fmt.Errorf("llm summarization failed: %w (output: %s)", err, string(out))
	}
	return strings.TrimSpace(string(out)), nil
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
		output: "",
		status: "running", // Initial status
		done:   false,
	}

	tasksMu.Lock()
	tasks[taskID] = task
	tasksMu.Unlock()

	go executePromptTask(task, worktreePath, prompt, notebookName)

	log.Printf("Started task %s for prompt on %s", taskID, notebookName)
	json.NewEncoder(w).Encode(map[string]string{"taskId": taskID})
}

// executePromptTask runs the Gemini command and captures its output.
func executePromptTask(task *Task, worktreePath, prompt, notebookName string) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	task.mu.Lock()
	task.status = "running" // Ensure status is explicitly set to running
	task.mu.Unlock()

	cmd := exec.CommandContext(ctx, "gemini", "--prompt", prompt)
	cmd.Dir = worktreePath
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		task.mu.Lock()
		task.err = fmt.Errorf("failed to get stdout pipe: %w", err)
		task.status = "error"
		task.done = true
		task.mu.Unlock()
		log.Printf("Gemini command for %s failed to get stdout pipe: %v", notebookName, err)
		return
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		task.mu.Lock()
		task.err = fmt.Errorf("failed to get stderr pipe: %w", err)
		task.status = "error"
		task.done = true
		task.mu.Unlock()
		log.Printf("Gemini command for %s failed to get stderr pipe: %v", notebookName, err)
		return
	}

	if err := cmd.Start(); err != nil {
		task.mu.Lock()
		task.err = fmt.Errorf("failed to start gemini command: %w", err)
		task.status = "error"
		task.done = true
		task.mu.Unlock()
		log.Printf("Gemini command for %s failed to start: %v", notebookName, err)
		return
	}

	var wg sync.WaitGroup
	wg.Add(2) // Two goroutines for stdout and stderr

	// Goroutine to read stdout
	go func() {
		defer wg.Done()
		scanner := bufio.NewScanner(stdoutPipe)
		for scanner.Scan() {
			line := scanner.Text()
			task.mu.Lock()
			task.output += line + "\n" // Append line by line
			task.mu.Unlock()
		}
		if err := scanner.Err(); err != nil {
			log.Printf("Error reading stdout for task %s: %v", notebookName, err)
		}
	}()

	// Goroutine to read stderr
	go func() {
		defer wg.Done()
		scanner := bufio.NewScanner(stderrPipe)
		for scanner.Scan() {
			line := scanner.Text()
			task.mu.Lock()
			task.output += line + "\n" // Append line by line
			task.mu.Unlock()
		}
		if err := scanner.Err(); err != nil {
			log.Printf("Error reading stderr for task %s: %v", notebookName, err)
		}
	}()

	wg.Wait() // Wait for both readers to finish after pipes are closed

	// Wait for the command to exit
	err = cmd.Wait()

	task.mu.Lock()
	defer task.mu.Unlock() // Ensure unlock happens

	task.output = strings.TrimSpace(task.output) // Trim after all output is collected
	task.done = true

	if err != nil {
		task.err = err
		task.status = "error"
		log.Printf("Gemini command for %s finished with error: %v\nOutput:\n%s", notebookName, err, task.output)
	} else {
		task.status = "success"
		log.Printf("Gemini command for %s finished successfully.\nOutput:\n%s", notebookName, task.output)
	}
}

// apiPollTaskHandler returns the current status and output of a task.
func apiPollTaskHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodGet {
		http.Error(w, `{"error": "Method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	parts := strings.Split(r.URL.Path, "/")
	if len(parts) < 4 || parts[2] != "poll-task" {
		http.Error(w, `{"error": "Invalid API URL"}`, http.StatusBadRequest)
		return
	}
	taskID := parts[3]

	tasksMu.RLock()
	task, ok := tasks[taskID]
	tasksMu.RUnlock()

	if !ok {
		http.Error(w, `{"error": "Task not found"}`, http.StatusNotFound)
		return
	}

	task.mu.RLock()
	resp := map[string]interface{}{
		"taskId": taskID,
		"status": task.status,
		"output": task.output,
		"done":   task.done,
	}
	if task.err != nil {
		resp["error"] = task.err.Error()
	}
	task.mu.RUnlock()

	json.NewEncoder(w).Encode(resp)
}

// apiSummarizeTaskHandler returns a summary of a task's status and output.
func apiSummarizeTaskHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodGet {
		http.Error(w, `{"error": "Method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	parts := strings.Split(r.URL.Path, "/")
	if len(parts) < 4 || parts[2] != "summarize-task" {
		http.Error(w, `{"error": "Invalid API URL"}`, http.StatusBadRequest)
		return
	}
	taskID := parts[3]

	tasksMu.RLock()
	task, ok := tasks[taskID]
	tasksMu.RUnlock()

	if !ok {
		http.Error(w, `{"error": "Task not found"}`, http.StatusNotFound)
		return
	}

	task.mu.RLock()
	currentStatus := task.status
	currentOutput := task.output
	taskErr := task.err
	taskDone := task.done
	cachedFinalSummary := task.finalSummary
	cachedHasFinalSummary := task.hasFinalSummary
	task.mu.RUnlock()

	var summary string
	if cachedHasFinalSummary {
		summary = cachedFinalSummary // Use cached summary if already generated
	} else if taskDone { // Task is done, but final summary not yet generated
		if currentOutput != "" {
			ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
			defer cancel()
			s, err := runGeminiSummary(ctx, currentOutput)
			if err != nil {
				log.Printf("Failed to generate final summary for task %s: %v", taskID, err)
				summary = "Could not generate final summary."
			} else {
				summary = s
				// Cache the generated summary for future requests
				task.mu.Lock()
				task.finalSummary = summary
				task.hasFinalSummary = true
				task.mu.Unlock()
			}
		} else {
			summary = "No output available for final summary."
		}
	} else { // Task is still running, generate a real-time summary
		if currentOutput != "" {
			ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second) // Give LLM some time
			defer cancel()
			s, err := runGeminiSummary(ctx, currentOutput)
			if err != nil {
				log.Printf("Failed to generate running summary for task %s: %v", taskID, err)
				summary = "Could not generate summary." // Fallback summary
			} else {
				summary = s
			}
		} else {
			summary = "No output available yet."
		}
	}

    // Determine overall message based on status for a single sentence summary
    var statusMessage string
    switch currentStatus {
    case "running":
        statusMessage = "Task is currently running."
    case "success":
        statusMessage = "Task completed successfully."
    case "error":
        statusMessage = "Task exited with an error."
        if taskErr != nil {
            statusMessage = fmt.Sprintf("Task exited with an error: %v", taskErr.Error())
        }
    default:
        statusMessage = "Task status is unknown."
    }

	resp := map[string]interface{}{
		"taskId": taskID,
		"status": currentStatus,
		"statusMessage": statusMessage,
		"summary": summary,
		"output": currentOutput, // Add raw output to the response
	}
	if taskErr != nil {
		resp["error"] = taskErr.Error()
	}

	json.NewEncoder(w).Encode(resp)
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

