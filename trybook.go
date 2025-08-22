package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"os/user"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

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
      var escOwner = it.fullName.replace(/&/g,'&amp;').replace(/</g,'&lt;');
      var escDesc = desc.replace(/&/g,'&amp;').replace(/</g,'&lt;');
      return '<div class="sugg-item" data-repo="' + escOwner + '">' +
               '<div class="sugg-title">' + escOwner + '</div>' +
               '<div class="sugg-desc">' + escDesc + '</div>' +
             '</div>';
    }).join('');
    Array.prototype.forEach.call(box.querySelectorAll('.sugg-item'), function(el) {
      el.addEventListener('click', function() {
        input.value = el.getAttribute('data-repo');
        clearBox();
        input.focus();
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

var (
	indexTmpl = template.Must(template.New("index").Parse(indexHTML))
	workDir   string
)

type IndexData struct {
	Query  string
	Result string
	Error  string
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

	if data.Query != "" {
		dir, err := manageRepo(r.Context(), data.Query)
		if err != nil {
			data.Error = err.Error()
		} else {
			data.Result = fmt.Sprintf("Managed repository at %s", dir)
		}
	}

	if err := indexTmpl.Execute(w, data); err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}

func manageRepo(ctx context.Context, input string) (string, error) {
	owner, repo, err := parseGitHubInput(input)
	if err != nil {
		return "", err
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
			return "", fmt.Errorf("create repo directory %q: %w", repoDir, err)
		}
		cmd = exec.CommandContext(ctx, "git", "clone", "--depth=1", "--single-branch", sshURL, repoDir)
	} else {
		return "", fmt.Errorf("stat %q: %w", repoDir, err)
	}

	// Avoid interactive prompts in server context.
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("Failed %s for %s after %s: %v\n%s", operation, sshURL, time.Since(opStart), err, string(out))
		return "", fmt.Errorf("%s failed: %v\n%s", operation, err, string(out))
	}
	log.Printf("Completed %s for %s in %s", operation, sshURL, time.Since(opStart))
	return repoDir, nil
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

