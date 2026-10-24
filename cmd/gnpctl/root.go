// Package main is the gnpctl CLI. It mirrors gnpctl/cli.py: the config is
// loaded lazily by each command, so --help works without a config file.
package main

import (
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/fatred/gnpctl-v2/internal/catalog"
	"github.com/fatred/gnpctl-v2/internal/config"
	"github.com/fatred/gnpctl-v2/internal/state"
)

// appContext holds the global options and the lazily loaded config.
type appContext struct {
	configPath      string
	stateOverride   string
	catalogOverride string
	cfg             *config.Config
}

// config loads the config once, applies the CLI overrides and sets up logging.
func (a *appContext) config() (*config.Config, error) {
	if a.cfg != nil {
		return a.cfg, nil
	}
	cfg, err := config.Load(a.configPath)
	if err != nil {
		return nil, err
	}
	cfg.ApplyOverrides(a.stateOverride, a.catalogOverride)
	if err := config.SetupLogging(cfg); err != nil {
		return nil, err
	}
	a.cfg = cfg
	return cfg, nil
}

// openState opens the state database named by the config.
func (a *appContext) openState() (*state.State, error) {
	cfg, err := a.config()
	if err != nil {
		return nil, err
	}
	return state.Open(cfg.Database, nil)
}

// loadCatalog loads the layered catalog directory named by the config.
func (a *appContext) loadCatalog() (*catalog.LayeredCatalog, error) {
	cfg, err := a.config()
	if err != nil {
		return nil, err
	}
	return catalog.Load(cfg.Catalog)
}

// exitError ends the command with a non-zero status and no error message
// (Python's sys.exit(1) after the command already printed its output).
type exitError struct{ code int }

func (e *exitError) Error() string { return fmt.Sprintf("exit %d", e.code) }

// newRoot builds a fresh command tree so tests can run it repeatedly.
func newRoot(out, errw io.Writer) *cobra.Command {
	app := &appContext{}
	root := &cobra.Command{
		Use:           "gnpctl",
		Short:         "gNMI discovery and path-catalog control for gnp-stack.",
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	root.CompletionOptions.DisableDefaultCmd = true
	root.SetOut(out)
	root.SetErr(errw)
	root.PersistentFlags().StringVar(&app.configPath, "config", "", "Path to the gnpctl config file (overrides $GNPCONFIG).")
	root.PersistentFlags().StringVar(&app.stateOverride, "state", "", "Override the state database path from config.")
	root.PersistentFlags().StringVar(&app.catalogOverride, "catalog", "", "Override the catalog directory from config.")

	root.AddCommand(
		newInventoryCmd(app),
		newDiscoverCmd(app),
		newModulesCmd(app),
		newTestCmd(app),
		newRenderCmd(app),
	)
	return root
}

// run executes the CLI and returns its exit code. Errors print as
// "Error: <msg>" to stderr, like click's ClickException.
func run(args []string, out, errw io.Writer) int {
	root := newRoot(out, errw)
	root.SetArgs(args)
	if err := root.Execute(); err != nil {
		var exit *exitError
		if errors.As(err, &exit) {
			return exit.code
		}
		fmt.Fprintf(errw, "Error: %s\n", err)
		return 1
	}
	return 0
}
