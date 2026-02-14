Output "docs/demos/favorites-filter.gif"

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

Type "./bin/tmux-ssh-manager --tui-source yaml --config ./docs/demos/hosts.demo.yaml"
Enter

Sleep 800ms
Type "/"
Sleep 150ms
Type "demo"
Sleep 800ms

Down
Sleep 200ms
Type "f"
Sleep 800ms
Type "F"
Sleep 1200ms
Type "A"
Sleep 500ms
Type "Esc"
Sleep 200ms
Type "q"
Sleep 300ms
