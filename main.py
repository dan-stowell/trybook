import os
import sys
import socket
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

    # Get current branch and commit info
    try:
        current_branch = repo.active_branch.name
        latest_commit_message = repo.head.commit.message.strip()
    except TypeError: # Detached HEAD state
        current_branch = "detached HEAD"
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
            <style>
                body {{ font-family: sans-serif; margin: 2em; }}
                h1 {{ color: #333; }}
                p {{ color: #666; font-size: 0.9em; }}
            </style>
        </head>
        <body>
            <h1>{repo_name}</h1>
            <p><strong>Branch:</strong> {new_branch_name}</p>
            <p><strong>Latest Commit:</strong> {latest_commit_message}</p>
        </body>
        </html>
        """
        return HTMLResponse(content=html_content)

    port = get_free_port()
    print(f"Starting Repobook server on http://127.0.0.1:{port}")
    uvicorn.run(app, host="127.0.0.1", port=port)

if __name__ == "__main__":
    main()
