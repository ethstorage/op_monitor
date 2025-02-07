package monitor

import (
	"context"
	"fmt"

	"github.com/ethereum-optimism/optimism/op-service/cliapp"
	oplog "github.com/ethereum-optimism/optimism/op-service/log"
	"github.com/urfave/cli/v2"
)

func Main() cliapp.LifecycleAction {
	return func(cliCtx *cli.Context, closeApp context.CancelCauseFunc) (cliapp.Lifecycle, error) {
		if err := CheckRequired(cliCtx); err != nil {
			return nil, err
		}

		cfg, err := NewConfig(cliCtx)
		if err != nil {
			return nil, fmt.Errorf("NewConfig failed: %w", err)
		}
		if err := cfg.Check(); err != nil {
			return nil, fmt.Errorf("Config.Check failed: %w", err)
		}

		l := oplog.NewLogger(oplog.AppOut(cliCtx), cfg.LogConfig)
		oplog.SetGlobalLogHandler(l.Handler())

		l.Info("Initializing Op Monitor")
		return ServiceFromCLIConfig(cliCtx.Context, cfg, l)
	}
}
