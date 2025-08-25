package main

import (
	"database/sql"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite" // Import for SQLite driver
)

const htmlContent = `
<!DOCTYPE html>
<html>
<head>
    <title>trybook</title>
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
            padding: 0.8em; /* Increased padding */
            margin-right: 1em;
            border: 1px solid #ccc;
            border-radius: 4px;
            font-size: 1.1em; /* Larger font size */
        }
        button {
            padding: 0.8em 1.5em; /* Increased padding */
            background-color: #007bff;
            color: white;
            border: none;
            border-radius: 4px;
            cursor: pointer;
            font-size: 1.1em; /* Larger font size */
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
    <h1>trybook</h1>
    <div id="content">
        <pre id="output"></pre>
    </div>
    <form id="inputForm">
        <input type="text" id="commandInput" placeholder="Enter command" autofocus>
        <button type="submit">try</button>
    </form>

    <script>
        document.getElementById('inputForm').addEventListener('submit', async function(event) {
            event.preventDefault();
            const command = document.getElementById('commandInput').value;
            const outputElement = document.getElementById('output');
            outputElement.textContent = 'trying...';
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

func runHandler(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	command := r.FormValue("command")

	// Store the command in the database
	insertSQL := `INSERT INTO commands(command) VALUES(?)`
	_, err := db.Exec(insertSQL, command)
	if err != nil {
		log.Printf("Failed to insert command into DB: %v", err)
		http.Error(w, "Internal server error: failed to store command", http.StatusInternalServerError)
		return
	}

	// For now, just echo the command back.
	// In a real application, you would execute the command and return its output.
	fmt.Fprintf(w, "You entered: %s\n(Command stored in DB. Execution not yet implemented)", command)
}

func main() {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		log.Fatalf("Error getting user home directory: %v", err)
	}
	defaultTryDir := filepath.Join(homeDir, ".trybook")

	tryDirFlag := flag.String("trydir", defaultTryDir, "Directory to store trybook data (SQLite DB)")
	flag.Parse()

	// Ensure the directory exists
	if err := os.MkdirAll(*tryDirFlag, 0755); err != nil {
		log.Fatalf("Failed to create directory %s: %v", *tryDirFlag, err)
	}

	dbPath := filepath.Join(*tryDirFlag, "trybook.db")
	log.Printf("Using SQLite database at: %s", dbPath)

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		log.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	// Create 'commands' table if it doesn't exist
	createTableSQL := `
	CREATE TABLE IF NOT EXISTS commands (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		command TEXT NOT NULL,
		timestamp DATETIME DEFAULT CURRENT_TIMESTAMP
	);`
	_, err = db.Exec(createTableSQL)
	if err != nil {
		log.Fatalf("Failed to create table: %v", err)
	}

	http.HandleFunc("/", handler)
	http.HandleFunc("/run", func(w http.ResponseWriter, r *http.Request) {
		runHandler(w, r, db)
	})

	port := ":8080"
	log.Printf("Server starting on port %s\n", port)
	log.Fatal(http.ListenAndServe(port, nil))
}
