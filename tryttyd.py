# tryttyd.py
# Single-file FastAPI + HTMX app that launches a new tmux session per command
# and serves a per-session ttyd instance. Each submission replaces the input
# with a read-only command + ttyd link, then appends a fresh input below.

import html
import shutil
import socket
import shlex
import subprocess
import uuid
from datetime import datetime
from typing import Dict, Any

from fastapi import FastAPI, Form, Request
from fastapi.responses import HTMLResponse, PlainTextResponse

app = FastAPI()

# In-memory registry of sessions -> metadata (simple demo; not persistent)
SESSIONS: Dict[str, Dict[str, Any]] = {}

HTMX = "https://unpkg.com/htmx.org@1.9.12"

BASE_HTML = """<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1" />
  <title>Command → tmux → ttyd</title>
  <script src="{htmx}"></script>
  <style>
    :root {{
      --fg: #111;
      --bg: #fafafa;
      --muted: #666;
      --border: #ddd;
      --accent: #0b5cff;
    }}
    body {{
      margin: 0; padding: 2rem; color: var(--fg); background: var(--bg);
      font-family: ui-sans-serif, system-ui, -apple-system, Segoe UI, Roboto, Helvetica, Arial, "Apple Color Emoji","Segoe UI Emoji";
      line-height: 1.4;
    }}
    h1 {{ margin: 0 0 1rem 0; font-size: 1.25rem; }}
    .wrap {{ max-width: 860px; margin: 0 auto; }}
    .hint {{ color: var(--muted); margin-bottom: 1rem; }}
    form.cmd-form {{
      display: flex; gap: .5rem; align-items: center; margin: 0; 
    }}
    input.cmd {{
      flex: 1 1 auto;
      font: 600 18px/1.2 ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", "Courier New", monospace;
      padding: 0.85rem 1rem;
      border: 1px solid var(--border);
      border-radius: 12px;
      outline: none;
    }}
    input.cmd:focus {{ border-color: var(--accent); box-shadow: 0 0 0 3px rgba(11,92,255,0.15); }}
    button.run {{
      padding: 0.85rem 1rem;
      border: 1px solid var(--border);
      background: white;
      border-radius: 12px;
      cursor: pointer;
      font-weight: 600;
    }}
    button.run:hover {{ border-color: var(--accent); color: var(--accent); }}
    .entry {{
      padding: 1rem; border: 1px solid var(--border); border-radius: 12px; background: white;
    }}
    .entry + .entry {{ margin-top: 1rem; }}
    .row {{ display: flex; align-items: center; gap: .75rem; flex-wrap: wrap; }}
    .label {{ color: var(--muted); font-size: .9rem; }}
    .ro {{
      width: 100%;
      font: 600 16px/1.2 ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", "Courier New", monospace;
      padding: .6rem .8rem;
      border: 1px dashed var(--border);
      border-radius: 10px;
      background: #fcfcfc;
    }}
    a.session {{
      font-weight: 700; text-decoration: none; color: var(--accent);
      word-break: break-all;
    }}
    .meta {{ color: var(--muted); font-size: .85rem; }}
  </style>
</head>
<body>
  <div class="wrap">
    <h1>Command → tmux → ttyd</h1>
    <p class="hint">Enter a shell command. The server creates a new <b>tmux</b> session running the command and launches <b>ttyd</b> attached to that session. The page shows a read-only copy of the command and a link to the live terminal, then appends a fresh prompt.</p>

    <!-- First prompt -->
    {prompt_html}

    <!-- History gets appended via HTMX; entries are independent blocks -->
    <div id="history" hx-swap-oob="beforeend"></div>
  </div>
</body>
</html>
"""

def prompt_form_html() -> str:
    # The form replaces itself on submit with the returned HTML (outerHTML swap).
    # The returned HTML includes a read-only entry + a brand-new form appended below.
    return """
<div id="prompt-block" class="entry">
  <form class="cmd-form" hx-post="/run" hx-target="#prompt-block" hx-swap="outerHTML">
    <input class="cmd" type="text" name="cmd" placeholder="e.g., bash, htop, python3 -i, claude, node, etc." autofocus required />
    <button class="run" type="submit">Run</button>
  </form>
</div>
""".strip()


def find_free_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def ensure_binaries() -> None:
    # Raise a clear error if tmux or ttyd is missing.
    for bin_name in ("tmux", "ttyd", "bash"):
        if shutil.which(bin_name) is None:
            raise FileNotFoundError(f"Required binary not found on PATH: {bin_name}")


def start_tmux_session(session: str, cmd: str) -> None:
    # Set remain-on-exit globally to preserve panes after process exit.
    subprocess.run(["tmux", "set-option", "-g", "remain-on-exit", "on"], check=False)

    # Create a detached session that runs the command via bash -lc '<cmd>' to respect shell features.
    # Use shlex.quote to safely pass the command as a single string to the shell (avoid HTML escaping).
    shell_cmd = f"bash -lc {shlex.quote(cmd)}"
    subprocess.run(["tmux", "new-session", "-d", "-s", session, shell_cmd], check=True)


def start_ttyd_for_session(session: str, port: int):
    # Launch ttyd attached to the tmux session; run in background.
    # stdout/stderr are suppressed to keep the API responsive.
    return subprocess.Popen(
        ["ttyd", "-p", str(port), "tmux", "attach", "-t", session],
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
        start_new_session=True,
    )


def entry_block_html(command: str, url: str, session: str, created: str) -> str:
    # A read-only snapshot of the command and a link to the ttyd session.
    esc_cmd = html.escape(command)
    esc_url = html.escape(url)
    esc_sess = html.escape(session)
    esc_created = html.escape(created)
    return f"""
<div class="entry">
  <div class="row">
    <span class="label">Command</span>
    <input class="ro" type="text" value="{esc_cmd}" readonly />
  </div>
  <div class="row" style="margin-top:.5rem;">
    <span class="label">Session</span>
    <code>{esc_sess}</code>
  </div>
  <div class="row" style="margin-top:.5rem;">
    <a class="session" href="{esc_url}" target="_blank" rel="noopener noreferrer">{esc_url}</a>
  </div>
  <div class="meta" style="margin-top:.5rem;">Launched: {esc_created}</div>
</div>
""".strip()


def new_prompt_block_html() -> str:
    # A fresh prompt appended after the read-only entry.
    return f"""
<div id="prompt-block" class="entry">
  <form class="cmd-form" hx-post="/run" hx-target="#prompt-block" hx-swap="outerHTML">
    <input class="cmd" type="text" name="cmd" placeholder="Run another command…" autofocus required />
    <button class="run" type="submit">Run</button>
  </form>
</div>
""".strip()


@app.get("/", response_class=HTMLResponse)
async def index():
    return BASE_HTML.format(htmx=HTMX, prompt_html=prompt_form_html())


@app.post("/run", response_class=HTMLResponse)
async def run(request: Request, cmd: str = Form(...)):
    # Prepare environment and session
    try:
        ensure_binaries()
    except FileNotFoundError as e:
        # Replace the form with an error block + a new prompt to try again
        esc = html.escape(str(e))
        return f"""
<div class="entry">
  <div class="row"><b>Error:</b> {esc}</div>
</div>
{new_prompt_block_html()}
""".strip()

    session = f"sess-{uuid.uuid4().hex[:8]}"
    port = find_free_port()

    try:
        start_tmux_session(session, cmd)
    except subprocess.CalledProcessError as e:
        esc = html.escape(str(e))
        return f"""
<div class="entry">
  <div class="row"><b>Failed to start tmux session:</b> {esc}</div>
</div>
{new_prompt_block_html()}
""".strip()

    # Launch ttyd attached to the session
    try:
        proc = start_ttyd_for_session(session, port)
    except Exception as e:
        esc = html.escape(str(e))
        return f"""
<div class="entry">
  <div class="row"><b>Failed to start ttyd:</b> {esc}</div>
</div>
{new_prompt_block_html()}
""".strip()

    hostname = request.url.hostname or "127.0.0.1"
    url = f"http://{hostname}:{port}/"
    created = datetime.now().strftime("%Y-%m-%d %H:%M:%S")

    # Track metadata (best-effort)
    SESSIONS[session] = {"port": port, "pid": proc.pid, "cmd": cmd, "created": created}

    # Replace the current prompt with a read-only entry + new prompt appended under it.
    return "\n".join(
        [
            entry_block_html(cmd, url, session, created),
            new_prompt_block_html(),
        ]
    )


@app.get("/healthz", response_class=PlainTextResponse)
async def healthz():
    return "ok"

if __name__ == "__main__":
    import uvicorn
    uvicorn.run(
        "tryttyd:app",
        host="127.0.0.1",
        port=8000,
        reload=True,
    )
