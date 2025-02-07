package monitor

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	oplog "github.com/ethereum-optimism/optimism/op-service/log"
	"github.com/ethereum/go-ethereum/common"
	"github.com/urfave/cli/v2"
)

type Config struct {
	Batcher             common.Address
	Proposer            common.Address
	Challenger          common.Address
	L1RPC               string  `json:"l1_rpc"`
	L2ELRPC             string  `json:"l2_el_rpc"`
	L2CLRPC             string  `json:"l2_cl_rpc"`
	MinBalance          float64 `json:"min_balance"`
	MaxL2UnsafeHaltTime int     `json:"max_l2_unsafe_halt_time"`
	// in blocks
	MaxL2SafeDelay      int `json:"max_l2_safe_delay"`
	MaxL2FinalizedDelay int `json:"max_l2_finalized_delay"`

	LogConfig   oplog.CLIConfig
	EmailConfig EmailConfig `json:"email_config"`
}

type EmailConfig struct {
	Server string
	Port   int
	From   string
	To     []string
	Pass   string
}

func NewConfig(ctx *cli.Context) (*Config, error) {
	configPath := ctx.String(ConfigFlag.Name)
	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	dec := json.NewDecoder(bytes.NewReader(configBytes))
	dec.DisallowUnknownFields()
	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("failed to decode config file: %w", err)
	}
	cfg.LogConfig = oplog.ReadCLIConfig(ctx)
	return &cfg, nil
}

func (c *Config) Check() error {
	if c.L1RPC == "" {
		return errors.New("empty L1 RPC URL")
	}
	if c.L2ELRPC == "" {
		return errors.New("empty L2 EL RPC URL")
	}
	if c.L2CLRPC == "" {
		return errors.New("empty L2 CL RPC URL")
	}
	if c.Batcher == (common.Address{}) {
		return errors.New("empty batcher address")
	}
	if c.Proposer == (common.Address{}) {
		return errors.New("empty proposer address")
	}
	if c.Challenger == (common.Address{}) {
		return errors.New("empty challenger address")
	}
	if c.MinBalance <= 0 {
		return errors.New("min_balance <= 0")
	}

	return nil
}
