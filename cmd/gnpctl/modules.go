package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/fatred/gnpctl-v2/internal/modules"
)

func newModulesCmd(app *appContext) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "modules",
		Short: "Per-device module enablement (enable / disable / reset / show).",
	}
	cmd.AddCommand(
		newModulesSetCmd(app, "enable", "Force a module on for a target (force-on)."),
		newModulesSetCmd(app, "disable", "Force a module off for a target (force-off)."),
		newModulesResetCmd(app),
		newModulesShowCmd(app),
	)
	return cmd
}

// newModulesSetCmd wires enable and disable, which differ only in the call made
// and the status line printed.
func newModulesSetCmd(app *appContext, action, short string) *cobra.Command {
	var target string
	cmd := &cobra.Command{
		Use:   action + " MODULE",
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			module := args[0]
			cat, err := app.loadCatalog()
			if err != nil {
				return err
			}
			st, err := app.openState()
			if err != nil {
				return err
			}
			defer st.Close()
			out := cmd.OutOrStdout()
			if action == "enable" {
				warning, err := modules.Enable(st, cat, target, module)
				if err != nil {
					return err
				}
				if warning != "" {
					fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s\n", warning)
				}
				fmt.Fprintf(out, "%s: %s = forced-on\n", target, module)
				return nil
			}
			if err := modules.Disable(st, cat, target, module); err != nil {
				return err
			}
			fmt.Fprintf(out, "%s: %s = forced-off\n", target, module)
			return nil
		},
	}
	cmd.Flags().StringVar(&target, "target", "", "Target to force the module on.")
	_ = cmd.MarkFlagRequired("target")
	return cmd
}

func newModulesResetCmd(app *appContext) *cobra.Command {
	var target string
	cmd := &cobra.Command{
		Use:   "reset MODULE",
		Short: "Clear an operator pin, returning the module to its discovered default.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			module := args[0]
			cat, err := app.loadCatalog()
			if err != nil {
				return err
			}
			st, err := app.openState()
			if err != nil {
				return err
			}
			defer st.Close()
			value, err := modules.Reset(st, cat, target, module)
			if err != nil {
				return err
			}
			state := "off"
			if value != 0 {
				state = "on"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s: %s = %s (default)\n", target, module, state)
			return nil
		},
	}
	cmd.Flags().StringVar(&target, "target", "", "Target to reset the module on.")
	_ = cmd.MarkFlagRequired("target")
	return cmd
}

func newModulesShowCmd(app *appContext) *cobra.Command {
	var target string
	cmd := &cobra.Command{
		Use:   "show",
		Short: "Show per-target module state (off / on / forced-off / forced-on).",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cat, err := app.loadCatalog()
			if err != nil {
				return err
			}
			st, err := app.openState()
			if err != nil {
				return err
			}
			defer st.Close()
			var name *string
			if cmd.Flags().Changed("target") {
				name = &target
			}
			rows, err := modules.Show(st, cat, name)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), modules.FormatModules(rows))
			return nil
		},
	}
	cmd.Flags().StringVar(&target, "target", "", "Limit to a single target name.")
	return cmd
}
