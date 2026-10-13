package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/urfave/cli/v3"

	"github.com/the127/aibox/internal/config"
)

func configCommand(deps dependencies) *cli.Command {
	return &cli.Command{
		Name:  "config",
		Usage: "the settings of the project in the current folder",
		Commands: []*cli.Command{{
			Name:  "edit",
			Usage: "open the config file in the editor, like git: $VISUAL, then $EDITOR, then " + fallbackEditor,
			Flags: []cli.Flag{
				&cli.StringFlag{Name: "editor", Usage: "the editor to use instead, run through the shell, so it may have arguments"},
				&cli.BoolFlag{Name: "template", Usage: "edit ~/.aibox/config.yaml instead, the config new projects start from"},
			},
			Action: func(_ context.Context, cmd *cli.Command) error {
				return editConfig(deps, cmd.String("editor"), cmd.Bool("template"))
			},
		}, {
			Name:  "suggest",
			Usage: "suggest presets for the allow list from the names of the files in the current folder, and add them once you say yes",
			Action: func(ctx context.Context, _ *cli.Command) error {
				return suggestConfig(ctx, deps)
			},
		}},
	}
}

func editConfig(deps dependencies, editor string, template bool) error {
	cwd, aibox, err := folders(deps)
	if err != nil {
		return err
	}

	path := templateFile(aibox)

	// a new template starts from the default file, and a new project from
	// the template. A file that does not parse is what the editor is for.
	if template {
		if err := os.MkdirAll(aibox, 0o700); err != nil {
			return fmt.Errorf("create %s: %w", aibox, err)
		}

		if err := config.EnsureDefault(path, ""); err != nil {
			return err
		}
	} else {
		p, err := openProject(aibox, cwd)
		if err != nil {
			return err
		}

		if err := config.EnsureDefault(p.Config, path); err != nil {
			return err
		}

		path = p.Config
	}

	if editor == "" {
		editor = chooseEditor(deps.lookupEnv)
	}

	if err := deps.edit(editor, path); err != nil {
		return fmt.Errorf("%s: %w", editor, err)
	}

	_, err = loadConfig(deps, path, aibox)

	return err
}

func suggestConfig(ctx context.Context, deps dependencies) error {
	cwd, aibox, err := folders(deps)
	if err != nil {
		return err
	}

	p, err := openProject(aibox, cwd)
	if err != nil {
		return err
	}

	cfg, err := loadConfig(deps, p.Config, aibox)
	if err != nil {
		return err
	}

	entries, err := os.ReadDir(cwd)
	if err != nil {
		return fmt.Errorf("read the current folder: %w", err)
	}

	// plain files only: a link or a folder of that name shows nothing
	var files []string

	for _, entry := range entries {
		if entry.Type().IsRegular() {
			files = append(files, entry.Name())
		}
	}

	out := newPainter(deps)
	name := filepath.Base(cwd)

	suggestions := config.Suggest(files, cfg.Allow)
	if len(suggestions) == 0 {
		out.printf("Nothing to suggest for %s. aibox looks for these files:\n\n", name)

		for _, line := range wrap(config.Markers(), out.width-2) {
			out.printf("  %s\n", out.paint(line, dim))
		}

		out.printf("\nIt found none of them, or the config allows their tools already.\n")

		return nil
	}

	out.printf("aibox found these tools in %s. The VM can't reach their package registries yet:\n\n", name)

	toolWidth := 0
	for _, s := range suggestions {
		toolWidth = max(toolWidth, len(s.Tool))
	}

	for _, s := range suggestions {
		indent := strings.Repeat(" ", 2+toolWidth+3)
		out.printf("  %s%s   because of %s\n", out.paint(s.Tool, bold, cyan), strings.Repeat(" ", toolWidth-len(s.Tool)), joinAnd(out.each(s.Files, bold)))
		out.printf("%spreset:%s would allow:\n", indent, s.Preset)

		for _, line := range wrap(s.Hosts, out.width-len(indent)-2) {
			out.printf("%s  %s\n", indent, out.paint(line, dim))
		}
	}

	out.printf("\n%s\n\n", out.paint("Anyone can upload to package registries, so a program in the VM could send data there.", yellow))

	if !deps.stdinIsTerminal() {
		return printPresets(out, suggestions)
	}

	var chosen []config.Suggestion

	answers := bufio.NewReader(deps.stdin)

	for _, s := range suggestions {
		yes, err := ask(ctx, out, answers, "Allow the preset for "+s.Tool+"?")
		if err != nil {
			return err
		}

		if yes {
			chosen = append(chosen, s)
		}
	}

	if len(chosen) == 0 {
		out.printf("\nNothing changed.\n")

		return nil
	}

	content, err := os.ReadFile(p.Config)
	if err != nil {
		return fmt.Errorf("read %s: %w", p.Config, err)
	}

	added, err := config.AddPresets(content, presetsOf(chosen))
	if errors.Is(err, config.ErrNoAllowList) {
		out.printf("\n")

		return printPresets(out, chosen)
	}

	if err != nil {
		return fmt.Errorf("add to %s: %w", p.Config, err)
	}

	if err := replaceFile(p.Config, added); err != nil {
		return fmt.Errorf("write %s: %w", p.Config, err)
	}

	tools := make([]string, 0, len(chosen))
	for _, s := range chosen {
		tools = append(tools, s.Tool)
	}

	out.printf("\n%s\n", out.paint("Added the "+plural(len(tools), "preset", "presets")+" for "+joinAnd(tools)+".", green))
	out.printf("Start aibox again for it to apply.\n")

	return nil
}

// ask asks the question and reads the answer from answers: yes for an
// empty line, else yes unless it starts with n. The end of the input is no.
func ask(ctx context.Context, out painter, answers *bufio.Reader, question string) (bool, error) {
	out.printf("%s [Y/n] ", out.paint(question, bold))

	answer, err := readAnswer(ctx, answers)
	if err != nil {
		out.printf("\n")

		return false, err
	}

	if !strings.HasSuffix(answer, "\n") {
		out.printf("\n")

		return false, nil
	}

	answer = strings.ToLower(strings.TrimSpace(answer))

	return !strings.HasPrefix(answer, "n"), nil
}

// printPresets tells how to add the presets of the suggestions by hand.
func printPresets(out painter, suggestions []config.Suggestion) error {
	out.printf("Add them to allow with aibox config edit:\n")

	for _, name := range presetsOf(suggestions) {
		out.printf("  - preset:%s\n", name)
	}

	return nil
}

func presetsOf(suggestions []config.Suggestion) []string {
	names := make([]string, 0, len(suggestions))
	for _, s := range suggestions {
		names = append(names, s.Preset)
	}

	return names
}

// joinAnd joins the words as a list in a sentence: a, b and c.
func joinAnd(words []string) string {
	if len(words) < 2 {
		return strings.Join(words, "")
	}

	return strings.Join(words[:len(words)-1], ", ") + " and " + words[len(words)-1]
}

// wrap puts the words into lines no wider than width, two spaces apart. A
// word wider than width gets a line of its own.
func wrap(words []string, width int) []string {
	var lines []string

	line := ""

	for _, word := range words {
		switch {
		case line == "":
			line = word
		case len(line)+2+len(word) <= width:
			line += "  " + word
		default:
			lines = append(lines, line)
			line = word
		}
	}

	return append(lines, line)
}

// replaceFile writes content to a new file beside path and puts it in
// path's place in one step, so that no aibox reads it half written. A link
// at path, such as into a folder of dotfiles, stays and gets the new file.
func replaceFile(path string, content []byte) error {
	path, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+"-*")
	if err != nil {
		return err
	}

	_, err = tmp.Write(content)
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}

	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}

	if err != nil {
		_ = os.Remove(tmp.Name())
	}

	return err
}

func chooseEditor(lookup func(string) (string, bool)) string {
	for _, name := range []string{"VISUAL", "EDITOR"} {
		if editor, ok := lookup(name); ok && editor != "" {
			return editor
		}
	}

	return fallbackEditor
}
