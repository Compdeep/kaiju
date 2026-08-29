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
2. `bash` — `tmux send-keys -t work "npm run dev" Enter`
3. `bash` — `sleep 3 && tmux capture-pane -t work -p`

These steps pass no data to each other — the session name is the only thing they share, and you already know it. What they need is order, and that is what `depends_on` is for. Use it here.

### Send input to an interactive prompt

1. `bash` — `tmux send-keys -t work "y" Enter`
2. `bash` — `sleep 1 && tmux capture-pane -t work -p`, ordered after it with `depends_on`

### Monitor a long-running process

1. `bash` — `tmux capture-pane -t work -p | tail -20`

No dependencies — can run standalone to check current state.

### Multiple panes in parallel

1. `bash` — `tmux split-window -t work -h`
2. `bash` — `tmux send-keys -t work.0 "make build" Enter`, ordered after the split
3. `bash` — `tmux send-keys -t work.1 "make test" Enter`, ordered after the split

Steps 2 and 3 both wait on the split and neither waits on the other, so they run at the same time in separate panes.

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
