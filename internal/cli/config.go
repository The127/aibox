package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/urfave/cli/v3"

	"github.com/the127/aibox/internal/config"
	"github.com/the127/aibox/internal/project"
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
			},
			Action: func(_ context.Context, cmd *cli.Command) error {
				return editConfig(deps, cmd.String("editor"))
			},
		}, {
			Name:  "suggest",
			Usage: "suggest presets for the allow list from the names of the files in the current folder, and add them once you say yes",
			Action: func(context.Context, *cli.Command) error {
				return suggestConfig(deps)
			},
		}},
	}
}

func editConfig(deps dependencies, editor string) error {
	cwd, err := deps.getwd()
	if err != nil {
		return fmt.Errorf("find the current folder: %w", err)
	}

	aibox, err := deps.aiboxDir()
	if err != nil {
		return fmt.Errorf("find the aibox folder: %w", err)
	}

	p, err := project.Open(filepath.Join(aibox, "projects"), cwd)
	if err != nil {
		return fmt.Errorf("open the project folder: %w", err)
	}

	// a new project gets the default file to start from, and a file that
	// does not parse is what the editor is for
	if err := config.EnsureDefault(p.Config); err != nil {
		return err
	}

	if editor == "" {
		editor = chooseEditor(deps.lookupEnv)
	}

	if err := deps.edit(editor, p.Config); err != nil {
		return fmt.Errorf("%s: %w", editor, err)
	}

	_, err = loadConfig(deps, p.Config, aibox)

	return err
}

func suggestConfig(deps dependencies) error {
	cwd, aibox, err := folders(deps)
	if err != nil {
		return err
	}

	p, err := project.Open(filepath.Join(aibox, "projects"), cwd)
	if err != nil {
		return fmt.Errorf("open the project folder: %w", err)
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

	suggestions := config.Suggest(files, cfg.Allow)
	if len(suggestions) == 0 {
		_, _ = fmt.Fprintf(deps.stderr, "aibox: nothing to suggest. aibox looks for these files in the current folder, and the config allows the presets of those it found: %s\n", strings.Join(config.Markers(), ", "))

		return nil
	}

	names := make([]string, 0, len(suggestions))

	for _, s := range suggestions {
		names = append(names, s.Preset)
		_, _ = fmt.Fprintf(deps.stderr, "preset:%s, for %s, allows %s\n", s.Preset, strings.Join(s.Files, ", "), strings.Join(s.Hosts, ", "))
	}

	_, _ = fmt.Fprintln(deps.stderr, "Anyone can upload files to package registries like these, so a program in the VM can send data there too.")

	if !deps.stdinIsTerminal() {
		return printPresets(deps, names)
	}

	if err := confirm(deps, "Add them to "+p.Config+"?", false); err != nil {
		return err
	}

	content, err := os.ReadFile(p.Config)
	if err != nil {
		return fmt.Errorf("read %s: %w", p.Config, err)
	}

	added, err := config.AddPresets(content, names)
	if errors.Is(err, config.ErrNoAllowList) {
		return printPresets(deps, names)
	}

	if err != nil {
		return fmt.Errorf("add to %s: %w", p.Config, err)
	}

	if err := replaceFile(p.Config, added); err != nil {
		return fmt.Errorf("write %s: %w", p.Config, err)
	}

	_, _ = fmt.Fprintln(deps.stderr, "aibox: added them. Start aibox again for them to apply.")

	return nil
}

// printPresets tells how to add the presets by hand.
func printPresets(deps dependencies, names []string) error {
	_, _ = fmt.Fprintln(deps.stderr, "Add them to allow with aibox config edit:")

	for _, name := range names {
		_, _ = fmt.Fprintf(deps.stderr, "  - preset:%s\n", name)
	}

	return nil
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
