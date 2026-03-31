---
name: tmux
description: "Remote-control tmux sessions for interactive CLIs by sending keystrokes and reading pane output. Use when a command needs interactive input, long-running processes, or terminal UI inspection."
metadata:
  requires:
    bins: ["tmux"]
---

## When to Use

Use when you need to drive interactive CLI tools, monitor long-running processes, or inspect terminal UI state. Tmux lets you send keystrokes and read pane output without a real TTY.

Do NOT use for simple non-interactive commands — use `bash` directly.

## Planning Guidance

### Start a session and run a command

1. `bash` — `tmux new-session -d -s work -x 200 -y 50`
2. `bash` — `tmux send-keys -t work "npm run dev" Enter` (depends on step 0)
3. `bash` — `sleep 3 && tmux capture-pane -t work -p` (depends on step 1)

### Send input to an interactive prompt

1. `bash` — `tmux send-keys -t work "y" Enter`
2. `bash` — `sleep 1 && tmux capture-pane -t work -p` (depends on step 0)

### Monitor a long-running process

1. `bash` — `tmux capture-pane -t work -p | tail -20`

No dependencies — can run standalone to check current state.

### Multiple panes in parallel

1. `bash` — `tmux split-window -t work -h`
2. `bash` — `tmux send-keys -t work.0 "make build" Enter` (depends on step 0)
3. `bash` — `tmux send-keys -t work.1 "make test" Enter` (depends on step 0, parallel with step 1)

### Key patterns

| Action | Command |
|--------|---------|
| Create session | `tmux new-session -d -s NAME -x 200 -y 50` |
| Send keys | `tmux send-keys -t NAME "command" Enter` |
| Read output | `tmux capture-pane -t NAME -p` |
| Send Ctrl-C | `tmux send-keys -t NAME C-c` |
| Kill session | `tmux kill-session -t NAME` |
| List sessions | `tmux list-sessions` |

### What NOT to do

- Don't read pane output immediately after send-keys — add a `sleep` for the command to produce output
- Don't use tmux for commands that complete instantly — use `bash` directly
- Don't forget to kill sessions when done — they persist and consume resources
