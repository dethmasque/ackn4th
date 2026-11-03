package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/dethmasque/ackn4th/internal/config"
)

func configCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "config",
		GroupID: "info",
		Short:   "Show the effective configuration",
		Long: `Print the configuration in effect: config.yaml with every default filled
in, under a comment naming the file. The file lives in the OS's config
directory: $XDG_CONFIG_HOME/ackn4th (or ~/.config/ackn4th) on Linux,
~/Library/Application Support/ackn4th on macOS, %APPDATA%\ackn4th on
Windows. Every setting is explained at
https://github.com/dethmasque/ackn4th/blob/main/internal/config/README.md`,
		Example: `  ackn4th config
  ackn4th config init        # write config.yaml with the defaults, to edit`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := config.Path()
			if err != nil {
				return err
			}
			data, err := yaml.Marshal(a.cfg)
			if err != nil {
				return err
			}
			a.term.Println(a.term.Gray("# " + path))
			fmt.Print(string(data))
			return nil
		},
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "init",
		Short: "Write a config.yaml with the defaults",
		Long: `Write config.yaml with every default, ready to edit. It refuses to
overwrite a config.yaml that exists; ackn4th config shows where it is.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := config.Path()
			if err != nil {
				return err
			}
			if _, err := os.Stat(path); err == nil {
				return fmt.Errorf("%s already exists", path)
			} else if !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			if _, err := config.Save(config.Default()); err != nil {
				return err
			}
			a.term.Println(a.term.Green("✓ "), "wrote ", path)
			return nil
		},
	})
	return cmd
}
