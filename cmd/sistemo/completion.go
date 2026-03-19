package main

import (
	"os"

	"github.com/spf13/cobra"
)

func completionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "completion [bash|zsh|fish]",
		Short: "Generate shell completion scripts",
		Long: `Generate shell completion scripts for sistemo.

Bash:
  sistemo completion bash > /etc/bash_completion.d/sistemo

Zsh:
  sistemo completion zsh > "${fpath[1]}/_sistemo"

Fish:
  sistemo completion fish > ~/.config/fish/completions/sistemo.fish`,
		Args:      cobra.ExactArgs(1),
		ValidArgs: []string{"bash", "zsh", "fish"},
		RunE: func(cmd *cobra.Command, args []string) error {
			switch args[0] {
			case "bash":
				return cmd.Root().GenBashCompletion(os.Stdout)
			case "zsh":
				return cmd.Root().GenZshCompletion(os.Stdout)
			case "fish":
				return cmd.Root().GenFishCompletion(os.Stdout, true)
			default:
				return cmd.Help()
			}
		},
	}
	return cmd
}

// vmNameCompletionFunc provides dynamic completion for VM names.
// Used by VM subcommands that take a <name|id> argument.
func vmNameCompletionFunc(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) != 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	dataDir := getDataDirFromCmd(cmd)
	db, err := getDB(dataDir)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	defer db.Close()

	rows, err := db.Query("SELECT name FROM vm WHERE status NOT IN ('destroyed') AND name IS NOT NULL")
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if rows.Scan(&name) == nil && name != "" {
			names = append(names, name)
		}
	}
	return names, cobra.ShellCompDirectiveNoFileComp
}
