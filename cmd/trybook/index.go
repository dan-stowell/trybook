package main

import (
	"context"
	"embed"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

	//go:embed index.html
	var indexFS embed.FS

	var indexTmpl = template.Must(template.ParseFS(indexFS, "index.html"))

func indexHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	data := IndexData{
		Query: r.URL.Query().Get("repo"),
	}

	if data.Query != "" {
		dir, err := cloneDefaultBranch(r.Context(), data.Query)
		if err != nil {
			data.Error = err.Error()
		} else {
			data.Result = fmt.Sprintf("Cloned default branch to %s", dir)
		}
	}

	if err := indexTmpl.Execute(w, data); err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}

type IndexData struct {
	Query  string
	Result string
	Error  string
}

func cloneDefaultBranch(ctx context.Context, input string) (string, error) {
	owner, repo, err := parseGitHubInput(input)
	if err != nil {
		return "", err
	}

	sshURL := "ssh://git@github.com/" + owner + "/" + repo

	// Timeout the clone to avoid hanging connections.
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	// Create a unique parent temp dir and clone into a subdir with repo name.
	parent, err := os.MkdirTemp("", "trybook-")
	if err != nil {
		return "", fmt.Errorf("create temp dir: %w", err)
	}
	dest := filepath.Join(parent, repo)

	cmd := exec.CommandContext(ctx, "git", "clone", "--depth=1", "--single-branch", sshURL, dest)
	// Avoid interactive prompts in server context.
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git clone failed: %v\n%s", err, string(out))
	}
	return dest, nil
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
