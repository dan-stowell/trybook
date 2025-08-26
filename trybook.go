package main

import (
	"bufio" // Added for streaming command output
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"log"
	"math/rand"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
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
  .notebook-list { margin-top: 1rem; max-width: 40rem; }
  .notebook-list h2 { font-size: 1.2rem; margin-bottom: 0.5rem; }
  .notebook-list ul { list-style: none; padding: 0; }
  .notebook-list li { margin-bottom: 0.25rem; }
  .notebook-list a { text-decoration: none; color: #007bff; }
  .notebook-list a:hover { text-decoration: underline; }
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

    <div class="notebook-list">
      <h2>Existing Notebooks</h2>
      {{if .Notebooks}}
      <ul>
        {{range .Notebooks}}
        <li><a href="/notebook/{{.Owner}}/{{.Repo}}/{{.Name}}">{{.RepoName}} / {{.Name}}</a></li>
        {{end}}
      </ul>
      {{else}}
      <p>No notebooks found.</p>
      {{end}}
    </div>
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

// LLMResponse holds the output, status, and summary for a single LLM execution.
type LLMResponse struct {
	mu         sync.RWMutex // Protects fields of this LLMResponse
	Output     string       // Stores combined stdout/stderr
	Status     string       // "running", "success", "error"
	Done       bool         // if true, the LLM has finished processing (either success or error)
	Err        error        // Stores the Go error if LLM failed
	Summary    string       // Stores the one-time generated summary for this LLM
	HasSummary bool         // Indicates if Summary has been generated
}

// PromptExecution represents the overall execution of a user prompt, involving multiple LLMs.
type PromptExecution struct {
	mu sync.RWMutex // Protects fields of PromptExecution itself, e.g., overall completion or shared data
	// Note: individual LLMResponse fields have their own mutexes.
	Claude    LLMResponse
	BazelQuery LLMResponse // New field for Bazel query output
	BazelTest  LLMResponse // New field for Bazel test output
}

var (
	// promptExecutions maps a prompt execution ID to its PromptExecution struct.
	promptExecutions   = make(map[string]*PromptExecution)
	promptExecutionsMu sync.RWMutex
)

// generatePromptExecutionID creates a unique ID for a prompt execution.
func generatePromptExecutionID() string {
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

    <template id="llmResponseTemplate">
      <div class="llm-response-entry" style="margin-top: 1rem; padding: 0.5rem 1rem; border: 1px solid #ddd; border-radius: 4px; background-color: #fcfcfc; text-align: left; position: relative;">
        <div style="position: absolute; bottom: 0.5rem; right: 0.5rem; font-size: 0.75em; color: #888; background-color: rgba(255, 255, 255, 0.7); padding: 0.2em 0.5em; border-radius: 3px;" class="llm-title"></div>
        <pre class="output-area" style="white-space: pre-wrap; font-family: monospace; text-align: left; margin: 0; padding-left: 0em;"></pre>
        <pre class="raw-output-area" style="white-space: pre-wrap; font-family: monospace; text-align: left; margin: 0; background-color: #eee; padding: 0.5rem; border-radius: 4px; display: none; max-height: 200px; overflow-y: auto;"></pre>
      </div>
    </template>

    <template id="bazelResponseTemplate">
      <div class="bazel-response-entry" style="margin-top: 1rem; padding: 0.5rem 1rem; border: 1px solid #C5CAE9; border-radius: 4px; background-color: #E8EAF6; text-align: left; position: relative;">
        <div style="position: absolute; bottom: 0.5rem; right: 0.5rem; font-size: 0.75em; color: #5C6BC0; background-color: rgba(255, 255, 255, 0.7); padding: 0.2em 0.5em; border-radius: 3px;" class="bazel-title"></div>
        <pre class="output-area" style="white-space: pre-wrap; font-family: monospace; text-align: left; margin: 0; padding-left: 0em;"></pre>
        <pre class="raw-output-area" style="white-space: pre-wrap; font-family: monospace; text-align: left; margin: 0; background-color: #e0e0e0; padding: 0.5rem; border-radius: 4px; display: none; max-height: 200px; overflow-y: auto;"></pre>
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
      const llmResponseTemplate = document.getElementById('llmResponseTemplate');
      const bazelResponseTemplate = document.getElementById('bazelResponseTemplate');

      let isSubmitting = false; // Flag to prevent multiple submissions
      // taskId -> {promptLogEntry, claudeUI: {llmResponseEntry, outputArea, rawOutputArea}, bazelQueryUI, bazelTestUI, pollingIntervalId}
      const activeTasks = {};

      // Helper to create UI for a single LLM response
      function createLLMResponseUI(llmName) {
        const llmClone = document.importNode(llmResponseTemplate.content, true);
        const llmResponseEntry = llmClone.querySelector('.llm-response-entry');
        llmResponseEntry.querySelector('.llm-title').textContent = llmName;
        const outputArea = llmResponseEntry.querySelector('.output-area');
        const rawOutputArea = llmResponseEntry.querySelector('.raw-output-area');
        taskLogContainer.append(llmResponseEntry);

        return { llmResponseEntry, outputArea, rawOutputArea };
      }

      // Helper to create UI for a single Bazel response
      function createBazelResponseUI(title) {
        const bazelClone = document.importNode(bazelResponseTemplate.content, true);
        const bazelResponseEntry = bazelClone.querySelector('.bazel-response-entry');
        bazelResponseEntry.querySelector('.bazel-title').textContent = title;
        const outputArea = bazelResponseEntry.querySelector('.output-area');
        const rawOutputArea = bazelResponseEntry.querySelector('.raw-output-area');
        taskLogContainer.append(bazelResponseEntry);

        return { bazelResponseEntry, outputArea, rawOutputArea };
      }

      function createTaskLogUI(promptText) {
        // Create prompt log entry
        const promptClone = document.importNode(promptLogTemplate.content, true);
        const promptLogEntry = promptClone.querySelector('.prompt-log-entry');
        promptLogEntry.textContent = promptText;
        taskLogContainer.append(promptLogEntry); // Append prompt box first

        // Create UI for Claude
        const claudeUI = createLLMResponseUI("Claude");
        // Create UI for Bazel Query (initially hidden)
        const bazelQueryUI = createBazelResponseUI("Bazel Query");
        bazelQueryUI.bazelResponseEntry.style.display = 'none';
        // Create UI for Bazel Test (initially hidden)
        const bazelTestUI = createBazelResponseUI("Bazel Test");
        bazelTestUI.bazelResponseEntry.style.display = 'none';

        return { promptLogEntry, claudeUI, bazelQueryUI, bazelTestUI };
      }

      function updateOutput(outputAreaElement, output) {
        outputAreaElement.textContent = output;
      }

      function updateRawOutput(rawOutputAreaElement, output) {
        rawOutputAreaElement.textContent = output;
      }

      function setLLMResponseStyle(element, statusType) {
        let bgColor, borderColor;
        switch (statusType) {
          case 'running':
            bgColor = '#fff3e0'; // Light orange background
            borderColor = '#ff9800';   // Sharper orange border
            break;
          case 'success':
            bgColor = '#e8f5e9'; // Light green background
            borderColor = '#4caf50';   // Sharper green border
            break;
          case 'error':
            bgColor = '#ffebee'; // Light red background
            borderColor = '#f44336';   // Sharper red border
            break;
          default: // Default or initial state
            bgColor = '#fcfcfc';
            borderColor = '#ddd';
            break;
        }
        element.style.backgroundColor = bgColor;
        element.style.borderColor = borderColor;
      }

      function setBazelResponseStyle(element, statusType) {
        let bgColor, borderColor;
        switch (statusType) {
          case 'running':
            bgColor = '#E3F2FD'; // Light blue background
            borderColor = '#2196F3';   // Sharper blue border
            break;
          case 'success':
            bgColor = '#e8f5e9'; // Light green background
            borderColor = '#4caf50';   // Sharper green border
            break;
          case 'error':
            bgColor = '#ffebee'; // Light red background
            borderColor = '#f44336';   // Sharper red border
            break;
          default: // Default or initial state
            bgColor = '#E8EAF6'; // Default light indigo
            borderColor = '#C5CAE9';
            break;
        }
        element.style.backgroundColor = bgColor;
        element.style.borderColor = borderColor;
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
        const promptExecUI = activeTasks[taskId];
        if (!promptExecUI) {
          console.error('UI elements not found for prompt execution:', taskId);
          if (activeTasks[taskId] && activeTasks[taskId].pollingIntervalId) {
            clearInterval(activeTasks[taskId].pollingIntervalId);
          }
          delete activeTasks[taskId];
          return;
        }

        try {
          const response = await fetch('/api/summarize-task/' + taskId);
          const data = await response.json();

          if (!response.ok) {
            console.error('Failed to fetch prompt execution summary:', data.error || 'Unknown error');
            updateOutput(promptExecUI.claudeUI.outputArea, 'Error fetching summary: ' + (data.error || 'Unknown error'));
            // Also update Bazel UIs if they were active
            if (promptExecUI.bazelQueryUI.bazelResponseEntry.style.display !== 'none') {
                updateOutput(promptExecUI.bazelQueryUI.outputArea, 'Error fetching summary: ' + (data.error || 'Unknown error'));
            }
            if (promptExecUI.bazelTestUI.bazelResponseEntry.style.display !== 'none') {
                updateOutput(promptExecUI.bazelTestUI.outputArea, 'Error fetching summary: ' + (data.error || 'Unknown error'));
            }
          } else {
            // Update Claude UI
            const claudeData = data.claude;
            updateRawOutput(promptExecUI.claudeUI.rawOutputArea, claudeData.output || "");
            updateOutput(promptExecUI.claudeUI.outputArea, claudeData.summary || "No summary available yet.");
            setLLMResponseStyle(promptExecUI.claudeUI.llmResponseEntry, claudeData.status);

            // Update Bazel Query UI if present
            if (data.bazelQuery) {
                const bazelQueryData = data.bazelQuery;
                promptExecUI.bazelQueryUI.bazelResponseEntry.style.display = 'block'; // Show it
                updateRawOutput(promptExecUI.bazelQueryUI.rawOutputArea, bazelQueryData.output || "");
                updateOutput(promptExecUI.bazelQueryUI.outputArea, bazelQueryData.summary || "No summary available yet.");
                setBazelResponseStyle(promptExecUI.bazelQueryUI.bazelResponseEntry, bazelQueryData.status);
            }

            // Update Bazel Test UI if present
            if (data.bazelTest) {
                const bazelTestData = data.bazelTest;
                promptExecUI.bazelTestUI.bazelResponseEntry.style.display = 'block'; // Show it
                updateRawOutput(promptExecUI.bazelTestUI.rawOutputArea, bazelTestData.output || "");
                updateOutput(promptExecUI.bazelTestUI.outputArea, bazelTestData.summary || "No summary available yet.");
                setBazelResponseStyle(promptExecUI.bazelTestUI.bazelResponseEntry, bazelTestData.status);
            }

            // Check overall status to decide when to stop polling and enable form
            if (data.overallStatus === 'success' || data.overallStatus === 'error') {
              clearInterval(promptExecUI.pollingIntervalId);
              delete activeTasks[taskId];
              enableForm();
            }
          }

        } catch (error) {
          console.error('Summarization polling failed:', error.message);
          updateOutput(promptExecUI.claudeUI.outputArea, 'Summarization polling failed: ' + error.message);
          // Also update Bazel UIs if they were active
          if (promptExecUI.bazelQueryUI.bazelResponseEntry.style.display !== 'none') {
              updateOutput(promptExecUI.bazelQueryUI.outputArea, 'Summarization polling failed: ' + error.message);
          }
          if (promptExecUI.bazelTestUI.bazelResponseEntry.style.display !== 'none') {
              updateOutput(promptExecUI.bazelTestUI.outputArea, 'Summarization polling failed: ' + error.message);
          }
          clearInterval(promptExecUI.pollingIntervalId);
          delete activeTasks[taskId];
          enableForm();
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

        const newUI = createTaskLogUI(prompt); // Creates promptLogEntry, claudeUI, bazelQueryUI, bazelTestUI
        
        // Initialize Claude UI
        updateOutput(newUI.claudeUI.outputArea, "Starting Claude task...");
        updateRawOutput(newUI.claudeUI.rawOutputArea, "No raw output yet.");
        newUI.claudeUI.rawOutputArea.style.display = 'none'; // Ensure raw output is hidden initially
        setLLMResponseStyle(newUI.claudeUI.llmResponseEntry, 'running');

        // Initialize Bazel Query UI
        updateOutput(newUI.bazelQueryUI.outputArea, "Waiting for Bazel query...");
        updateRawOutput(newUI.bazelQueryUI.rawOutputArea, "No raw output yet.");
        newUI.bazelQueryUI.rawOutputArea.style.display = 'none';
        setBazelResponseStyle(newUI.bazelQueryUI.bazelResponseEntry, 'default');

        // Initialize Bazel Test UI
        updateOutput(newUI.bazelTestUI.outputArea, "Waiting for Bazel test...");
        updateRawOutput(newUI.bazelTestUI.rawOutputArea, "No raw output yet.");
        newUI.bazelTestUI.rawOutputArea.style.display = 'none';
        setBazelResponseStyle(newUI.bazelTestUI.bazelResponseEntry, 'default');

        // Add event listeners to toggle raw output on click for the entire LLM/Bazel response box
        function addToggleClickListener(uiElement, isLLM = true) {
            const entryElement = isLLM ? uiElement.llmResponseEntry : uiElement.bazelResponseEntry;
            entryElement.style.cursor = 'pointer'; // Indicate it's clickable
            entryElement.addEventListener('click', function() {
                if (uiElement.rawOutputArea.style.display === 'none') {
                    uiElement.rawOutputArea.style.display = 'block';
                } else {
                    uiElement.rawOutputArea.style.display = 'none';
                }
            });
        }
        addToggleClickListener(newUI.claudeUI, true);
        addToggleClickListener(newUI.bazelQueryUI, false);
        addToggleClickListener(newUI.bazelTestUI, false);

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
          // If task couldn't even start, clean up UI elements
          const errorMessage = 'Error starting task: ' + error.message;
          setLLMResponseStyle(newUI.claudeUI.llmResponseEntry, 'error');
          updateOutput(newUI.claudeUI.outputArea, errorMessage);
          enableForm();
          newUI.promptLogEntry.remove();
          newUI.claudeUI.llmResponseEntry.remove();
          return;
        }

        if (!taskId) {
          const errorMessage = 'Error: Did not receive a task ID from server.';
          updateOutput(newUI.claudeUI.outputArea, errorMessage);
          enableForm();
          newUI.promptLogEntry.remove();
          newUI.claudeUI.llmResponseEntry.remove();
          newUI.bazelQueryUI.bazelResponseEntry.remove();
          newUI.bazelTestUI.bazelResponseEntry.remove();
          return;
        }

        activeTasks[taskId] = {
          promptLogEntry: newUI.promptLogEntry,
          claudeUI: newUI.claudeUI,
          bazelQueryUI: newUI.bazelQueryUI,
          bazelTestUI: newUI.bazelTestUI,
          pollingIntervalId: null,
        };

        // Initial messages for polling status
        updateOutput(newUI.claudeUI.outputArea, "Claude task started, waiting for updates...");
        
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
	indexTmpl    = template.Must(template.New("index").Parse(indexHTML))
	repoTmpl     = template.Must(template.New("repo").Parse(repoHTML))
	notebookTmpl = template.Must(template.New("notebook").Parse(notebookHTML))
	workDir      string
)

// Notebook represents a single existing notebook (worktree).
type Notebook struct {
	Owner    string
	Repo     string
	RepoName string // owner/repo
	Name     string // notebook_name
}

type IndexData struct {
	Query     string
	Result    string
	Error     string
	Notebooks []Notebook // List of existing notebooks
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
	mux.HandleFunc("/repo/", repoHandler)                           // Handle /repo/{owner}/{repo}
	mux.HandleFunc("/create-notebook/", createNotebookHandler)      // POST /create-notebook/{owner}/{repo}
	mux.HandleFunc("/notebook/", notebookHandler)                   // GET /notebook/{owner}/{repo}/{notebook_name}
	mux.HandleFunc("/api/run-prompt/", apiRunPromptHandler)         // POST /api/run-prompt/{owner}/{repo}/{notebook_name}
	mux.HandleFunc("/api/poll-task/", apiPollTaskHandler)           // GET /api/poll-task/{task_id}
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

	notebooks, err := listNotebooks()
	if err != nil {
		log.Printf("Error listing notebooks: %v", err)
		// Don't fail the whole page, just log the error and proceed without notebooks
	}

	data := IndexData{
		Query:     r.URL.Query().Get("repo"),
		Notebooks: notebooks,
	}

	if err := indexTmpl.Execute(w, data); err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}

// listNotebooks scans the worktree directory and returns a list of existing notebooks.
func listNotebooks() ([]Notebook, error) {
	var notebooks []Notebook
	worktreeBaseDir := filepath.Join(workDir, "worktree")

	// Check if the base directory exists
	if _, err := os.Stat(worktreeBaseDir); os.IsNotExist(err) {
		return []Notebook{}, nil // No worktrees directory, so no notebooks
	} else if err != nil {
		return nil, fmt.Errorf("error accessing worktree base directory %q: %w", worktreeBaseDir, err)
	}

	// owner directories
	ownerDirs, err := os.ReadDir(worktreeBaseDir)
	if err != nil {
		return nil, fmt.Errorf("error reading worktree base directory %q: %w", worktreeBaseDir, err)
	}

	for _, ownerEntry := range ownerDirs {
		if !ownerEntry.IsDir() {
			continue
		}
		owner := ownerEntry.Name()
		repoBaseDir := filepath.Join(worktreeBaseDir, owner)

		// repo directories
		repoDirs, err := os.ReadDir(repoBaseDir)
		if err != nil {
			log.Printf("Error reading repo directory %q: %v", repoBaseDir, err)
			continue
		}

		for _, repoEntry := range repoDirs {
			if !repoEntry.IsDir() {
				continue
			}
			repo := repoEntry.Name()
			notebookBaseDir := filepath.Join(repoBaseDir, repo)

			// notebook directories (which are the worktrees)
			notebookDirs, err := os.ReadDir(notebookBaseDir)
			if err != nil {
				log.Printf("Error reading notebook directory %q: %v", notebookBaseDir, err)
				continue
			}

			for _, notebookEntry := range notebookDirs {
				if !notebookEntry.IsDir() {
					continue
				}
				notebookName := notebookEntry.Name()
				notebooks = append(notebooks, Notebook{
					Owner:    owner,
					Repo:     repo,
					RepoName: fmt.Sprintf("%s/%s", owner, repo),
					Name:     notebookName,
				})
			}
		}
	}
	return notebooks, nil
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

// runSummary invokes the llm command with a summarization prompt for any given text.
// It uses the gpt-5-nano model and asks for a single-sentence summary.
func runSummary(ctx context.Context, textToSummarize string, systemPrompt string) (string, error) {
	if textToSummarize == "" {
		return "", nil // Nothing to summarize
	}
	log.Printf("Running llm for summary of text length %d", len(textToSummarize))

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

// runLLMSummary invokes the llm command with a summarization prompt for LLM output.
func runLLMSummary(ctx context.Context, textToSummarize string) (string, error) {
	systemPrompt := `
		This is the output from a coding agent.
		Can you summarize the output in a single sentence?
		The agent may still be thinking or reading files, in which case you can summarize what the agent has thought or done so far.
		The agent may have provided a partial or complete answer to a question, in which case you should summarize that answer and ignore the thinking and tool use.
		Agents may print diagnostic information such as 'Data collection disabled.' Please ignore diagnostic information in your summary.
		If there is nothing worth summarizing, please responding 'Running...' or some other pithy response.
	`
	return runSummary(ctx, textToSummarize, systemPrompt)
}

// runBazelSummary invokes the llm command with a summarization prompt for Bazel output.
func runBazelSummary(ctx context.Context, textToSummarize string) (string, error) {
	systemPrompt := `
		This is the output from a Bazel command (query or test).
		Can you summarize the output in a single sentence?
		Focus on the key results, such as the number of targets found, or the test results (e.g., "X tests passed, Y failed").
		If there is nothing worth summarizing, please respond with a concise status like "Running..." or "No targets found."
	`
	return runSummary(ctx, textToSummarize, systemPrompt)
}

// runLLMCommand executes a single LLM command (gemini or claude) and updates the provided LLMResponse.
func runLLMCommand(llmResponse *LLMResponse, worktreePath, llmName, prompt string) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	llmResponse.mu.Lock()
	llmResponse.Status = "running"
	llmResponse.Output = ""
	llmResponse.Err = nil
	llmResponse.Done = false
	llmResponse.HasSummary = false
	llmResponse.Summary = ""
	llmResponse.mu.Unlock()

	log.Printf("Running %s for prompt in worktree %s", llmName, worktreePath)

	var cmd *exec.Cmd
	extraEnv := []string{"GIT_TERMINAL_PROMPT=0"}

	switch llmName {
	case "gemini":
		cmd = exec.CommandContext(ctx, "gemini", "--prompt", prompt)
	case "claude":
		cmd = exec.CommandContext(ctx, "claude", "--print", prompt) // Assuming 'claude --print $PROMPT'
		if anthropicKey := os.Getenv("ANTHROPIC_API_KEY"); anthropicKey != "" {
			extraEnv = append(extraEnv, "ANTHROPIC_API_KEY="+anthropicKey)
		}
	case "codex":
		cmd = exec.CommandContext(ctx, "codex", "exec", prompt)
	default:
		llmResponse.mu.Lock()
		llmResponse.Err = fmt.Errorf("unknown LLM: %s", llmName)
		llmResponse.Status = "error"
		llmResponse.Done = true
		llmResponse.mu.Unlock()
		log.Printf("Unknown LLM specified: %s", llmName)
		return
	}

	cmd.Dir = worktreePath
	cmd.Env = append(os.Environ(), extraEnv...) // Append any extra environment variables

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		llmResponse.mu.Lock()
		llmResponse.Err = fmt.Errorf("failed to get stdout pipe for %s: %w", llmName, err)
		llmResponse.Status = "error"
		llmResponse.Done = true
		llmResponse.mu.Unlock()
		log.Printf("%s command failed to get stdout pipe: %v", llmName, err)
		return
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		llmResponse.mu.Lock()
		llmResponse.Err = fmt.Errorf("failed to get stderr pipe for %s: %w", llmName, err)
		llmResponse.Status = "error"
		llmResponse.Done = true
		llmResponse.mu.Unlock()
		log.Printf("%s command failed to get stderr pipe: %v", llmName, err)
		return
	}

	if err := cmd.Start(); err != nil {
		llmResponse.mu.Lock()
		llmResponse.Err = fmt.Errorf("failed to start %s command: %w", llmName, err)
		llmResponse.Status = "error"
		llmResponse.Done = true
		llmResponse.mu.Unlock()
		log.Printf("%s command failed to start: %v", llmName, err)
		return
	}

	var wg sync.WaitGroup
	wg.Add(2) // Two goroutines for stdout and stderr
	var combinedOutputBuilder strings.Builder

	// Goroutine to read stdout
	go func() {
		defer wg.Done()
		scanner := bufio.NewScanner(stdoutPipe)
		for scanner.Scan() {
			line := scanner.Text()
			combinedOutputBuilder.WriteString(line + "\n")
		}
		if err := scanner.Err(); err != nil {
			log.Printf("Error reading stdout for %s: %v", llmName, err)
		}
	}()

	// Goroutine to read stderr
	go func() {
		defer wg.Done()
		scanner := bufio.NewScanner(stderrPipe)
		for scanner.Scan() {
			line := scanner.Text()
			combinedOutputBuilder.WriteString(line + "\n")
		}
		if err := scanner.Err(); err != nil {
			log.Printf("Error reading stderr for %s: %v", llmName, err)
		}
	}()

	wg.Wait() // Wait for both readers to finish after pipes are closed

	// Wait for the command to exit
	execErr := cmd.Wait()

	llmResponse.mu.Lock()
	defer llmResponse.mu.Unlock()

	llmResponse.Output = strings.TrimSpace(combinedOutputBuilder.String())
	llmResponse.Done = true

	if execErr != nil {
		llmResponse.Err = execErr
		llmResponse.Status = "error"
		log.Printf("%s command finished with error: %v\nOutput:\n%s", llmName, execErr, llmResponse.Output)
	} else {
		llmResponse.Status = "success"
		log.Printf("%s command finished successfully.\nOutput:\n%s", llmName, llmResponse.Output)
	}
}

// executePromptTask orchestrates the execution of multiple LLM commands for a single prompt.
func executePromptTask(pe *PromptExecution, worktreePath, prompt, notebookName string) {
	var wg sync.WaitGroup
	wg.Add(1) // Always add for Claude

	// Run Claude
	go func() {
		defer wg.Done()
		runLLMCommand(&pe.Claude, worktreePath, "claude", prompt)
	}()

	// Check if the prompt is a "test <word>" command
	if strings.HasPrefix(prompt, "test ") {
		word := strings.TrimSpace(strings.TrimPrefix(prompt, "test "))
		if word != "" {
			wg.Add(2) // Add for Bazel Query and Bazel Test

			go func() {
				defer wg.Done()
				runBazelQueryAndTest(&pe.BazelQuery, &pe.BazelTest, worktreePath, word, notebookName)
			}()
		}
	}

	wg.Wait() // Wait for all commands to complete
	log.Printf("All commands for prompt execution %s completed.", notebookName)
}

// runBazelQueryAndTest executes a Bazel query and then Bazel tests if targets are found.
func runBazelQueryAndTest(queryResp, testResp *LLMResponse, worktreePath, word, notebookName string) {
	// Initialize query response
	queryResp.mu.Lock()
	queryResp.Status = "running"
	queryResp.Output = ""
	queryResp.Err = nil
	queryResp.Done = false
	queryResp.HasSummary = false
	queryResp.Summary = ""
	queryResp.mu.Unlock()

	// Initialize test response
	testResp.mu.Lock()
	testResp.Status = "running"
	testResp.Output = ""
	testResp.Err = nil
	testResp.Done = false
	testResp.HasSummary = false
	testResp.Summary = ""
	testResp.mu.Unlock()

	log.Printf("Running Bazel query for word '%s' in worktree %s", word, worktreePath)

	// Determine TRYBOOK_DIR, ORG, REPO for bazel output_base and caches
	trybookDir := workDir
	parts := strings.Split(notebookName, "-") // Assuming notebookName is like owner-repo-date-random
	org := parts[0]
	repo := parts[1]

	bazelOutputBase := filepath.Join(trybookDir, org, repo)
	bazelDiskCache := filepath.Join(trybookDir, "disk_cache")
	bazelRepoCache := filepath.Join(trybookDir, "repository_cache")

	// Ensure cache directories exist
	os.MkdirAll(bazelDiskCache, 0o755)
	os.MkdirAll(bazelRepoCache, 0o755)

	// Bazel Query command
	queryCmdArgs := []string{
		"--output_base=" + bazelOutputBase,
		"query",
		fmt.Sprintf("filter('%s', tests(//...))", word),
		"--disk_cache=" + bazelDiskCache,
		"--repository_cache=" + bazelRepoCache,
	}
	queryCmd := exec.Command("bazel", queryCmdArgs...)
	queryCmd.Dir = worktreePath
	queryCmd.Env = os.Environ() // Inherit environment

	queryOut, queryErr := queryCmd.CombinedOutput()

	queryResp.mu.Lock()
	queryResp.Output = strings.TrimSpace(string(queryOut))
	queryResp.Done = true
	if queryErr != nil {
		queryResp.Err = queryErr
		queryResp.Status = "error"
		log.Printf("Bazel query failed: %v\nOutput:\n%s", queryErr, queryResp.Output)
	} else {
		queryResp.Status = "success"
		log.Printf("Bazel query successful.\nOutput:\n%s", queryResp.Output)
	}
	queryResp.mu.Unlock()

	// If query failed or found no targets, stop here for tests
	if queryErr != nil || queryResp.Output == "" {
		testResp.mu.Lock()
		testResp.Status = "success" // No tests to run is a success for the test step
		testResp.Output = "No Bazel test targets found or query failed."
		testResp.Done = true
		testResp.mu.Unlock()
		return
	}

	// Extract targets from query output (one target per line)
	targets := strings.Fields(queryResp.Output)
	if len(targets) == 0 {
		testResp.mu.Lock()
		testResp.Status = "success"
		testResp.Output = "Bazel query found no test targets."
		testResp.Done = true
		testResp.mu.Unlock()
		return
	}

	log.Printf("Running Bazel test for targets: %v in worktree %s", targets, worktreePath)

	// Bazel Test command
	testCmdArgs := []string{
		"--output_base=" + bazelOutputBase,
		"test",
	}
	testCmdArgs = append(testCmdArgs, targets...)
	testCmdArgs = append(testCmdArgs,
		"--disk_cache="+bazelDiskCache,
		"--repository_cache="+bazelRepoCache,
	)

	testCmd := exec.Command("bazel", testCmdArgs...)
	testCmd.Dir = worktreePath
	testCmd.Env = os.Environ() // Inherit environment

	testOut, testErr := testCmd.CombinedOutput()

	testResp.mu.Lock()
	testResp.Output = strings.TrimSpace(string(testOut))
	testResp.Done = true
	if testErr != nil {
		testResp.Err = testErr
		testResp.Status = "error"
		log.Printf("Bazel test failed: %v\nOutput:\n%s", testErr, testResp.Output)
	} else {
		testResp.Status = "success"
		log.Printf("Bazel test successful.\nOutput:\n%s", testResp.Output)
	}
	testResp.mu.Unlock()
}

// apiRunPromptHandler starts a long-running prompt execution involving multiple LLMs.
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

	promptExecutionID := generatePromptExecutionID()

	// Initialize PromptExecution with separate LLMResponse structs
	pe := &PromptExecution{
		Claude:    LLMResponse{Status: "running"},
		BazelQuery: LLMResponse{Status: "running"}, // Initialize BazelQuery
		BazelTest:  LLMResponse{Status: "running"},  // Initialize BazelTest
	}

	promptExecutionsMu.Lock()
	promptExecutions[promptExecutionID] = pe
	promptExecutionsMu.Unlock()

	go executePromptTask(pe, worktreePath, prompt, notebookName)

	log.Printf("Started prompt execution %s for prompt on %s", promptExecutionID, notebookName)
	json.NewEncoder(w).Encode(map[string]string{"taskId": promptExecutionID})
}

// buildLLMResponseData constructs a map containing the status, summary, and output for a single LLM.
func buildLLMResponseData(llmResp *LLMResponse, ctx context.Context) map[string]interface{} {
	llmResp.mu.RLock()
	currentStatus := llmResp.Status
	currentOutput := llmResp.Output
	llmErr := llmResp.Err
	llmDone := llmResp.Done
	cachedSummary := llmResp.Summary
	cachedHasSummary := llmResp.HasSummary
	llmResp.mu.RUnlock()

	var summary string
	// Determine which summarization function to use based on the LLMResponse type
	// This is a heuristic; a more robust solution might pass a type or a specific prompt.
	var summaryFunc func(context.Context, string) (string, error)
	if strings.Contains(llmResp.Summary, "Bazel") || strings.Contains(llmResp.Summary, "targets") { // Heuristic for Bazel
		summaryFunc = runBazelSummary
	} else {
		summaryFunc = runLLMSummary
	}

	if cachedHasSummary {
		summary = cachedSummary
	} else if llmDone { // LLM is done, but summary not yet generated
		if currentOutput != "" {
			ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			s, err := summaryFunc(ctx, currentOutput)
			if err != nil {
				log.Printf("Failed to generate final summary for LLM: %v", err)
				summary = "Could not generate final summary."
			} else {
				summary = s
				// Cache the generated summary
				llmResp.mu.Lock()
				llmResp.Summary = summary
				llmResp.HasSummary = true
				llmResp.mu.Unlock()
			}
		} else {
			summary = "No output available for final summary."
		}
	} else { // LLM is still running, generate a real-time summary
		if currentOutput != "" {
			ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			s, err := summaryFunc(ctx, currentOutput)
			if err != nil {
				log.Printf("Failed to generate running summary for LLM: %v", err)
				summary = "Could not generate summary."
			} else {
				summary = s
			}
		} else {
			summary = "No output available yet."
		}
	}

	data := map[string]interface{}{
		"status":  currentStatus,
		"summary": summary,
		"output":  currentOutput,
		"done":    llmDone,
	}
	if llmErr != nil {
		data["error"] = llmErr.Error()
	}
	return data
}

// apiPollTaskHandler returns the current status and output of a task.
// This handler is less detailed than apiSummarizeTaskHandler and primarily shows Gemini's state.
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
	promptExecutionID := parts[3]

	promptExecutionsMu.RLock()
	pe, ok := promptExecutions[promptExecutionID]
	promptExecutionsMu.RUnlock()

	if !ok {
		http.Error(w, `{"error": "Prompt execution not found"}`, http.StatusNotFound)
		return
	}

	// For apiPollTaskHandler, we'll return Claude's status as the primary.
	pe.Claude.mu.RLock()
	resp := map[string]interface{}{
		"taskId": promptExecutionID,
		"status": pe.Claude.Status, // Report Claude's status as primary
		"output": pe.Claude.Output, // Report Claude's output as primary
		"done":   pe.Claude.Done,   // Report Claude's done status
	}
	if pe.Claude.Err != nil {
		resp["error"] = pe.Claude.Err.Error()
	}
	pe.Claude.mu.RUnlock()

	json.NewEncoder(w).Encode(resp)
}

// apiSummarizeTaskHandler returns summaries of both LLMs for a prompt execution.
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
	promptExecutionID := parts[3]

	promptExecutionsMu.RLock()
	pe, ok := promptExecutions[promptExecutionID]
	promptExecutionsMu.RUnlock()

	if !ok {
		http.Error(w, `{"error": "Prompt execution not found"}`, http.StatusNotFound)
		return
	}

	// Prepare response for Claude
	claudeResp := buildLLMResponseData(&pe.Claude, r.Context())

	// Prepare response for Bazel Query
	bazelQueryResp := buildLLMResponseData(&pe.BazelQuery, r.Context())

	// Prepare response for Bazel Test
	bazelTestResp := buildLLMResponseData(&pe.BazelTest, r.Context())

	// Determine overall status for the prompt execution
	// If it's a "test" prompt, overall status depends on BazelTest.
	// Otherwise, it depends on Claude.
	overallStatus := "running"
	if strings.HasPrefix(r.FormValue("prompt"), "test ") { // Check original prompt to determine primary task
		if pe.BazelTest.Done {
			if pe.BazelTest.Status == "success" {
				overallStatus = "success"
			} else {
				overallStatus = "error"
			}
		}
	} else {
		if pe.Claude.Done {
			if pe.Claude.Status == "success" {
				overallStatus = "success"
			} else {
				overallStatus = "error"
			}
		}
	}

	// Construct the full response for the client
	resp := map[string]interface{}{
		"taskId":        promptExecutionID,
		"overallStatus": overallStatus, // Can be "running", "success", "error"
		"claude":        claudeResp,
		"bazelQuery":    bazelQueryResp,
		"bazelTest":     bazelTestResp,
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
func createWorktree(ctx context.Context, baseRepoDir, owner, repo, notebookName, branchName string) (worktreePath string, err error) {
	worktreePath = filepath.Join(workDir, "worktree", owner, repo, notebookName)

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
	FullName        string `json:"fullName"`
	Description     string `json:"description"`
	URL             string `json:"url"`
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
