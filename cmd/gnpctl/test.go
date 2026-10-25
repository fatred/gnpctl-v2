package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/fatred/gnpctl-v2/internal/pipeline"
	"github.com/fatred/gnpctl-v2/internal/testcmd"
)

func newTestCmd(app *appContext) *cobra.Command {
	var target string
	cmd := &cobra.Command{
		Use:   "test",
		Short: "Subscribe-ONCE the effective-profile paths and report per-path results.",
		Args:  usageArgs(cobra.NoArgs),
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
			results, anyFailed, err := testcmd.RunTest(st, cat, name, pipeline.DefaultTransportFactory)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			for _, result := range results {
				if !result.Ready {
					fmt.Fprintf(out, "%s: not-ready (no catalog match)\n", result.Name)
					continue
				}
				for _, pathResult := range result.Results {
					status := "ok"
					if !pathResult.OK {
						status = "FAIL"
					}
					detail := ""
					if pathResult.Detail != "" {
						detail = " (" + pathResult.Detail + ")"
					}
					fmt.Fprintf(out, "%s %s: %s%s\n", result.Name, pathResult.Path, status, detail)
				}
			}
			if anyFailed {
				return &exitError{code: 1}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&target, "target", "", "Limit to a single target name.")
	return cmd
}
