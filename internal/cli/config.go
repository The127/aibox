package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/urfave/cli/v3"

	"github.com/the127/aibox/internal/config"
	"github.com/the127/aibox/internal/project"
)

const fallbackEditor = "vi"

func configCommand(deps dependencies) *cli.Command {
	return &cli.Command{
		Name:  "config",
		Usage: "the settings of the project in the current folder",
		Commands: []*cli.Command{{
			Name:  "edit",
			Usage: "open the config file in the editor, like git: $VISUAL, then $EDITOR, then vi",
			Flags: []cli.Flag{
				&cli.StringFlag{Name: "editor", Usage: "the editor to use instead, run through the shell, so it may have arguments"},
			},
			Action: func(_ context.Context, cmd *cli.Command) error {
				return editConfig(deps, cmd.String("editor"))
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

	_, err = config.Load(p.Config)

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

// runEditor runs the editor on the file through the shell, so that an
// editor setting with arguments works, and waits for it whatever happens to
// aibox meanwhile, because the person is in the editor.
func runEditor(editor, path string) error {
	cmd := exec.Command("/bin/sh", "-c", editor+` "$0"`, path) //nolint:gosec // the editor is the choice of the person
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}
