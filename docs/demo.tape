# Records docs/demo.gif with vhs (https://github.com/charmbracelet/vhs):
#
#   vhs docs/demo.tape
#
# Run it from the root of this repository, after aibox run has been logged in
# here once. The recording shows a real Claude Code session.

Output docs/demo.gif

Set Shell "bash"
Set FontSize 16
Set Width 1200
Set Height 700
Set Padding 20
Set TypingSpeed 60ms

Hide
Type "export PS1='$ ' && clear"
Enter
Show

Type "aibox run"
Sleep 500ms
Enter
Wait+Screen@30s /shift\+tab|for shortcuts/
Sleep 1s

Type "what does this project do? two sentences"
Sleep 500ms
Enter
Wait+Screen@120s / · done /
Sleep 3s

Type "/exit"
Sleep 500ms
Enter
Sleep 2s
