## Docs

This folder contains reproducible demo recordings (VHS tapes) and rendered GIF outputs.

### Demos

- `docs/demos/README.md` - How to view demos on GitHub + locally.
- `docs/demos/basic-flow.gif` - Basic flow demo (rendered from `docs/demos/basic-flow.tape`).
- `docs/demos/favorites-filter.gif` - Favorites filter demo (rendered from `docs/demos/favorites-filter.tape`).

### Regenerate recordings (VHS)

Prereqs (macOS):

- `vhs`
- `tmux` (recommended: render from inside tmux so window/pane actions behave as expected)
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
go build -o ./bin/tmux-ssh-manager ./cmd/tmux-ssh-manager

vhs docs/demos/basic-flow.tape --output docs/demos/basic-flow.gif
vhs docs/demos/favorites-filter.tape --output docs/demos/favorites-filter.gif
```

Notes:

- These demos are recorded against the Bubble Tea selector only; they do not require real SSH access.
- Avoid typing any real hostnames, usernames, or passwords while recording.

### VHS (optional)

We commit `docs/demos/*.tape` sources and the rendered `docs/demos/*.gif` outputs. Re-run the commands above to regenerate the GIFs after changing a tape.
