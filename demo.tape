# Pylon demo -- generate with: vhs demo.tape
#
# Prerequisites:
#   - pylon binary built (go build -o pylon .)
#   - at least one pylon configured (pylon construct my-sentry --from sentry)
#   - some job history for pylon jobs to show data
#
# Install VHS: go install github.com/charmbracelet/vhs@latest

Output demo.gif

Set Shell "bash"
Set FontSize 14
Set Width 1100
Set Height 600
Set Theme "Catppuccin Mocha"
Set Padding 20
Set TypingSpeed 40ms

# --- construct from template ---

Type "pylon construct my-sentry --from sentry"
Enter
Sleep 2s

# --- list configured pylons ---

Type "pylon list"
Enter
Sleep 2s

# --- start the daemon ---

Type "pylon start"
Enter
Sleep 3s
Ctrl+C
Sleep 500ms

# --- show job history ---

Type "pylon jobs"
Enter
Sleep 2s

# --- flash the TUI ---

Type "pylon nexus"
Enter
Sleep 3s
Type "q"

Sleep 1s
