## Docs

This folder contains reproducible demo recordings (VHS tapes preferred; asciinema fallback).

### Demos

- `docs/demos/README.md` - How to view demos on GitHub + locally.
- `docs/demos/basic-flow.cast` - Placeholder asciicast (regenerate locally; see below).
- `docs/demos/favorites-filter.cast` - Placeholder asciicast (regenerate locally; see below).

### Regenerate recordings (asciinema)

Prereqs (macOS):

- `asciinema`
- `tmux`
- `go` (to build `tmux-ssh-manager`)

Build:

```sh
go build -o ./bin/tmux-ssh-manager ./cmd/tmux-ssh-manager
```

Create a safe local demo config (uses non-routable example domains; does not contain secrets):

```sh
mkdir -p "$HOME/.config/tmux-ssh-manager"
cp ./docs/demos/hosts.demo.yaml "$HOME/.config/tmux-ssh-manager/hosts.yaml"
```

Record (run inside a tmux session; use a large terminal for readability):

```sh
asciinema rec -c "./bin/tmux-ssh-manager --tui-source yaml --config ./docs/demos/hosts.demo.yaml" docs/demos/basic-flow.cast
asciinema rec -c "./bin/tmux-ssh-manager --tui-source yaml --config ./docs/demos/hosts.demo.yaml" docs/demos/favorites-filter.cast
```

Notes:

- These demos are recorded against the Bubble Tea selector only; they do not require real SSH access.
- Avoid typing any real hostnames, usernames, or passwords while recording.

### VHS (optional)

If you have VHS available, prefer committing `docs/demos/*.tape` and rendered outputs (gif/mp4/webm). For now this repo ships asciinema `.cast` recordings; regenerate them locally (interactive) using the steps above.
