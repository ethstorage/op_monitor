package main

import (
	"os"

	"context"

	"github.com/ethereum-optimism/optimism/op-service/cliapp"
	"github.com/ethereum-optimism/optimism/op-service/ctxinterrupt"
	oplog "github.com/ethereum-optimism/optimism/op-service/log"
	"github.com/ethereum/go-ethereum/log"
	"github.com/urfave/cli/v2"
	"github.com/zhiqiangxu/op_monitor/monitor"
)

func main() {
	oplog.SetupDefaults()

	app := cli.NewApp()
	app.Name = "op-monitor"
	app.Copyright = "Copyright in 2025"
	app.Flags = cliapp.ProtectFlags(monitor.Flags)
	app.Commands = []*cli.Command{
		&monitor.UtilityCmd,
	}
	app.Action = cliapp.LifecycleCmd(monitor.Main())
	ctx := ctxinterrupt.WithSignalWaiterMain(context.Background())
	err := app.RunContext(ctx, os.Args)
	if err != nil {
		log.Crit("Application failed", "message", err)
	}
}
