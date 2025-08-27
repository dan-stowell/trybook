# tryttyd.py
# Single-file FastAPI + HTMX app that launches a new tmux session per command
# and serves a per-session ttyd instance. Each submission replaces the input
# with a read-only command + ttyd link, then appends a fresh input below.

import html
import shutil
import socket
import os
import signal
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
    /* Status-based backgrounds */
    .entry.state-running {{ background: #fff7e6; /* neutral orange */ }}
    .entry.state-success {{ background: #f0fff4; /* neutral green */ }}
    .entry.state-failed  {{ background: #fff5f5; /* neutral red */ }}
    .meta.ok {{ color: #2f9e44; font-weight: 600; }}
    .meta.err {{ color: #c92a2a; font-weight: 600; }}
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
    # Run the command, record its exit code, then keep the pane alive with an interactive bash.
    # This lets ttyd stay attached even if the command exits immediately.
    status_file = f"/tmp/{session}.status"
    wrapper = f"""
{cmd}
code=$?
printf %d "$code" > {shlex.quote(status_file)}
exec bash
""".strip()
    shell_cmd = f"bash -lc {shlex.quote(wrapper)}"
    subprocess.run(["tmux", "new-session", "-d", "-s", session, shell_cmd], check=True)
    # Pane will remain alive due to 'exec bash'; keep remain-on-exit off to avoid dead-pane messages.
    subprocess.run(["tmux", "set-option", "-t", session, "remain-on-exit", "off"], check=False)


def start_ttyd_for_session(session: str, port: int):
    # Launch ttyd attached to the tmux session; run in background.
    # stdout/stderr are suppressed to keep the API responsive.
    return subprocess.Popen(
        ["ttyd", "-p", str(port), "tmux", "attach", "-t", session],
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
        start_new_session=True,
    )

def tmux_pane_status(session: str) -> Dict[str, Any]:
    """
    Determine command status.
    Prefer a per-session status file written by our wrapper; otherwise fall back to tmux pane state.
    Returns dict with keys: state in {"running","success","failed"}, label, code.
    """
    # First, check if our wrapper has written an exit status.
    status_file = f"/tmp/{session}.status"
    if os.path.exists(status_file):
        try:
            with open(status_file) as f:
                txt = (f.read() or "").strip()
                code = int(txt) if txt != "" else -1
        except Exception:
            code = -1
        if code == 0:
            return {"state": "success", "label": "Succeeded", "code": 0}
        return {"state": "failed", "label": f"Failed ({code})" if code >= 0 else "Exited", "code": code}

    # Fallback: ask tmux about the pane state.
    try:
        fmt = "#{pane_dead} #{?#{pane_dead},#{pane_dead_status},-1}"
        out = subprocess.check_output(
            ["tmux", "display-message", "-p", "-t", session, fmt],
            stderr=subprocess.DEVNULL,
        )
        parts = out.decode().strip().split()
        dead = int(parts[0]) if parts else 0
        status = int(parts[1]) if len(parts) > 1 else -1
    except Exception:
        # Session missing or tmux error -> unknown; treat as finished
        return {"state": "failed", "label": "Exited", "code": -1}

    if dead == 0:
        return {"state": "running", "label": "Running…", "code": None}
    if status == 0:
        return {"state": "success", "label": "Succeeded", "code": 0}
    if status > 0:
        return {"state": "failed", "label": f"Failed ({status})", "code": status}
    return {"state": "failed", "label": "Exited", "code": status}

@app.get("/entry/{session}", response_class=HTMLResponse)
async def entry_view(request: Request, session: str):
    meta = SESSIONS.get(session)
    if not meta:
        esc = html.escape(session)
        return f'<div class="entry state-failed"><div class="row"><b>Unknown session:</b> {esc}</div></div>'
    hostname = request.url.hostname or "127.0.0.1"
    url = f"http://{hostname}:{meta['port']}/"
    st = tmux_pane_status(session)

    # Keep ttyd alive after the command finishes so the terminal remains accessible.

    return entry_block_html(meta["cmd"], url, session, meta["created"], state=st["state"], status_label=st["label"])


def entry_block_html(command: str, url: str, session: str, created: str, state: str = "running", status_label: str = "Running…") -> str:
    # A read-only snapshot of the command and a link to the ttyd session, with live status styling.
    esc_cmd = html.escape(command)
    esc_url = html.escape(url)
    esc_sess = html.escape(session)
    esc_created = html.escape(created)
    esc_status = html.escape(status_label)

    state_class = {
        "running": "state-running",
        "success": "state-success",
        "failed": "state-failed",
    }.get(state, "state-running")

    meta_class = "ok" if state == "success" else ("err" if state == "failed" else "")

    # Only poll while running; once finished, the returned block omits hx-get to stop polling.
    hx_attrs = f' hx-get="/entry/{esc_sess}" hx-trigger="load, every 1s" hx-swap="outerHTML"' if state == "running" else ""

    # Keep the live link available even after the command ends (pane remains alive with exec bash).
    link_html = f'<a class="session" href="{esc_url}" target="_blank" rel="noopener noreferrer">{esc_url}</a>'

    return f"""
<div id="entry-{esc_sess}" class="entry {state_class}"{hx_attrs}>
  <div class="row">
    <span class="label">Command</span>
    <input class="ro" type="text" value="{esc_cmd}" readonly />
  </div>
  <div class="row" style="margin-top:.5rem;">
    <span class="label">Session</span>
    <code>{esc_sess}</code>
  </div>
  <div class="row" style="margin-top:.5rem;">
    <span class="label">Status</span>
    <span class="meta {meta_class}">{esc_status}</span>
  </div>
  <div class="row" style="margin-top:.5rem;">
    {link_html}
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
