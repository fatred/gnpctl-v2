package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/fatred/gnpctl-v2/internal/emit"
	"github.com/fatred/gnpctl-v2/internal/render"
)

func newRenderCmd(app *appContext) *cobra.Command {
	var outputDir string
	cmd := &cobra.Command{
		Use:   "render",
		Short: "Render the gnmic ingress/egress + targets config files from discovered state.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := app.config()
			if err != nil {
				return err
			}
			renderCfg, err := cfg.RequireRender()
			if err != nil {
				return err
			}
			cat, err := app.loadCatalog()
			if err != nil {
				return err
			}
			outDir := outputDir
			if outDir == "" {
				outDir = renderCfg.OutputDir
			}
			st, err := app.openState()
			if err != nil {
				return err
			}
			defer st.Close()
			desired, err := render.Compute(st, cat, renderCfg)
			if err != nil {
				return err
			}
			paths, err := emit.NewFileEmitter(outDir).Emit(desired)
			if err != nil {
				return err
			}
			for _, path := range paths {
				fmt.Fprintf(cmd.OutOrStdout(), "wrote %s\n", path)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&outputDir, "output-dir", "", "Override the render output directory.")
	return cmd
}
