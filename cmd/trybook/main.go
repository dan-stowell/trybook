package main

import (
	"context"
	"html/template"
	"log"
	"net/http"
	"os"
	"os/signal"
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
  :root { color-scheme: light dark; }
  body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif; margin: 0; padding: 2rem; line-height: 1.5; }
  .container { max-width: 900px; margin: 0 auto; }
  h1 { margin-top: 0; font-size: 2rem; }
  form { margin-top: 1rem; }
  input[type="url"] { width: 100%; box-sizing: border-box; padding: 1rem; font-size: 1rem; border-radius: 8px; border: 1px solid #ccc; }
  input[type="url"] { height: 3rem; }
  button { margin-top: 1rem; padding: 0.75rem 1.25rem; font-size: 1rem; border: none; border-radius: 6px; background: #2d6cdf; color: white; cursor: pointer; }
  button:hover { background: #2156b6; }
  .hint { color: #666; font-size: 0.9rem; margin-top: 0.5rem; }
</style>
</head>
<body>
  <div class="container">
    <h1>trybook</h1>
    <p>Enter a GitHub repository URL or owner/repo to explore or edit its local clone.</p>
    <form method="GET" action="/">
      <label for="repoUrl" style="display:block; font-weight:600; margin-bottom:0.5rem;">GitHub repository URL or owner/repo</label>
      <input type="url" id="repoUrl" name="repo" placeholder="https://github.com/owner/repo or owner/repo" autofocus>
      <div class="hint">Example: https://github.com/owner/repo or owner/repo</div>
      <button type="submit">Open</button>
    </form>
  </div>
</body>
</html>`

var indexTmpl = template.Must(template.New("index").Parse(indexHTML))

func indexHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := indexTmpl.Execute(w, nil); err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/", indexHandler)

	addr := getAddr()

	srv := &http.Server{
		Addr:              addr,
		Handler:           logRequest(mux),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		log.Printf("trybook listening on http://%s", prettyAddr(addr))
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

func getAddr() string {
	if v := os.Getenv("TRYBOOK_ADDR"); v != "" {
		return v
	}
	if v := os.Getenv("PORT"); v != "" {
		// support platforms that provide only PORT
		if strings.HasPrefix(v, ":") {
			return v
		}
		return ":" + v
	}
	return "127.0.0.1:8080"
}

func logRequest(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start))
	})
}

func prettyAddr(addr string) string {
	if strings.HasPrefix(addr, ":") {
		return "127.0.0.1" + addr
	}
	return addr
}
