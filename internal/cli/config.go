package cli

import (
	"context"
	"fmt"
	"path/filepath"

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

func chooseEditor(lookup func(string) (string, bool)) string {
	for _, name := range []string{"VISUAL", "EDITOR"} {
		if editor, ok := lookup(name); ok && editor != "" {
			return editor
		}
	}

	return fallbackEditor
}
