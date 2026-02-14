Output "docs/demos/basic-flow.gif"

Set FontSize 18
Set Width 1000
Set Height 600
Set TypingSpeed 80ms
Set PlaybackSpeed 1.0
Set CursorBlink false

Hide
Type "go build -o ./bin/tmux-ssh-manager ./cmd/tmux-ssh-manager"
Enter
Sleep 2s
Show

Type "./bin/tmux-ssh-manager --config docs/demos/hosts.demo.yaml"
Enter

Sleep 800ms
Type "demo"
Sleep 500ms
Down
Sleep 400ms
Down
Sleep 400ms
Enter
Sleep 1s

# Return to the list and exit cleanly.
Escape
Sleep 300ms
Type "q"
