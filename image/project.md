The project:

- /project is the project folder from the host. Changes there are real
  changes to the person's files. Many small files are slow there, so put
  scratch files in /tmp.
- Do not change .git/config or .git/hooks of the project unless asked.
  git on the host runs what they say. Use `git -c key=value` for one
  command instead.
