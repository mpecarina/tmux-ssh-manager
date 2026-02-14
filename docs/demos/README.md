# Demos

GitHub's Markdown viewer can't play terminal recordings directly.

This folder keeps:

- `*.tape` (VHS source scripts) and rendered `*.gif`/`*.mp4`/`*.webm` outputs (preferred)
- `*.cast` (asciinema recordings; use for local playback)

## View on GitHub

- GIFs render inline in Markdown.
- MP4/WEBM files can be downloaded; GitHub may not inline-play them in Markdown.

### Rendered GIFs

#### Basic flow

![](./basic-flow.gif)

#### Favorites filter

![](./favorites-filter.gif)

## Local playback

Asciinema:

```sh
asciinema play docs/demos/basic-flow.cast
asciinema play docs/demos/favorites-filter.cast
```

VHS:

```sh
brew install vhs
go build -o ./bin/tmux-ssh-manager ./cmd/tmux-ssh-manager

# Note: demos that open tmux windows must be rendered from inside tmux.
vhs docs/demos/basic-flow.tape --output docs/demos/basic-flow.gif
vhs docs/demos/favorites-filter.tape --output docs/demos/favorites-filter.gif
```
