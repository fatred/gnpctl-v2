package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/fatred/gnpctl-v2/internal/inventory"

	"github.com/fatred/gnpctl-v2/internal/state"
)

func newInventoryCmd(app *appContext) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "inventory",
		Short: "Manage inventory sources and the targets table.",
		Args:  usageArgs(cobra.NoArgs),
		RunE:  func(cmd *cobra.Command, _ []string) error { return helpUsage(cmd) },
	}
	cmd.AddCommand(
		newInventoryAddCmd(app),
		newInventoryRemoveCmd(app),
		newInventoryListCmd(app),
		newInventoryPullCmd(app),
		newInventoryDiffCmd(app),
		newInventoryShowCmd(app),
	)
	return cmd
}

func newInventoryAddCmd(app *appContext) *cobra.Command {
	var typeName, tokenEnv string
	var params []string
	cmd := &cobra.Command{
		Use:   "add NAME",
		Short: "Register an inventory source (like a git remote).",
		Args:  usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireFlag(cmd, "type"); err != nil {
				return err
			}
			parsed := map[string]string{}
			for _, item := range params {
				key, value, ok := strings.Cut(item, "=")
				if !ok {
					return fmt.Errorf("--param must be key=value: %s", item)
				}
				parsed[key] = value
			}
			st, err := app.openState()
			if err != nil {
				return err
			}
			defer st.Close()
			if err := inventory.AddSource(st, args[0], typeName, parsed, tokenEnv); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "added source '%s' (%s)\n", args[0], typeName)
			return nil
		},
	}
	cmd.Flags().StringVar(&typeName, "type", "", "Source type (e.g. nornir, netbox).")
	cmd.Flags().StringArrayVarP(&params, "param", "p", nil, "Source parameter as key=value (repeatable).")
	cmd.Flags().StringVar(&tokenEnv, "token-env", "", "Name of the env var holding the source credential.")
	return cmd
}

func newInventoryRemoveCmd(app *appContext) *cobra.Command {
	return &cobra.Command{
		Use:   "remove NAME",
		Short: "Remove an inventory source (targets are left intact).",
		Args:  usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := app.openState()
			if err != nil {
				return err
			}
			defer st.Close()
			removed, err := st.RemoveSource(args[0])
			if err != nil {
				return err
			}
			if !removed {
				return fmt.Errorf("no such source: %s", args[0])
			}
			fmt.Fprintf(cmd.OutOrStdout(), "removed source '%s'\n", args[0])
			return nil
		},
	}
}

func newInventoryListCmd(app *appContext) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List configured inventory sources.",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			st, err := app.openState()
			if err != nil {
				return err
			}
			defer st.Close()
			sources, err := st.ListSources()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			for _, src := range sources {
				fmt.Fprintf(out, "%s\t%s\tlast_poll=%s\t%s\n",
					rowText(src, "name"), rowText(src, "type"),
					rowText(src, "last_poll"), rowText(src, "last_error"))
			}
			return nil
		},
	}
}

func newInventoryPullCmd(app *appContext) *cobra.Command {
	var sources []string
	cmd := &cobra.Command{
		Use:   "pull",
		Short: "Sync targets from configured sources into the targets table.",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			st, err := app.openState()
			if err != nil {
				return err
			}
			defer st.Close()
			summary, err := inventory.Pull(st, sources, nil)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "pulled %d target(s) from %d source(s)\n", summary.Targets, summary.Sources)
			for _, pullErr := range summary.Errors {
				fmt.Fprintf(cmd.ErrOrStderr(), "  error [%s]: %s\n", pullErr.Name, pullErr.Message)
			}
			if len(summary.Errors) > 0 {
				return &exitError{code: 1}
			}
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&sources, "source", nil, "Limit to named source(s).")
	return cmd
}

func newInventoryDiffCmd(app *appContext) *cobra.Command {
	var sources []string
	cmd := &cobra.Command{
		Use:   "diff",
		Short: "Show source-vs-database variance without writing.",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			st, err := app.openState()
			if err != nil {
				return err
			}
			defer st.Close()
			result, err := inventory.Diff(st, sources)
			if err != nil {
				return err
			}
			if text := inventory.FormatDiff(result); text != "" {
				fmt.Fprintln(cmd.OutOrStdout(), text)
			}
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&sources, "source", nil, "Limit to named source(s).")
	return cmd
}

func newInventoryShowCmd(app *appContext) *cobra.Command {
	var errors, undiscovered, stale bool
	cmd := &cobra.Command{
		Use:   "show",
		Short: "Render the targets table (filterable).",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			st, err := app.openState()
			if err != nil {
				return err
			}
			defer st.Close()
			rows, err := st.ShowTargets(errors, undiscovered, stale, state.DefaultStaleWindow, nil)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), inventory.FormatTable(rows))
			return nil
		},
	}
	cmd.Flags().BoolVar(&errors, "target-errors", false, "Only targets with errors.")
	cmd.Flags().BoolVar(&undiscovered, "undiscovered", false, "Only undiscovered targets.")
	cmd.Flags().BoolVar(&stale, "stale", false, "Only stale discovered targets.")
	return cmd
}

// rowText renders a DB value the way Python prints it: text as-is, integers in
// decimal, NULL as empty.
func rowText(row map[string]any, key string) string {
	if value := row[key]; value != nil {
		return fmt.Sprint(value)
	}
	return ""
}
