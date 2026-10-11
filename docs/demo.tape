# Records docs/demo.gif with vhs (https://github.com/charmbracelet/vhs):
#
#   vhs docs/demo.tape
#
# Run it from the root of this repository, after aibox run has been logged in
# here once. The recording shows a real Claude Code session and a real task.
#
# The task needs ANTHROPIC_API_KEY under env in the project config, and the
# key in the login keychain under the service aibox-api-key. aibox run gets
# the variable empty, so Claude Code there uses the login. Each recording
# costs a few cents at API prices.

Output docs/demo.gif

Set Shell "bash"
Set FontSize 16
Set Width 1200
Set Height 800
Set Padding 20
Set TypingSpeed 40ms

Hide
Type "export PS1='$ ' ANTHROPIC_API_KEY= && git checkout -q justfile && git branch -q -D aibox/task-demo 2>/dev/null; clear"
Enter
Show

# aibox run: Claude Code changes the project folder right away.
Type "aibox run"
Sleep 300ms
Enter
Wait+Screen@30s /shift\+tab|for shortcuts/
Sleep 1s

Type "add a one-line comment at the top of justfile saying what it is for, reply in one sentence"
Sleep 300ms
Enter
Wait+Screen@90s / · done /
Sleep 2s

Type "/exit"
Sleep 300ms
Enter
Sleep 3s

Type "git diff justfile"
Enter
Sleep 3s

Hide
Type "git checkout -q justfile && export ANTHROPIC_API_KEY=$(security find-generic-password -s aibox-api-key -w) && clear"
Enter
Show

# aibox task: Claude Code works alone on a clone and hands back its commits.
Type `aibox task --model sonnet "add a just recipe that records docs/demo.tape with vhs, commit it, and reply in one sentence"`
Sleep 300ms
Enter
Wait+Screen@120s /fetch them with/
Sleep 5s

Hide
Type "git -c transfer.fsckObjects=true fetch -q $(ls -td ~/.aibox/projects/*/tasks/*/ | head -1)changes.bundle aibox/task:aibox/task-demo && clear"
Enter
Show

Type "git log --oneline -1 aibox/task-demo"
Enter
Sleep 1s
Type "git diff --stat HEAD aibox/task-demo"
Enter
Sleep 3s
