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
	BatchInbox          common.Address `json:"batch_inbox"`
	L1RPC               string         `json:"l1_rpc"`
	L2ELRPC             string         `json:"l2_el_rpc"`
	L2CLRPC             string         `json:"l2_cl_rpc"`
	MinBalance          float64        `json:"min_balance"`
	MaxL2UnsafeHaltTime int            `json:"max_l2_unsafe_halt_time"`
	// in blocks
	MaxL2SafeDelay      int `json:"max_l2_safe_delay"`
	MaxL2FinalizedDelay int `json:"max_l2_finalized_delay"`

	// Scalar adjustment monitor config
	LastETHQKCRatio               float64 `json:"last_eth_qkc_ratio"`                 // ETH/QKC ratio when scalars were last set
	QKCL1BlobBaseFeeScalar        uint32  `json:"qkc_l1_blob_base_fee_scalar"`        // QKC chain's l1BlobBaseFeeScalar
	QKCL1BaseFeeScalar            uint32  `json:"qkc_l1_base_fee_scalar"`             // QKC chain's l1BaseFeeScalar
	L1BlobBaseFeeScalarMultiplier uint64  `json:"l1_blob_base_fee_scalar_multiplier"` // Multiplier for l1BlobBaseFeeScalar calculation
	L1BaseFeeScalarMultiplier     uint64  `json:"l1_base_fee_scalar_multiplier"`      // Multiplier for l1BaseFeeScalar calculation
	RatioChangeThreshold          float64 `json:"ratio_change_threshold"`             // Alert when ratio change exceeds this threshold (e.g., 0.1 = 10%)
	Uint32OverflowThreshold       float64 `json:"uint32_overflow_threshold"`          // Alert when value exceeds this fraction of maxUint32 (e.g., 0.9 = 90%)

	// Transaction submission monitor config (uses Etherscan v2 API)
	EtherscanAPIKey         string `json:"etherscan_api_key"`          // Etherscan API key for querying transactions
	MaxProposerTxInterval   int    `json:"max_proposer_tx_interval"`   // Max seconds between proposer transactions
	MaxChallengerTxInterval int    `json:"max_challenger_tx_interval"` // Max seconds between challenger transactions (0 to disable)

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
	if c.BatchInbox == (common.Address{}) {
		return errors.New("empty batch inbox address")
	}
	if c.MinBalance <= 0 {
		return errors.New("min_balance <= 0")
	}
	if c.Uint32OverflowThreshold < 0 || c.Uint32OverflowThreshold >= 1 {
		return errors.New("uint32_overflow_threshold must be >= 0 and < 1")
	}
	if c.RatioChangeThreshold < 0 {
		return errors.New("ratio_change_threshold must not be negative")
	}

	// Check for partial scalar monitor configuration
	scalarFields := []bool{
		c.LastETHQKCRatio != 0,
		c.QKCL1BlobBaseFeeScalar != 0,
		c.QKCL1BaseFeeScalar != 0,
		c.L1BlobBaseFeeScalarMultiplier != 0,
		c.L1BaseFeeScalarMultiplier != 0,
	}
	configuredCount := 0
	for _, configured := range scalarFields {
		if configured {
			configuredCount++
		}
	}
	if configuredCount > 0 && configuredCount < len(scalarFields) {
		return errors.New("scalar monitor fields must be all configured or all unconfigured: last_eth_qkc_ratio, qkc_l1_blob_base_fee_scalar, qkc_l1_base_fee_scalar, l1_blob_base_fee_scalar_multiplier, l1_base_fee_scalar_multiplier")
	}

	// Check for partial transaction monitor configuration
	txMonitorEnabled := c.MaxProposerTxInterval > 0 || c.MaxChallengerTxInterval > 0
	if txMonitorEnabled {
		if c.EtherscanAPIKey == "" {
			return errors.New("etherscan_api_key is required when transaction monitoring is enabled")
		}
	}

	return nil
}
