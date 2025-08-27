#!/usr/bin/env bash
set -euo pipefail

SESSION="claude"
# Whatever you want running in the pane:
APP_CMD='claude'   # e.g. your CLI / REPL / script

# Create the session once, running your command
if ! tmux has-session -t "$SESSION" 2>/dev/null; then
  # keep the pane open even if APP_CMD exits (optional but useful)
  tmux set-option -g remain-on-exit on
  tmux new-session -d -s "$SESSION" "$APP_CMD"
fi

# ttyd will spawn a *tmux client* per web connection,
# all attaching to the same session/pane.
exec ttyd -p 8080 --writable --browser tmux attach -t "$SESSION"
