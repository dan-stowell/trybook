package main

import (
	"fmt"
	"html/template"
	"log"
	"net/http"
)

const htmlContent = `
<!DOCTYPE html>
<html>
<head>
    <title>TryBook</title>
    <style>
        body { font-family: sans-serif; margin: 2em; }
        input[type="text"] { width: 80%; padding: 0.5em; }
        button { padding: 0.5em 1em; }
    </style>
</head>
<body>
    <h1>TryBook</h1>
    <form id="inputForm">
        <input type="text" id="commandInput" placeholder="Enter command" autofocus>
        <button type="submit">Run</button>
    </form>
    <pre id="output"></pre>

    <script>
        document.getElementById('inputForm').addEventListener('submit', async function(event) {
            event.preventDefault();
            const command = document.getElementById('commandInput').value;
            const outputElement = document.getElementById('output');
            outputElement.textContent = 'Running...';

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

func handler(w http.ResponseWriter, r *http.Request) {
	tmpl, err := template.New("index").Parse(htmlContent)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	tmpl.Execute(w, nil)
}

func runHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	command := r.FormValue("command")
	// For now, just echo the command back.
	// In a real application, you would execute the command and return its output.
	fmt.Fprintf(w, "You entered: %s\n(Command execution not yet implemented)", command)
}

func main() {
	http.HandleFunc("/", handler)
	http.HandleFunc("/run", runHandler)

	port := ":8080"
	log.Printf("Server starting on port %s\n", port)
	log.Fatal(http.ListenAndServe(port, nil))
}
