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
        body {
            font-family: sans-serif;
            margin: 0;
            display: flex;
            flex-direction: column;
            min-height: 100vh;
        }
        h1 {
            margin: 0 2em;
            padding-top: 1em;
        }
        #content {
            flex-grow: 1;
            padding: 0 2em;
            overflow-y: auto; /* In case content overflows */
        }
        #inputForm {
            display: flex;
            padding: 1em 2em;
            border-top: 1px solid #eee;
            background-color: #f9f9f9;
        }
        input[type="text"] {
            flex-grow: 1;
            padding: 0.5em;
            margin-right: 1em;
            border: 1px solid #ccc;
            border-radius: 4px;
        }
        button {
            padding: 0.5em 1em;
            background-color: #007bff;
            color: white;
            border: none;
            border-radius: 4px;
            cursor: pointer;
        }
        button:hover {
            background-color: #0056b3;
        }
        #output {
            background-color: #f4f4f4;
            padding: 1em;
            border-radius: 4px;
            white-space: pre-wrap;
            word-break: break-all;
            margin-top: 1em;
        }
    </style>
</head>
<body>
    <h1>TryBook</h1>
    <div id="content">
        <pre id="output"></pre>
    </div>
    <form id="inputForm">
        <input type="text" id="commandInput" placeholder="Enter command" autofocus>
        <button type="submit">Run</button>
    </form>

    <script>
        document.getElementById('inputForm').addEventListener('submit', async function(event) {
            event.preventDefault();
            const command = document.getElementById('commandInput').value;
            const outputElement = document.getElementById('output');
            outputElement.textContent = 'Running...';
            document.getElementById('commandInput').value = ''; // Clear input after submission

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
