import os
import sys
import socket
import sqlite3
from datetime import datetime
import html
import subprocess
import uvicorn
from fastapi import FastAPI, Path, Form
from fastapi.responses import HTMLResponse, RedirectResponse
import git
from urllib.parse import quote

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
                branch_name TEXT,
                commit_message TEXT,
                commit_author_date DATETIME,
                short_sha TEXT,
                timestamp DATETIME DEFAULT CURRENT_TIMESTAMP
            )
        ''')
        # Backfill schema for existing databases: add columns if missing
        cursor.execute("PRAGMA table_info(raw_inputs)")
        existing_cols = {row[1] for row in cursor.fetchall()}
        schema_adds = [
            ("branch_name", "TEXT"),
            ("commit_message", "TEXT"),
            ("commit_author_date", "DATETIME"),
            ("short_sha", "TEXT"),
        ]
        for col_name, col_type in schema_adds:
            if col_name not in existing_cols:
                cursor.execute(f"ALTER TABLE raw_inputs ADD COLUMN {col_name} {col_type}")

        conn.commit()
        conn.close()

    init_db()

    # Get current branch and commit info
    try:
        current_branch = repo.active_branch.name
        latest_commit_sha = repo.head.commit.hexsha
        msg_full = repo.head.commit.message or ""
        latest_commit_message = msg_full.splitlines()[0].strip() if msg_full else ""
    except TypeError: # Detached HEAD state
        current_branch = "detached HEAD"
        latest_commit_sha = repo.head.commit.hexsha
        msg_full = repo.head.commit.message or ""
        latest_commit_message = msg_full.splitlines()[0].strip() if msg_full else ""
    except Exception as e:
        print(f"Error getting Git info: {e}", file=sys.stderr)
        sys.exit(1)

    # Determine branch to use: most recent existing from DB, otherwise create new repobook-<timestamp>
    try:
        # Fetch most recently created branches from DB
        conn = sqlite3.connect(db_path)
        cursor = conn.cursor()
        cursor.execute("SELECT branch_name FROM branches ORDER BY timestamp DESC")
        rows = cursor.fetchall()
        conn.close()

        # Find first branch that still exists in the repo
        existing_heads = {h.name for h in repo.heads}
        selected_branch = None
        for (bname,) in rows:
            if bname in existing_heads:
                selected_branch = bname
                break

        if selected_branch:
            repo.git.checkout(selected_branch)
            active_branch_name = selected_branch
            print(f"Checked out existing branch: {active_branch_name}")
        else:
            timestamp = datetime.now().strftime("%Y%m%d%H%M%S")
            new_branch_name = f"repobook-{timestamp}"
            new_branch = repo.create_head(new_branch_name)
            new_branch.checkout()
            active_branch_name = new_branch_name
            print(f"Checked out new branch: {new_branch_name}")

            # Store branch info in DB
            conn = sqlite3.connect(db_path)
            cursor = conn.cursor()
            cursor.execute("INSERT INTO branches (branch_name, commit_sha) VALUES (?, ?)",
                           (new_branch_name, latest_commit_sha))
            conn.commit()
            conn.close()
            print(f"Stored branch '{new_branch_name}' with commit '{latest_commit_sha}' in database.")

        # Refresh latest commit message after checkout
        _msg_full = repo.head.commit.message or ""
        latest_commit_message = _msg_full.splitlines()[0].strip() if _msg_full else ""

    except git.GitCommandError as e:
        print(f"Error selecting/creating branch: {e}", file=sys.stderr)
        sys.exit(1)

    repo_name = os.path.basename(repo_path)

    app = FastAPI()

    def get_repo_status():
        try:
            # Ensure we are in the correct directory for git commands
            current_dir = os.getcwd()
            os.chdir(repo_path)

            untracked_files = repo.untracked_files
            changed_files = [item.a_path for item in repo.index.diff(None)]

            os.chdir(current_dir) # Change back to original directory

            short_sha = repo.head.commit.hexsha[:7]
            full_msg = repo.head.commit.message or ""
            single_line_msg = full_msg.splitlines()[0].strip() if full_msg else ""
            commit_msg = html.escape(single_line_msg, quote=True)

            status_message = f"Current Commit: {short_sha} - {commit_msg}<br>"
            if untracked_files:
                safe_untracked = ", ".join(html.escape(f, quote=True) for f in untracked_files)
                status_message += f"Untracked files: {safe_untracked}<br>"
            if changed_files:
                safe_changed = ", ".join(html.escape(f, quote=True) for f in changed_files)
                status_message += f"Changed files: {safe_changed}<br>"
            if not untracked_files and not changed_files:
                status_message += "No untracked or changed files."
            return status_message
        except Exception as e:
            return f"Error getting repo status: {html.escape(str(e), quote=True)}"

    @app.get("/", response_class=HTMLResponse)
    async def read_root():
        # Redirect to canonical notebook URL: /notebook/<repo>/<branch>
        try:
            current_branch_name = repo.active_branch.name
        except TypeError:
            current_branch_name = "detached HEAD"
        except Exception:
            current_branch_name = active_branch_name
        repo_segment = quote(repo_name, safe="")
        branch_segment = quote(current_branch_name, safe="")
        return RedirectResponse(url=f"/notebook/{repo_segment}/{branch_segment}", status_code=307)

    @app.get("/notebook/{repo_slug}/{branch_name}", response_class=HTMLResponse)
    async def notebook_page(repo_slug: str = Path(...), branch_name: str = Path(...)):
        # Keep URL canonical: if path doesn't match current state, redirect
        try:
            current_branch_name = repo.active_branch.name
        except TypeError:
            current_branch_name = "detached HEAD"
        except Exception:
            current_branch_name = active_branch_name

        if repo_slug != repo_name or branch_name != current_branch_name:
            repo_segment = quote(repo_name, safe="")
            branch_segment = quote(current_branch_name, safe="")
            return RedirectResponse(url=f"/notebook/{repo_segment}/{branch_segment}", status_code=307)

        msg_full_current = repo.head.commit.message or ""
        latest_commit_message_current = msg_full_current.splitlines()[0].strip() if msg_full_current else ""

        # Load existing inputs for the current branch from DB
        conn = sqlite3.connect(db_path)
        cursor = conn.cursor()
        cursor.execute(
            "SELECT input_text, commit_message, short_sha FROM raw_inputs WHERE branch_name = ? ORDER BY id ASC",
            (current_branch_name,),
        )
        prior_rows = cursor.fetchall()
        conn.close()

        entries_html = ""
        for input_text, commit_msg, short_sha in prior_rows:
            safe_input = html.escape(input_text or "", quote=True)
            display_msg_full = (commit_msg or "")
            display_msg = display_msg_full.splitlines()[0].strip() if display_msg_full else ""
            safe_msg = html.escape(display_msg, quote=True)
            safe_short = html.escape(short_sha or "", quote=True)
            entries_html += f'''
            <div class="submitted-entry">
                <div class="input-entry">
                    <input type="text" name="user_input_readonly" value="{safe_input}" readonly />
                </div>
                <div class="status-message">
                    Commit: {safe_short} - {safe_msg}
                </div>
            </div>
            '''

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
                .input-entry {{ margin-top: 1.5em; }}
                .input-entry input[type="text"] {{
                    width: 100%;
                    box-sizing: border-box;
                    padding: 0.8em;
                    font-size: 1em;
                    border: 1px solid #ccc;
                    border-radius: 4px;
                }}
                .status-message {{
                    margin-top: 1em;
                    padding: 0.8em;
                    background-color: #f0f0f0;
                    border: 1px solid #ddd;
                    border-radius: 4px;
                    font-size: 0.9em;
                    color: #555;
                }}
            </style>
        </head>
        <body>
            <h1>{repo_name}</h1>
            <p><strong>Branch:</strong> {current_branch_name}</p>
            <p><strong>Latest Commit:</strong> {latest_commit_message_current}</p>

            <div id="input-container" hx-on:htmx:oobAfterSwap="this.querySelector(&quot;#current-input-form input[name='user_input']&quot;)?.focus()" hx-on:htmx:afterSwap="this.querySelector(&quot;#current-input-form input[name='user_input']&quot;)?.focus()">
                {entries_html}
                <div class="input-entry" id="current-input-form">
                    <form hx-post="/submit_input" hx-target="#current-input-form" hx-swap="outerHTML">
                        <input type="text" name="user_input" placeholder="Enter your thoughts here..." autocomplete="off" autofocus />
                    </form>
                </div>
            </div>
        </body>
        </html>
        """
        return HTMLResponse(content=html_content)

    @app.post("/submit_input")
    async def submit_input(user_input: str = Form(...)):
        try:
            commit = repo.head.commit
            try:
                branch_name = repo.active_branch.name
            except TypeError:
                branch_name = "detached HEAD"
            except Exception:
                branch_name = "unknown"

            commit_sha = commit.hexsha
            short_sha = commit_sha[:7]
            full_commit_message = commit.message or ""
            commit_message = full_commit_message.splitlines()[0].strip() if full_commit_message else ""
            try:
                commit_author_date = commit.authored_datetime.isoformat()
            except Exception:
                commit_author_date = datetime.fromtimestamp(commit.authored_date).isoformat()

            conn = sqlite3.connect(db_path)
            cursor = conn.cursor()
            cursor.execute(
                "INSERT INTO raw_inputs (input_text, commit_sha, branch_name, commit_message, commit_author_date, short_sha) VALUES (?, ?, ?, ?, ?, ?)",
                (user_input, commit_sha, branch_name, commit_message, commit_author_date, short_sha)
            )
            conn.commit()
            conn.close()

            # Get updated repo status
            updated_status = get_repo_status()

            # Optionally run a Bazel query if the input starts with 'q '
            bazel_html = ""
            stripped = user_input.lstrip()
            if stripped.startswith("q "):
                something = stripped[2:].strip()
                if something:
                    try:
                        output_base = f"/tmp/output_base/{repo_name}"
                        query_pattern = f'filter("{something}", //...)'
                        cmd = [
                            "bazel",
                            f"--output_base={output_base}",
                            "query",
                            query_pattern,
                            "--disk_cache=/tmp/disk_cache",
                            "--repository_cache=/tmp/repository_cache",
                            "--config=cache",
                        ]
                        result = subprocess.run(
                            cmd,
                            cwd=repo_path,
                            capture_output=True,
                            text=True,
                            timeout=60,
                        )
                        out = result.stdout.strip()
                        err = result.stderr.strip()
                        exit_code = result.returncode
                        combined = out if out else err
                        if not combined:
                            combined = "(no output)"
                        if len(combined) > 4000:
                            combined = combined[:4000] + "\n... (truncated) ..."
                        safe_combined = html.escape(combined, quote=True)
                        safe_cmd = html.escape(" ".join(cmd), quote=True)
                        bazel_html = f'''
                <div class="status-message">
                    Bazel query exit code {exit_code}<br>
                    <code>{safe_cmd}</code>
                    <pre style="white-space: pre-wrap;">{safe_combined}</pre>
                </div>'''
                    except FileNotFoundError:
                        bazel_html = '''
                <div class="status-message">Bazel not found on PATH.</div>'''
                    except subprocess.TimeoutExpired:
                        bazel_html = '''
                <div class="status-message">Bazel query timed out.</div>'''
                    except Exception as be:
                        bazel_html = f'''
                <div class="status-message">Bazel query error: {html.escape(str(be), quote=True)}</div>'''

            # Safely echo the input back into the HTML attribute
            safe_input = html.escape(user_input, quote=True)

            # Return HTML for the just-submitted input (now read-only) and the status message.
            # Also, use hx-swap-oob to append a new active input field at the end of #input-container.
            return HTMLResponse(content=f"""
            <div class="submitted-entry">
                <div class="input-entry">
                    <input type="text" name="user_input_readonly" value="{safe_input}" readonly />
                </div>
                <div class="status-message">
                    {updated_status}
                </div>
                {bazel_html}
            </div>
            <div id="input-container" hx-swap-oob="beforeend">
                <div class="input-entry" id="current-input-form">
                    <form hx-post="/submit_input" hx-target="#current-input-form" hx-swap="outerHTML">
                        <input type="text" name="user_input" placeholder="Enter more thoughts here..." autocomplete="off" autofocus />
                    </form>
                </div>
            </div>
            """)
        except Exception as e:
            print(f"Error recording input: {e}", file=sys.stderr)
            return HTMLResponse(content=f"Error recording input: {html.escape(str(e), quote=True)}", status_code=500)

    port = get_free_port()
    print(f"Starting Repobook server on http://127.0.0.1:{port}")
    uvicorn.run(app, host="127.0.0.1", port=port)

if __name__ == "__main__":
    main()
