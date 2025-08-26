"""
trybook_htmx.py - HTMX + FastAPI implementation (~300 lines total)
Run with: uvicorn trybook_htmx:app --reload --host 127.0.0.1 --port 8080
"""

import asyncio
import subprocess
import json
import uuid
from pathlib import Path
from datetime import datetime
from typing import Dict, Any
import os

from fastapi import FastAPI, Form, Request, Response
from fastapi.responses import HTMLResponse, RedirectResponse

app = FastAPI()

WORK_DIR = Path.home() / ".trybook"
WORK_DIR.mkdir(exist_ok=True)

# Store active tasks
tasks: Dict[str, Dict[str, Any]] = {}

# Base template with HTMX
BASE_HTML = """<!DOCTYPE html>
<html>
<head>
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <title>trybook</title>
    <script src="https://unpkg.com/htmx.org@1.9.10"></script>
    <style>
        body {{ padding: 1rem; font-family: system-ui; }}
        .prompt-box {{ padding: 0.5rem 1rem; border: 1px solid #64B5F6; 
                      border-radius: 4px; background: #E3F2FD; 
                      font-style: italic; color: #3F51B5; margin: 1rem 0; }}
        .llm-box {{ padding: 0.5rem 1rem; border: 1px solid #ddd; 
                   border-radius: 4px; background: #fcfcfc; 
                   margin: 0.5rem 0; position: relative; }}
        .llm-title {{ position: absolute; bottom: 0.5rem; right: 0.5rem; 
                     font-size: 0.75em; color: #888; 
                     background: rgba(255, 255, 255, 0.7); 
                     padding: 0.2em 0.5em; border-radius: 3px; }}
        .running {{ background: #fff3e0; border-color: #ff9800; }}
        .success {{ background: #e8f5e9; border-color: #4caf50; }}
        .error {{ background: #ffebee; border-color: #f44336; }}
        pre {{ white-space: pre-wrap; font-family: monospace; margin: 0; }}
        .suggestions div {{ padding: 0.5rem; border: 1px solid #ddd; 
                          cursor: pointer; background: white; }}
        .suggestions div:hover {{ background: #f7f7f7; }}
        form {{ display: flex; gap: 0.5rem; margin: 1rem 0; }}
        input[type="text"], input[type="url"] {{ 
            flex-grow: 1; font-size: 1.25rem; padding: 0.6rem 0.75rem; 
        }}
        button {{ font-size: 1.1rem; padding: 0.6rem 1rem; cursor: pointer; }}
    </style>
</head>
<body>
    {content}
</body>
</html>"""

@app.get("/", response_class=HTMLResponse)
async def index(repo: str = ""):
    return BASE_HTML.format(content=f"""
        <h1>trybook</h1>
        <form hx-get="/repo-redirect" hx-target="body">
            <input type="url" name="repo" placeholder="github repo" 
                   value="{repo}" autofocus
                   hx-get="/api/search" 
                   hx-trigger="input changed delay:250ms" 
                   hx-target="#suggestions">
            <button type="submit">Open</button>
        </form>
        <div id="suggestions" class="suggestions"></div>
    """)

@app.get("/api/search")
async def search(repo: str = ""):
    if len(repo) < 2:
        return HTMLResponse("")
    
    # Run GitHub search
    try:
        result = await asyncio.create_subprocess_exec(
            "gh", "search", "repos", repo, "--limit", "5", 
            "--json", "fullName,description,url,stargazersCount",
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            env={**dict(os.environ), "GH_NO_UPDATE_NOTIFIER": "1"}
        )
        stdout, _ = await result.communicate()
        repos = json.loads(stdout)[:5]
        
        return HTMLResponse("".join(
            f"""<div hx-get="/repo/{r['fullName']}" hx-target="body" hx-push-url="true">
                <div style="font-weight: 600">{r['fullName']}</div>
                <div style="color: #555; font-size: 0.9rem">{r.get('description', '')}</div>
            </div>""" for r in repos
        ))
    except:
        return HTMLResponse("")

@app.get("/repo-redirect")
async def repo_redirect(repo: str):
    owner, name = parse_github_url(repo)
    return RedirectResponse(f"/repo/{owner}/{name}")

@app.get("/repo/{owner}/{repo}", response_class=HTMLResponse)
async def repo_page(owner: str, repo: str):
    repo_dir = WORK_DIR / "clone" / owner / repo
    
    # Clone or pull repository
    commit_hash = await manage_repo(f"{owner}/{repo}", repo_dir)
    
    return BASE_HTML.format(content=f"""
        <h1>trybook</h1>
        <p>Repository: <strong><a href="https://github.com/{owner}/{repo}">{owner}/{repo}</a></strong></p>
        <p>Cloned Commit: <code>{commit_hash}</code></p>
        
        <form hx-post="/create-notebook/{owner}/{repo}" hx-target="body">
            <button type="submit">Create Notebook</button>
        </form>
        
        <p><a href="/">Back to search</a></p>
    """)

@app.post("/create-notebook/{owner}/{repo}")
async def create_notebook(owner: str, repo: str):
    notebook_name = f"{repo}-{datetime.now().strftime('%Y%m%d')}-{uuid.uuid4().hex[:6]}"
    base_repo_dir = WORK_DIR / "clone" / owner / repo
    worktree_path = WORK_DIR / "worktree" / owner / repo / notebook_name
    
    # Create git worktree
    await asyncio.create_subprocess_exec(
        "git", "worktree", "add", "-b", notebook_name, str(worktree_path),
        cwd=str(base_repo_dir)
    )
    
    return RedirectResponse(f"/notebook/{owner}/{repo}/{notebook_name}", status_code=303)

@app.get("/notebook/{owner}/{repo}/{notebook_name}", response_class=HTMLResponse)
async def notebook_page(owner: str, repo: str, notebook_name: str):
    return BASE_HTML.format(content=f"""
        <h1><a href="https://github.com/{owner}/{repo}">{owner}/{repo}</a> / {notebook_name}</h1>
        
        <div id="task-log"></div>
        
        <form hx-post="/api/run-prompt/{owner}/{repo}/{notebook_name}" 
              hx-target="#task-log" hx-swap="beforeend"
              style="position: fixed; bottom: 0; left: 0; right: 0; 
                     padding: 1rem; background: #f0f0f0; border-top: 1px solid #ccc;">
            <input type="text" name="prompt" placeholder="question? or tell me to do something" required>
            <button type="submit">run</button>
        </form>
    """)

@app.post("/api/run-prompt/{owner}/{repo}/{notebook_name}")
async def run_prompt(owner: str, repo: str, notebook_name: str, prompt: str = Form(...)):
    task_id = str(uuid.uuid4())
    worktree_path = WORK_DIR / "worktree" / owner / repo / notebook_name
    
    # Initialize task
    tasks[task_id] = {
        "prompt": prompt,
        "status": "running",
        "llms": {"gemini": {}, "claude": {}, "codex": {}}
    }
    
    # Start background execution
    asyncio.create_task(execute_llms(task_id, prompt, str(worktree_path)))
    
    # Return initial UI with polling
    return HTMLResponse(f"""
        <div class="prompt-box">{prompt}</div>
        <div id="task-{task_id}" hx-get="/api/poll/{task_id}" 
             hx-trigger="load, every 1s" hx-swap="outerHTML">
            <div class="llm-box running">
                <span class="llm-title">Gemini</span>
                <pre>Starting Gemini task...</pre>
            </div>
            <div class="llm-box running">
                <span class="llm-title">Claude</span>
                <pre>Starting Claude task...</pre>
            </div>
            <div class="llm-box running">
                <span class="llm-title">Codex</span>
                <pre>Starting Codex task...</pre>
            </div>
        </div>
    """)

@app.get("/api/poll/{task_id}")
async def poll_task(task_id: str):
    if task_id not in tasks:
        return HTMLResponse("")
    
    task = tasks[task_id]
    all_done = all(llm.get("done", False) for llm in task["llms"].values())
    
    # Generate HTML for current state
    html = f'<div id="task-{task_id}"'
    if not all_done:
        html += ' hx-get="/api/poll/' + task_id + '" hx-trigger="every 1s" hx-swap="outerHTML"'
    html += '>'
    
    for llm_name in ["gemini", "claude", "codex"]:
        llm_data = task["llms"][llm_name]
        status = llm_data.get("status", "running")
        summary = llm_data.get("summary", "Processing...")
        
        html += f"""
            <div class="llm-box {status}" onclick="this.querySelector('.raw').style.display = 
                 this.querySelector('.raw').style.display === 'none' ? 'block' : 'none'">
                <span class="llm-title">{llm_name.title()}</span>
                <pre>{summary}</pre>
                <pre class="raw" style="display:none; background:#eee; padding:0.5rem; 
                     max-height:200px; overflow-y:auto">{llm_data.get("output", "")}</pre>
            </div>
        """
    
    html += '</div>'
    return HTMLResponse(html)

async def execute_llms(task_id: str, prompt: str, worktree_path: str):
    """Execute all three LLMs concurrently"""
    async def run_llm(name: str):
        # Command mapping
        cmds = {
            "gemini": ["gemini", "--prompt", prompt],
            "claude": ["claude", "--print", prompt],
            "codex": ["codex", "exec", prompt]
        }
        
        try:
            # Run LLM command
            proc = await asyncio.create_subprocess_exec(
                *cmds[name],
                cwd=worktree_path,
                stdout=subprocess.PIPE,
                stderr=subprocess.STDOUT,
                env={**dict(os.environ), "GIT_TERMINAL_PROMPT": "0"}
            )
            
            # Stream output
            output = ""
            while True:
                line = await proc.stdout.readline()
                if not line:
                    break
                output += line.decode()
                tasks[task_id]["llms"][name]["output"] = output
            
            await proc.wait()
            
            # Generate summary
            summary_proc = await asyncio.create_subprocess_exec(
                "llm", "--model", "gpt-5-nano", "-s",
                "Summarize this coding agent output in one sentence. If nothing worth summarizing, respond 'Running...'",
                stdin=subprocess.PIPE,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE
            )
            summary_out, _ = await summary_proc.communicate(output.encode())
            
            tasks[task_id]["llms"][name].update({
                "status": "success" if proc.returncode == 0 else "error",
                "summary": summary_out.decode().strip(),
                "done": True
            })
            
        except Exception as e:
            tasks[task_id]["llms"][name].update({
                "status": "error",
                "output": str(e),
                "summary": f"Error: {e}",
                "done": True
            })
    
    # Run all LLMs concurrently
    await asyncio.gather(
        run_llm("gemini"),
        run_llm("claude"),
        run_llm("codex")
    )

async def manage_repo(repo: str, repo_dir: Path) -> str:
    """Clone or pull repository and return commit hash"""
    ssh_url = f"ssh://git@github.com/{repo}"
    
    if repo_dir.exists():
        # Pull existing repo
        proc = await asyncio.create_subprocess_exec(
            "git", "pull", cwd=str(repo_dir),
            stdout=subprocess.PIPE, stderr=subprocess.PIPE
        )
    else:
        # Clone new repo
        repo_dir.mkdir(parents=True, exist_ok=True)
        proc = await asyncio.create_subprocess_exec(
            "git", "clone", "--depth=1", ssh_url, str(repo_dir),
            stdout=subprocess.PIPE, stderr=subprocess.PIPE
        )
    
    await proc.wait()
    
    # Get commit hash
    proc = await asyncio.create_subprocess_exec(
        "git", "rev-parse", "HEAD", cwd=str(repo_dir),
        stdout=subprocess.PIPE
    )
    stdout, _ = await proc.communicate()
    return stdout.decode().strip()

def parse_github_url(url: str) -> tuple[str, str]:
    """Parse GitHub URL to get owner and repo"""
    url = url.strip().rstrip("/").rstrip(".git")
    for prefix in ["https://github.com/", "http://github.com/", 
                   "ssh://git@github.com/", "git@github.com:", "github.com/"]:
        if url.startswith(prefix):
            url = url[len(prefix):]
            break
    parts = url.split("/")
    return parts[0], parts[1] if len(parts) > 1 else ""

if __name__ == "__main__":
    import uvicorn
    import os
    uvicorn.run(app, host="127.0.0.1", port=8080)
