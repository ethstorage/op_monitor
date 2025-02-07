package monitor

import (
	"context"
	"fmt"

	"github.com/ethereum-optimism/optimism/op-service/client"
	"github.com/ethereum-optimism/optimism/op-service/sources"
	"github.com/urfave/cli/v2"
)

var UtilityCmd = cli.Command{
	Name:        "u",
	Usage:       "utility actions",
	Subcommands: []*cli.Command{fetchBlob},
}

var fetchBlob = &cli.Command{
	Name:   "fetch_blob",
	Usage:  "fetch blob by slot",
	Flags:  []cli.Flag{SlotFlag, RPCFlag},
	Action: fetchBlobAction,
}

func fetchBlobAction(ctx *cli.Context) (err error) {
	slot := ctx.Uint64(SlotFlag.Name)
	rpc := ctx.String(RPCFlag.Name)

	beaconClient := sources.NewBeaconHTTPClient(client.NewBasicHTTPClient(rpc, nil))

	resp, err := beaconClient.BeaconBlobSideCars(context.Background(), true, slot, nil)
	if err != nil {
		return
	}

	fmt.Println(resp)
	return
}
