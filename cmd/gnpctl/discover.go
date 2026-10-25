package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/fatred/gnpctl-v2/internal/pipeline"
	"github.com/fatred/gnpctl-v2/internal/state"
)

func newDiscoverCmd(app *appContext) *cobra.Command {
	var periodic, force bool
	var target string
	cmd := &cobra.Command{
		Use:   "discover",
		Short: "Fingerprint targets and register their catalog match.",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			mode := "default"
			if periodic {
				mode = "periodic"
			}
			if force {
				mode = "force"
			}
			cat, err := app.loadCatalog() // aborts before contacting devices if invalid
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
			report := cmd.OutOrStdout()
			counts, err := pipeline.RunDiscovery(
				st, cat, mode, state.DefaultStaleWindow, name, pipeline.DefaultTransportFactory, nil, report,
			)
			if err != nil {
				return err
			}
			fmt.Fprintf(report, "matched=%d unknown=%d error=%d\n",
				counts[pipeline.StatusMatched], counts[pipeline.StatusUnknown], counts[pipeline.StatusError])
			if counts[pipeline.StatusError] > 0 {
				return &exitError{code: 1}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&periodic, "periodic", false, "Re-discover stale discovered targets.")
	cmd.Flags().BoolVar(&force, "force", false, "Discover all targets, ignoring state.")
	cmd.Flags().StringVar(&target, "target", "", "Limit to a single target name.")
	return cmd
}
