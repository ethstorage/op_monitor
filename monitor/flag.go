package monitor

import (
	"fmt"

	oplog "github.com/ethereum-optimism/optimism/op-service/log"
	"github.com/urfave/cli/v2"
)

var ConfigFlag = &cli.StringFlag{
	Name:  "config",
	Usage: "config file path",
}

var requiredFlags = []cli.Flag{
	ConfigFlag,
}

var optionalFlags = []cli.Flag{}

// Flags contains the list of configuration options available to the binary.
var Flags []cli.Flag

func init() {
	optionalFlags = append(optionalFlags, oplog.CLIFlags("")...)

	Flags = append(requiredFlags, optionalFlags...)
}

func CheckRequired(ctx *cli.Context) error {
	for _, f := range requiredFlags {
		if !ctx.IsSet(f.Names()[0]) {
			return fmt.Errorf("flag %s is required", f.Names()[0])
		}
	}
	return nil
}

// utility action flags
var SlotFlag = &cli.Uint64Flag{
	Name:     "slot",
	Usage:    "specify slot",
	Required: true,
}

var RPCFlag = &cli.StringFlag{
	Name:     "rpc",
	Usage:    "specify rpc",
	Required: true,
}
