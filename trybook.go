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
	"sync"
	"syscall"
	"time"
)

// r is a global random number generator for generating unique names.
var r *rand.Rand

func init() {
	r = rand.New(rand.NewSource(time.Now().UnixNano()))
}

// RepoOperation holds the output, status, and summary for a single git repository operation.
type RepoOperation struct {
	mu         sync.RWMutex // Protects fields of this RepoOperation
	Output     string       // Stores combined stdout/stderr/progress
	Status     string       // "running", "success", "error"
	Done       bool         // if true, the operation has finished processing (either success or error)
	Err        error        // Stores the Go error if operation failed
	RepoDir    string       // Path to the cloned/pulled repository
	CommitHash string       // HEAD commit hash after successful operation
	RepoName   string       // Full name of the repository (owner/repo)
}

var (
	// repoOperations maps an operation ID to its RepoOperation struct.
	repoOperations   = make(map[string]*RepoOperation)
	repoOperationsMu sync.RWMutex
)

// generateOperationID creates a unique ID for a repository operation.
func generateOperationID() string {
	return fmt.Sprintf("%x", r.Int63())
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
  #repo-status-container {
    margin-top: 1rem;
    padding: 1rem;
    border: 1px solid #ccc;
    border-radius: 4px;
    background-color: #f9f9f9;
    display: none; /* Hidden by default */
  }
  #repo-status-output {
    white-space: pre-wrap;
    font-family: monospace;
    font-size: 0.9em;
    max-height: 200px;
    overflow-y: auto;
    background-color: #eee;
    padding: 0.5rem;
    border-radius: 4px;
    margin-top: 0.5rem;
  }
</style>
</head>
<body style="padding: 1rem; text-align: left;">
  <div>
    <h1>trybook</h1>
    <form id="repoForm">
      <div style="display: flex; gap: 0.5rem;">
        <input type="url" id="repoUrl" name="repo" placeholder="github repo" value="{{.Query}}" autofocus style="flex-grow: 1; font-size: 1.25rem; padding: 0.6rem 0.75rem;">
        <button type="submit" id="openButton" style="font-size: 1.1rem; padding: 0.6rem 1rem;">Open</button>
      </div>
    </form>
    <div id="suggestions" style="margin-top: 0.5rem; text-align: left;"></div>

    <div id="repo-status-container">
      <p id="repo-status-message" style="font-weight: bold;"></p>
      <pre id="repo-status-output"></pre>
      <p id="repo-status-error" style="color: #b00020; font-size: 0.95rem; margin-top: 0.5rem; white-space: pre-wrap;"></p>
    </div>

    {{if .Error}}
    <p style="color: #b00020; font-size: 0.95rem; margin-top: 1rem; white-space: pre-wrap;">Error: {{.Error}}</p>
    {{end}}
    {{if .Result}}
    <p style="color: #0a7; font-size: 0.95rem; margin-top: 1rem; white-space: pre-wrap;">{{.Result}}</p>
    {{end}}
  </div>
<script>
(function(){
  const repoInput = document.getElementById('repoUrl');
  const repoForm = document.getElementById('repoForm');
  const openButton = document.getElementById('openButton');
  const suggestionsBox = document.getElementById('suggestions');

  const repoStatusContainer = document.getElementById('repo-status-container');
  const repoStatusMessage = document.getElementById('repo-status-message');
  const repoStatusOutput = document.getElementById('repo-status-output');
  const repoStatusError = document.getElementById('repo-status-error');

  let searchController = null;
  let debounceTimer = null;
  let pollingIntervalId = null;
  let currentRepoOperationTaskId = null;

  function clearSuggestionsBox() { suggestionsBox.innerHTML = ''; }

  function renderSuggestions(items) {
    if (!items || items.length === 0) { clearSuggestionsBox(); return; }
    suggestionsBox.innerHTML = items.map(function(it) {
      var desc = it.description ? it.description : '';
      var escFullName = it.fullName.replace(/&/g,'&amp;').replace(/</g,'&lt;');
      var escDesc = desc.replace(/&/g,'&amp;').replace(/</g,'&lt;');
      return '<div class="sugg-item" data-repo="' + escFullName + '">' +
               '<div class="sugg-title">' + escFullName + '</div>' +
               '<div class="sugg-desc">' + escDesc + '</div>' +
             '</div>';
    }).join('');
    Array.prototype.forEach.call(suggestionsBox.querySelectorAll('.sugg-item'), function(el) {
      el.addEventListener('click', function() {
        const repoFullName = el.getAttribute('data-repo');
        repoInput.value = 'https://github.com/' + repoFullName;
        repoForm.dispatchEvent(new Event('submit')); // Trigger form submission
      });
    });
  }

  async function searchRepos(q){
    if (searchController) searchController.abort();
    searchController = new AbortController();
    try{
      const resp = await fetch('/api/search?query=' + encodeURIComponent(q), { signal: searchController.signal });
      if (!resp.ok) { clearSuggestionsBox(); return; }
      const data = await resp.json();
      renderSuggestions(Array.isArray(data) ? data.slice(0,5) : []);
    } catch(e) {
      if (!(e && e.name === 'AbortError')) clearSuggestionsBox();
    }
  }

  repoInput.addEventListener('input', function() {
    const q = repoInput.value.trim();
    if (q.length < 2) { clearSuggestionsBox(); if (searchController) searchController.abort(); return; }
    clearTimeout(debounceTimer);
    debounceTimer = setTimeout(function(){ searchRepos(q); }, 250);
  });

  document.addEventListener('click', function(e) {
    if (!suggestionsBox.contains(e.target) && e.target !== repoInput) {
      clearSuggestionsBox();
    }
  });

  function showRepoStatus(message, output = '', error = '') {
    repoStatusContainer.style.display = 'block';
    repoStatusMessage.textContent = message;
    repoStatusOutput.textContent = output;
    repoStatusError.textContent = error;
  }

  function hideRepoStatus() {
    repoStatusContainer.style.display = 'none';
    repoStatusMessage.textContent = '';
    repoStatusOutput.textContent = '';
    repoStatusError.textContent = '';
  }

  function disableForm() {
    repoInput.disabled = true;
    openButton.disabled = true;
    clearSuggestionsBox();
  }

  function enableForm() {
    repoInput.disabled = false;
    openButton.disabled = false;
    repoInput.focus();
  }

  async function startRepoOperation(repoUrl) {
    disableForm();
    showRepoStatus('Starting repository operation...');
    try {
      const response = await fetch('/api/start-repo-operation', {
        method: 'POST',
        headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
        body: new URLSearchParams({ repo: repoUrl }).toString(),
      });
      const data = await response.json();
      if (!response.ok) {
        throw new Error(data.error || 'Failed to start repo operation');
      }
      currentRepoOperationTaskId = data.taskId;
      pollRepoOperationStatus(); // Start polling immediately
      pollingIntervalId = setInterval(pollRepoOperationStatus, 1000);
    } catch (error) {
      showRepoStatus('Error initiating operation:', '', error.message);
      enableForm();
    }
  }

  async function pollRepoOperationStatus() {
    if (!currentRepoOperationTaskId) {
      clearInterval(pollingIntervalId);
      return;
    }

    try {
      const response = await fetch('/api/poll-repo-operation/' + currentRepoOperationTaskId);
      const data = await response.json();

      if (!response.ok) {
        clearInterval(pollingIntervalId);
        showRepoStatus('Error polling status:', '', data.error || 'Unknown error');
        enableForm();
        return;
      }

      let message = '';
      switch (data.status) {
        case 'running':
          message = 'Repository operation in progress...';
          break;
        case 'success':
          message = 'Repository operation completed successfully.';
          break;
        case 'error':
          message = 'Repository operation failed.';
          break;
        default:
          message = 'Unknown status.';
      }

      showRepoStatus(message, data.output, data.error || '');

      if (data.done) {
        clearInterval(pollingIntervalId);
        if (data.status === 'success') {
          // Redirect to the repo page
          window.location.href = '/repo/' + data.repoName;
        } else {
          enableForm();
        }
      }
    } catch (error) {
      clearInterval(pollingIntervalId);
      showRepoStatus('Error during polling:', '', error.message);
      enableForm();
    }
  }

  repoForm.addEventListener('submit', function(event) {
    event.preventDefault();
    const repoUrl = repoInput.value.trim();
    if (repoUrl) {
      startRepoOperation(repoUrl);
    } else {
      alert('Please enter a GitHub repository URL.');
    }
  });

  // Check if a repo param is present in the URL on page load, if so, start operation
  const urlParams = new URLSearchParams(window.location.search);
  const initialRepo = urlParams.get('repo');
  const initialTaskId = urlParams.get('taskId'); // Added for direct task polling, if needed

  if (initialRepo) {
      repoInput.value = initialRepo; // Populate input for user
      startRepoOperation(initialRepo);
  } else if (initialTaskId) { // If we ever want to just poll by taskId directly (e.g., for recovery)
      currentRepoOperationTaskId = initialTaskId;
      showRepoStatus('Resuming repository operation...');
      pollRepoOperationStatus();
      pollingIntervalId = setInterval(pollRepoOperationStatus, 1000);
  } else {
    enableForm(); // Ensure form is enabled if no operation is starting
    hideRepoStatus(); // Hide status container by default
  }
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
	Gemini LLMResponse
	Claude LLMResponse
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
        <div style="font-weight: bold; margin-bottom: 0.5rem;" class="llm-title"></div>
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
      const llmResponseTemplate = document.getElementById('llmResponseTemplate');

      let isSubmitting = false; // Flag to prevent multiple submissions
      // taskId -> {promptLogEntry, geminiUI: {llmResponseEntry, outputArea, toggleButton, rawOutputArea}, claudeUI: {llmResponseEntry, outputArea, toggleButton, rawOutputArea}, pollingIntervalId}
      const activeTasks = {}; 

      // Helper to create UI for a single LLM response
      function createLLMResponseUI(llmName) {
        const llmClone = document.importNode(llmResponseTemplate.content, true);
        const llmResponseEntry = llmClone.querySelector('.llm-response-entry');
        llmResponseEntry.querySelector('.llm-title').textContent = llmName;
        const toggleButton = llmResponseEntry.querySelector('.toggle-raw-output');
        const outputArea = llmResponseEntry.querySelector('.output-area');
        const rawOutputArea = llmResponseEntry.querySelector('.raw-output-area');
        taskLogContainer.append(llmResponseEntry);

        return { llmResponseEntry, outputArea, toggleButton, rawOutputArea };
      }

      function createTaskLogUI(promptText) {
        // Create prompt log entry
        const promptClone = document.importNode(promptLogTemplate.content, true);
        const promptLogEntry = promptClone.querySelector('.prompt-log-entry');
        promptLogEntry.textContent = 'Prompt: "' + promptText + '"';
        taskLogContainer.append(promptLogEntry); // Append prompt box first

        // Create UI for Gemini
        const geminiUI = createLLMResponseUI("Gemini");

        // Create UI for Claude
        const claudeUI = createLLMResponseUI("Claude");

        return { promptLogEntry, geminiUI, claudeUI };
      }

      function updateOutput(outputAreaElement, output) {
        outputAreaElement.textContent = output;
      }

      function updateRawOutput(rawOutputAreaElement, output) {
        rawOutputAreaElement.textContent = output;
      }

      function setLLMResponseStyle(llmResponseElement, statusType) {
        switch (statusType) {
          case 'running':
            llmResponseElement.style.backgroundColor = '#fff3e0'; // Light orange background
            llmResponseElement.style.borderColor = '#ff9800';   // Sharper orange border
            break;
          case 'success':
            llmResponseElement.style.backgroundColor = '#e8f5e9'; // Light green background
            llmResponseElement.style.borderColor = '#4caf50';   // Sharper green border
            break;
          case 'error':
            llmResponseElement.style.backgroundColor = '#ffebee'; // Light red background
            llmResponseElement.style.borderColor = '#f44336';   // Sharper red border
            break;
          default: // Default or initial state
            llmResponseElement.style.backgroundColor = '#fcfcfc';
            llmResponseElement.style.borderColor = '#ddd';
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
            updateOutput(promptExecUI.geminiUI.outputArea, 'Error fetching summary: ' + (data.error || 'Unknown error'));
            updateOutput(promptExecUI.claudeUI.outputArea, 'Error fetching summary: ' + (data.error || 'Unknown error'));
          } else {
            // Update Gemini UI
            const geminiData = data.gemini;
            updateRawOutput(promptExecUI.geminiUI.rawOutputArea, geminiData.output || "");
            if (geminiData.output && geminiData.output.trim() !== "") {
              promptExecUI.geminiUI.toggleButton.style.display = 'inline-block';
            } else {
              promptExecUI.geminiUI.toggleButton.style.display = 'none';
            }
            updateOutput(promptExecUI.geminiUI.outputArea, geminiData.summary || "No summary available yet.");
            setLLMResponseStyle(promptExecUI.geminiUI.llmResponseEntry, geminiData.status);

            // Update Claude UI
            const claudeData = data.claude;
            updateRawOutput(promptExecUI.claudeUI.rawOutputArea, claudeData.output || "");
            if (claudeData.output && claudeData.output.trim() !== "") {
              promptExecUI.claudeUI.toggleButton.style.display = 'inline-block';
            } else {
              promptExecUI.claudeUI.toggleButton.style.display = 'none';
            }
            updateOutput(promptExecUI.claudeUI.outputArea, claudeData.summary || "No summary available yet.");
            setLLMResponseStyle(promptExecUI.claudeUI.llmResponseEntry, claudeData.status);

            // Check overall status to decide when to stop polling and enable form
            if (data.overallStatus === 'success' || data.overallStatus === 'error') {
              clearInterval(promptExecUI.pollingIntervalId);
              delete activeTasks[taskId];
              enableForm();
            }
          }

        } catch (error) {
          console.error('Summarization polling failed:', error.message);
          updateOutput(promptExecUI.geminiUI.outputArea, 'Summarization polling failed: ' + error.message);
          updateOutput(promptExecUI.claudeUI.outputArea, 'Summarization polling failed: ' + error.message);
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

        const newUI = createTaskLogUI(prompt); // Creates promptLogEntry, geminiUI, claudeUI
        
        // Initialize Gemini UI
        updateOutput(newUI.geminiUI.outputArea, "Starting Gemini task...");
        updateRawOutput(newUI.geminiUI.rawOutputArea, "No raw output yet.");
        newUI.geminiUI.toggleButton.style.display = 'none';
        newUI.geminiUI.toggleButton.style.transform = 'rotate(0deg)';
        newUI.geminiUI.rawOutputArea.style.display = 'none';
        setLLMResponseStyle(newUI.geminiUI.llmResponseEntry, 'running');

        // Initialize Claude UI
        updateOutput(newUI.claudeUI.outputArea, "Starting Claude task...");
        updateRawOutput(newUI.claudeUI.rawOutputArea, "No raw output yet.");
        newUI.claudeUI.toggleButton.style.display = 'none';
        newUI.claudeUI.toggleButton.style.transform = 'rotate(0deg)';
        newUI.claudeUI.rawOutputArea.style.display = 'none';
        setLLMResponseStyle(newUI.claudeUI.llmResponseEntry, 'running');

        // Add event listeners for the toggle buttons
        function addToggleButtonListener(uiElement) {
            uiElement.toggleButton.addEventListener('click', function() {
                if (uiElement.rawOutputArea.style.display === 'none') {
                    uiElement.rawOutputArea.style.display = 'block';
                    uiElement.toggleButton.style.transform = 'rotate(90deg)';
                    uiElement.toggleButton.style.color = '#333';
                } else {
                    uiElement.rawOutputArea.style.display = 'none';
                    uiElement.toggleButton.style.transform = 'rotate(0deg)';
                    uiElement.toggleButton.style.color = '#666';
                }
            });
        }
        addToggleButtonListener(newUI.geminiUI);
        addToggleButtonListener(newUI.claudeUI);

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
          // If task couldn't even start, clean up both UI elements
          const errorMessage = 'Error starting task: ' + error.message;
          setLLMResponseStyle(newUI.geminiUI.llmResponseEntry, 'error');
          updateOutput(newUI.geminiUI.outputArea, errorMessage);
          setLLMResponseStyle(newUI.claudeUI.llmResponseEntry, 'error');
          updateOutput(newUI.claudeUI.outputArea, errorMessage);
          enableForm();
          newUI.promptLogEntry.remove();
          newUI.geminiUI.llmResponseEntry.remove();
          newUI.claudeUI.llmResponseEntry.remove();
          return;
        }

        if (!taskId) {
          const errorMessage = 'Error: Did not receive a task ID from server.';
          updateOutput(newUI.geminiUI.outputArea, errorMessage);
          updateOutput(newUI.claudeUI.outputArea, errorMessage);
          enableForm();
          newUI.promptLogEntry.remove();
          newUI.geminiUI.llmResponseEntry.remove();
          newUI.claudeUI.llmResponseEntry.remove();
          return;
        }

        activeTasks[taskId] = {
          promptLogEntry: newUI.promptLogEntry,
          geminiUI: newUI.geminiUI,
          claudeUI: newUI.claudeUI,
          pollingIntervalId: null,
        };

        // Initial messages for polling status
        updateOutput(newUI.geminiUI.outputArea, "Gemini task started, waiting for updates...");
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
	mux.HandleFunc("/api/start-repo-operation", apiStartRepoOperationHandler) // New API to start git operations
	mux.HandleFunc("/api/poll-repo-operation/", apiPollRepoOperationHandler)   // New API to poll git operation status
	mux.HandleFunc("/repo/", repoHandler)                                      // Handle /repo/{owner}/{repo}
	mux.HandleFunc("/create-notebook/", createNotebookHandler)                // POST /create-notebook/{owner}/{repo}
	mux.HandleFunc("/notebook/", notebookHandler)                             // GET /notebook/{owner}/{repo}/{notebook_name}
	mux.HandleFunc("/api/run-prompt/", apiRunPromptHandler)                   // POST /api/run-prompt/{owner}/{repo}/{notebook_name}
	mux.HandleFunc("/api/poll-task/", apiPollTaskHandler)                     // GET /api/poll-task/{task_id}
	mux.HandleFunc("/api/summarize-task/", apiSummarizeTaskHandler)           // GET /api/summarize-task/{task_id}

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
		// taskId is now managed by JS, but keeping Query for initial input population
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

// runLLMSummary invokes the llm command with a summarization prompt for any given text.
// It uses the gpt-5-nano model and asks for a single-sentence summary.
func runLLMSummary(ctx context.Context, textToSummarize string) (string, error) {
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
	wg.Add(2) // One for Gemini, one for Claude

	// Run Gemini
	go func() {
		defer wg.Done()
		runLLMCommand(&pe.Gemini, worktreePath, "gemini", prompt)
	}()

	// Run Claude
	go func() {
		defer wg.Done()
		runLLMCommand(&pe.Claude, worktreePath, "claude", prompt)
	}()

	wg.Wait() // Wait for both LLM commands to complete
	log.Printf("All LLM commands for prompt execution %s completed.", notebookName)
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
		Gemini: LLMResponse{Status: "running"},
		Claude: LLMResponse{Status: "running"},
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
	if cachedHasSummary {
		summary = cachedSummary
	} else if llmDone { // LLM is done, but summary not yet generated
		if currentOutput != "" {
			ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			s, err := runLLMSummary(ctx, currentOutput) // Using runLLMSummary for any text summarization
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
			s, err := runLLMSummary(ctx, currentOutput)
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

// apiStartRepoOperationHandler starts a long-running git clone/pull operation.
func apiStartRepoOperationHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		http.Error(w, `{"error": "Method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	repoURL := r.FormValue("repo")
	if repoURL == "" {
		http.Error(w, `{"error": "Repository URL cannot be empty"}`, http.StatusBadRequest)
		return
	}

	// Parse owner/repo from the input URL
	owner, repo, err := parseGitHubInput(repoURL)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error": %q}`, err.Error()), http.StatusBadRequest)
		return
	}
	repoFullName := owner + "/" + repo

	operationID := generateOperationID()

	// Initialize RepoOperation struct
	op := &RepoOperation{
		Status:   "running",
		RepoName: repoFullName,
	}

	repoOperationsMu.Lock()
	repoOperations[operationID] = op
	repoOperationsMu.Unlock()

	// Start the git operation in a goroutine
	go manageRepo(r.Context(), op)

	log.Printf("Started repo operation %s for %s", operationID, repoFullName)
	json.NewEncoder(w).Encode(map[string]string{"taskId": operationID})
}

// apiPollRepoOperationHandler returns the current status and output of a git operation.
func apiPollRepoOperationHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodGet {
		http.Error(w, `{"error": "Method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	parts := strings.Split(r.URL.Path, "/")
	if len(parts) < 4 || parts[2] != "poll-repo-operation" {
		http.Error(w, `{"error": "Invalid API URL"}`, http.StatusBadRequest)
		return
	}
	operationID := parts[3]

	repoOperationsMu.RLock()
	op, ok := repoOperations[operationID]
	repoOperationsMu.RUnlock()

	if !ok {
		http.Error(w, `{"error": "Repository operation not found"}`, http.StatusNotFound)
		return
	}

	op.mu.RLock()
	resp := map[string]interface{}{
		"taskId":     operationID,
		"status":     op.Status,
		"output":     op.Output,
		"done":       op.Done,
		"repoDir":    op.RepoDir,
		"commitHash": op.CommitHash,
		"repoName":   op.RepoName,
	}
	if op.Err != nil {
		resp["error"] = op.Err.Error()
	}
	op.mu.RUnlock()

	json.NewEncoder(w).Encode(resp)
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

	// For apiPollTaskHandler, we'll return a combined status/output for simplicity,
	// or indicate that it's deprecated in favor of summarize-task if this becomes complex.
	// For now, let's just show Gemini's status as the "overall" for this legacy endpoint.
	pe.Gemini.mu.RLock()
	resp := map[string]interface{}{
		"taskId": promptExecutionID,
		"status": pe.Gemini.Status, // Report Gemini's status as primary
		"output": pe.Gemini.Output, // Report Gemini's output as primary
		"done":   pe.Gemini.Done,   // Report Gemini's done status
	}
	if pe.Gemini.Err != nil {
		resp["error"] = pe.Gemini.Err.Error()
	}
	pe.Gemini.mu.RUnlock()

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

	// Prepare responses for both Gemini and Claude
	geminiResp := buildLLMResponseData(&pe.Gemini, r.Context())
	claudeResp := buildLLMResponseData(&pe.Claude, r.Context())

	// Determine overall status for the prompt execution
	overallStatus := "running"
	if (pe.Gemini.Done && pe.Gemini.Status == "success") && (pe.Claude.Done && pe.Claude.Status == "success") {
		overallStatus = "success"
	} else if (pe.Gemini.Done && pe.Gemini.Status == "error") || (pe.Claude.Done && pe.Claude.Status == "error") {
		overallStatus = "error"
	} else if pe.Gemini.Done && pe.Claude.Done { // Both done, but not both success (at least one error)
		overallStatus = "error"
	}

	// Construct the full response for the client
	resp := map[string]interface{}{
		"taskId":        promptExecutionID,
		"overallStatus": overallStatus, // Can be "running", "success", "error" based on both LLMs
		"gemini":        geminiResp,
		"claude":        claudeResp,
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

func manageRepo(ctx context.Context, op *RepoOperation) {
	op.mu.Lock()
	op.Status = "running"
	op.Output = ""
	op.Err = nil
	op.Done = false
	op.CommitHash = ""
	op.mu.Unlock()

	owner, repo, err := parseGitHubInput(op.RepoName)
	if err != nil {
		op.mu.Lock()
		op.Err = fmt.Errorf("invalid repository name: %w", err)
		op.Status = "error"
		op.Done = true
		op.mu.Unlock()
		log.Printf("Failed to parse repo name %q: %v", op.RepoName, err)
		return
	}

	repoDir := filepath.Join(workDir, "clone", owner, repo)
	sshURL := "ssh://git@github.com/" + owner + "/" + repo

	// Timeout the git operation to avoid hanging connections.
	opCtx, cancel := context.WithTimeout(ctx, 120*time.Second) // Increased timeout for potentially slow clones
	defer cancel()

	var cmd *exec.Cmd
	var operationDesc string

	_, err = os.Stat(repoDir)
	if err == nil { // Directory exists, perform pull
		operationDesc = "git pull"
		log.Printf("Starting git pull for %s in %s", sshURL, repoDir)
		cmd = exec.CommandContext(opCtx, "git", "pull", "--progress")
		cmd.Dir = repoDir // Set working directory for pull
	} else if os.IsNotExist(err) { // Directory does not exist, perform clone
		operationDesc = "git clone"
		log.Printf("Starting git clone of %s into %s", sshURL, repoDir)
		if err := os.MkdirAll(repoDir, 0o755); err != nil {
			op.mu.Lock()
			op.Err = fmt.Errorf("create repo directory %q: %w", repoDir, err)
			op.Status = "error"
			op.Done = true
			op.mu.Unlock()
			return
		}
		cmd = exec.CommandContext(opCtx, "git", "clone", "--depth=1", "--single-branch", "--progress", sshURL, repoDir)
	} else {
		op.mu.Lock()
		op.Err = fmt.Errorf("stat %q: %w", repoDir, err)
		op.Status = "error"
		op.Done = true
		op.mu.Unlock()
		return
	}

	// Avoid interactive prompts in server context.
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")

	// Capture both stdout and stderr (progress goes to stderr by default for git)
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		op.mu.Lock()
		op.Err = fmt.Errorf("failed to get stdout pipe for %s: %w", operationDesc, err)
		op.Status = "error"
		op.Done = true
		op.mu.Unlock()
		return
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		op.mu.Lock()
		op.Err = fmt.Errorf("failed to get stderr pipe for %s: %w", operationDesc, err)
		op.Status = "error"
		op.Done = true
		op.mu.Unlock()
		return
	}

	if err := cmd.Start(); err != nil {
		op.mu.Lock()
		op.Err = fmt.Errorf("failed to start %s command: %w", operationDesc, err)
		op.Status = "error"
		op.Done = true
		op.mu.Unlock()
		log.Printf("%s command failed to start: %v", operationDesc, err)
		return
	}

	var outputBuilder strings.Builder
	var wg sync.WaitGroup
	wg.Add(2)

	// Goroutine to read stdout
	go func() {
		defer wg.Done()
		scanner := bufio.NewScanner(stdoutPipe)
		for scanner.Scan() {
			line := scanner.Text()
			op.mu.Lock()
			outputBuilder.WriteString(line + "\n")
			op.Output = outputBuilder.String() // Update continuously
			op.mu.Unlock()
		}
	}()

	// Goroutine to read stderr (git progress is often here)
	go func() {
		defer wg.Done()
		scanner := bufio.NewScanner(stderrPipe)
		for scanner.Scan() {
			line := scanner.Text()
			op.mu.Lock()
			outputBuilder.WriteString(line + "\n")
			op.Output = outputBuilder.String() // Update continuously
			op.mu.Unlock()
		}
	}()

	wg.Wait() // Wait for both readers to finish

	execErr := cmd.Wait()

	op.mu.Lock()
	defer op.mu.Unlock()

	op.Output = strings.TrimSpace(outputBuilder.String())
	op.Done = true
	op.RepoDir = repoDir // Store the final repo directory

	if execErr != nil {
		op.Err = execErr
		op.Status = "error"
		log.Printf("%s command finished with error: %v\nOutput:\n%s", operationDesc, execErr, op.Output)
	} else {
		op.Status = "success"
		log.Printf("%s command finished successfully.\nOutput:\n%s", operationDesc, op.Output)

		// Get the HEAD commit hash after successful operation
		commitHash, err := getHeadCommit(opCtx, repoDir)
		if err != nil {
			op.Err = fmt.Errorf("could not get HEAD commit after %s: %w", operationDesc, err)
			op.Status = "error"
			log.Printf("Failed to get HEAD commit after %s: %v", operationDesc, err)
		} else {
			op.CommitHash = commitHash
		}
	}
}

// repoHandler now simply renders the repo page, assuming the repo is already available.
func repoHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	parts := strings.Split(r.URL.Path, "/")
	if len(parts) < 4 || parts[1] != "repo" {
		http.Error(w, "Invalid repository URL", http.StatusBadRequest)
		return
	}
	owner := parts[2]
	repo := parts[3]
	repoFullName := owner + "/" + repo

	repoDir := filepath.Join(workDir, "clone", owner, repo)

	data := RepoPageData{
		Owner:    owner,
		Repo:     repo,
		RepoName: repoFullName,
	}

	// Verify the repo directory exists
	_, err := os.Stat(repoDir)
	if os.IsNotExist(err) {
		data.Error = fmt.Sprintf("Repository not found at %s. Please go back and try again.", repoDir)
		log.Printf("Repository directory not found: %s", repoDir)
	} else if err != nil {
		data.Error = fmt.Sprintf("Error accessing repository path %s: %v", repoDir, err)
		log.Printf("Error accessing repository path %s: %v", repoDir, err)
	} else {
		// If repo exists, get the current commit hash
		commitHash, err := getHeadCommit(r.Context(), repoDir)
		if err != nil {
			data.Error = fmt.Sprintf("Error getting HEAD commit for %s: %v", repoFullName, err)
			log.Printf("Error getting HEAD commit for %s: %v", repoFullName, err)
		} else {
			data.CommitHash = commitHash
		}
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

	// First, initiate the repo operation to ensure the base repository is cloned/pulled
	// This needs to be synchronous here, so we create a RepoOperation, start manageRepo in a goroutine, and wait.
	op := &RepoOperation{
		RepoName: repoFullName,
	}

	// Add to global map just in case, though not strictly needed for this synchronous wait pattern
	// since we won't be polling it via API.
	// We do not generate an ID here as it's not exposed via API for polling.

	// Start the git operation in a goroutine
	go manageRepo(r.Context(), op)

	// Wait for the repo operation to complete
	// We will poll its internal 'Done' status.
	// Add a timeout to prevent indefinite blocking in case of issues.
	waitCtx, waitCancel := context.WithTimeout(r.Context(), 180*time.Second) // Longer timeout than git op itself
	defer waitCancel()

	ticker := time.NewTicker(200 * time.Millisecond) // Poll every 200ms
	defer ticker.Stop()

	for {
		select {
		case <-waitCtx.Done():
			log.Printf("Timeout waiting for repo operation for %s: %v", repoFullName, waitCtx.Err())
			http.Error(w, fmt.Sprintf("Timeout preparing base repository: %v", waitCtx.Err()), http.StatusRequestTimeout)
			return
		case <-ticker.C:
			op.mu.RLock()
			done := op.Done
			opErr := op.Err
			opStatus := op.Status
			op.mu.RUnlock()

			if done {
				if opErr != nil {
					log.Printf("Error ensuring base repo for notebook creation %s: %v\nOutput:\n%s", repoFullName, opErr, op.Output)
					http.Error(w, fmt.Sprintf("Error preparing base repository: %v\nOutput:\n%s", opErr, op.Output), http.StatusInternalServerError)
					return
				}
				if opStatus != "success" {
					log.Printf("Repo operation for %s failed with status %q (no explicit error)", repoFullName, opStatus)
					http.Error(w, fmt.Sprintf("Failed to prepare base repository (status: %q)\nOutput:\n%s", opStatus, op.Output), http.StatusInternalServerError)
					return
				}
				break // Operation completed successfully
			}
		}
		if op.Done {
			break
		}
	}
	baseRepoDir := op.RepoDir // Get the final directory from the completed operation

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
