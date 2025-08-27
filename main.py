import os
import sys
import socket
import sqlite3
from datetime import datetime
import uvicorn
from fastapi import FastAPI, Path
from fastapi.responses import HTMLResponse
import git

def get_free_port():
    """Finds a free port on the system."""
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
        s.bind(('', 0))
        return s.getsockname()[1]

def main():
    if len(sys.argv) != 2:
        print("Usage: python main.py <path_to_git_repository>")
        sys.exit(1)

    repo_path = sys.argv[1]

    if not os.path.isdir(repo_path):
        print(f"Error: Directory not found at {repo_path}", file=sys.stderr)
        sys.exit(1)

    try:
        repo = git.Repo(repo_path)
        if repo.bare:
            print(f"Error: {repo_path} is a bare Git repository. Please provide a non-bare repository.", file=sys.stderr)
            sys.exit(1)
    except git.InvalidGitRepositoryError:
        print(f"Error: {repo_path} is not a valid Git repository.", file=sys.stderr)
        sys.exit(1)
    except git.NoSuchPathError:
        print(f"Error: Path does not exist for Git repository: {repo_path}", file=sys.stderr)
        sys.exit(1)

    # Change to the repository directory
    os.chdir(repo_path)

    db_path = os.path.join(repo_path, ".repobookdb")

    def init_db():
        conn = sqlite3.connect(db_path)
        cursor = conn.cursor()
        cursor.execute('''
            CREATE TABLE IF NOT EXISTS branches (
                id INTEGER PRIMARY KEY AUTOINCREMENT,
                branch_name TEXT NOT NULL,
                commit_sha TEXT NOT NULL,
                timestamp DATETIME DEFAULT CURRENT_TIMESTAMP
            )
        ''')
        cursor.execute('''
            CREATE TABLE IF NOT EXISTS raw_inputs (
                id INTEGER PRIMARY KEY AUTOINCREMENT,
                input_text TEXT NOT NULL,
                commit_sha TEXT NOT NULL,
                timestamp DATETIME DEFAULT CURRENT_TIMESTAMP
            )
        ''')
        conn.commit()
        conn.close()

    init_db()

    # Get current branch and commit info
    try:
        current_branch = repo.active_branch.name
        latest_commit_sha = repo.head.commit.hexsha
        latest_commit_message = repo.head.commit.message.strip()
    except TypeError: # Detached HEAD state
        current_branch = "detached HEAD"
        latest_commit_sha = repo.head.commit.hexsha
        latest_commit_message = repo.head.commit.message.strip()
    except Exception as e:
        print(f"Error getting Git info: {e}", file=sys.stderr)
        sys.exit(1)

    # Create and checkout new branch
    timestamp = datetime.now().strftime("%Y%m%d%H%M%S")
    new_branch_name = f"repobook-{timestamp}"
    try:
        new_branch = repo.create_head(new_branch_name)
        new_branch.checkout()
        print(f"Checked out new branch: {new_branch_name}")

        # Store branch info in DB
        conn = sqlite3.connect(db_path)
        cursor = conn.cursor()
        cursor.execute("INSERT INTO branches (branch_name, commit_sha) VALUES (?, ?)",
                       (new_branch_name, latest_commit_sha))
        conn.commit()
        conn.close()
        print(f"Stored branch '{new_branch_name}' with commit '{latest_commit_sha}' in database.")

    except git.GitCommandError as e:
        print(f"Error creating/checking out branch: {e}", file=sys.stderr)
        sys.exit(1)

    repo_name = os.path.basename(repo_path)

    app = FastAPI()

    @app.get("/", response_class=HTMLResponse)
    async def read_root():
        html_content = f"""
        <!DOCTYPE html>
        <html>
        <head>
            <title>Repobook: {repo_name}</title>
            <script src="https://unpkg.com/htmx.org@1.9.10"></script>
            <style>
                body {{ font-family: sans-serif; margin: 2em; }}
                h1 {{ color: #333; }}
                p {{ color: #666; font-size: 0.9em; }}
                .input-form {{ margin-top: 1.5em; }}
                .input-form input[type="text"] {{
                    width: 80%;
                    padding: 0.8em;
                    font-size: 1em;
                    border: 1px solid #ccc;
                    border-radius: 4px;
                }}
            </style>
        </head>
        <body>
            <h1>{repo_name}</h1>
            <p><strong>Branch:</strong> {new_branch_name}</p>
            <p><strong>Latest Commit:</strong> {latest_commit_message}</p>

            <div class="input-form">
                <form hx-post="/submit_input" hx-target="#input-feedback" hx-swap="innerHTML" hx-on--after-request="this.reset()">
                    <input type="text" name="user_input" placeholder="Enter your thoughts here..." hx-trigger="keyup[keyCode==13]" />
                </form>
                <div id="input-feedback" style="color: green; margin-top: 0.5em;"></div>
            </div>
        </body>
        </html>
        """
        return HTMLResponse(content=html_content)

    @app.post("/submit_input")
    async def submit_input(user_input: str):
        try:
            conn = sqlite3.connect(db_path)
            cursor = conn.cursor()
            cursor.execute("INSERT INTO raw_inputs (input_text, commit_sha) VALUES (?, ?)",
                           (user_input, latest_commit_sha))
            conn.commit()
            conn.close()
            return HTMLResponse(content="Input recorded!")
        except Exception as e:
            print(f"Error recording input: {e}", file=sys.stderr)
            return HTMLResponse(content="Error recording input.", status_code=500)

    port = get_free_port()
    print(f"Starting Repobook server on http://127.0.0.1:{port}")
    uvicorn.run(app, host="127.0.0.1", port=port)

if __name__ == "__main__":
    main()
