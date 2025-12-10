package monitor

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum-optimism/optimism/op-service/dial"
	"github.com/ethereum-optimism/optimism/op-service/eth"
	"github.com/ethereum-optimism/optimism/op-service/retry"
	"github.com/ethereum-optimism/optimism/op-service/sources"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/params"
	"gopkg.in/gomail.v2"
)

type Service struct {
	wg                 sync.WaitGroup
	logger             log.Logger
	l1Client           *ethclient.Client
	l2ELClient         *ethclient.Client
	l2CLClient         *sources.RollupClient
	batchInboxContract *bind.BoundContract
	stopped            atomic.Bool
	shutdownCtx        context.Context
	shutdownCancel     context.CancelFunc
	cfg                *Config
	l1ChainID          uint64

	lastEmailTime           *time.Time
	lastSyncStatus          *eth.SyncStatus
	lastUnsafeTime          *time.Time
	lastSyncStatusCandidate *eth.SyncStatus
	lastUnsafeTimeCandidate *time.Time

	// Etherscan API rate limiting - cache last query time and results
	lastProposerTxQueryTime   *time.Time
	lastProposerTxTime        *time.Time
	lastChallengerTxQueryTime *time.Time
	lastChallengerTxTime      *time.Time

	// Scalar monitor - only check once per day
	lastScalarCheckTime *time.Time
}

func ServiceFromCLIConfig(ctx context.Context, cfg *Config, logger log.Logger) (*Service, error) {
	now := time.Now()
	s := &Service{
		logger:        logger,
		cfg:           cfg,
		lastEmailTime: &now, // assume email was sent just now on startup, to support liveness email
	}
	if err := s.initFromCLIConfig(ctx, cfg); err != nil {
		return nil, errors.Join(err, s.Stop(ctx)) // try to clean up our failed initialization attempt
	}
	return s, nil
}

func (s *Service) initFromCLIConfig(ctx context.Context, cfg *Config) error {
	if err := s.initL1Client(ctx, cfg); err != nil {
		return err
	}
	if err := s.initL2Client(ctx, cfg); err != nil {
		return err
	}
	if err := s.initBatchInboxContract(cfg); err != nil {
		return err
	}
	return nil
}

func (s *Service) initL1Client(ctx context.Context, cfg *Config) error {
	l1Client, err := dial.DialEthClientWithTimeout(ctx, dial.DefaultDialTimeout, s.logger, cfg.L1RPC)
	if err != nil {
		return fmt.Errorf("failed to dial L1: %w", err)
	}
	s.l1Client = l1Client

	// Query L1 chain ID for Etherscan v2 API
	chainID, err := l1Client.ChainID(ctx)
	if err != nil {
		return fmt.Errorf("failed to get L1 chain ID: %w", err)
	}
	s.l1ChainID = chainID.Uint64()
	s.logger.Info("L1 chain ID", "chainID", s.l1ChainID)

	return nil
}

func (s *Service) initL2Client(ctx context.Context, cfg *Config) error {
	l2ELClient, err := dial.DialEthClientWithTimeout(ctx, dial.DefaultDialTimeout, s.logger, cfg.L2ELRPC)
	if err != nil {
		return fmt.Errorf("failed to dial L2 EL: %w", err)
	}
	s.l2ELClient = l2ELClient

	l2CLClient, err := dial.DialRollupClientWithTimeout(ctx, dial.DefaultDialTimeout, s.logger, cfg.L2CLRPC)
	if err != nil {
		return fmt.Errorf("failed to dial L2 CL: %w", err)
	}
	s.l2CLClient = l2CLClient
	return nil
}

func (s *Service) initBatchInboxContract(cfg *Config) error {
	// ABI for balances(address) returns (uint256)
	balancesABI := `[{"constant":true,"inputs":[{"name":"","type":"address"}],"name":"balances","outputs":[{"name":"","type":"uint256"}],"type":"function"}]`

	parsedABI, err := abi.JSON(strings.NewReader(balancesABI))
	if err != nil {
		return fmt.Errorf("failed to parse BatchInbox ABI: %w", err)
	}

	s.batchInboxContract = bind.NewBoundContract(cfg.BatchInbox, parsedABI, s.l1Client, s.l1Client, s.l1Client)
	return nil
}

func (s *Service) Start(_ context.Context) error {

	s.shutdownCtx, s.shutdownCancel = context.WithCancel(context.Background())

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.mainLoop()
	}()
	return nil
}

func (s *Service) mainLoop() {
	for {
		select {
		case <-s.shutdownCtx.Done():
			return
		default:
		}

		s.logger.Info("main loop")
		s.monitor()
		select {
		case <-s.shutdownCtx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

func (s *Service) monitor() {

	alertMsg := s.monitorBalance()
	alertMsg = append(alertMsg, s.monitorHeight()...)
	alertMsg = append(alertMsg, s.monitorScalar()...)
	alertMsg = append(alertMsg, s.monitorTransactions()...)
	if len(alertMsg) > 0 {
		s.sendEmail(strings.Join(alertMsg, "<br />"), "Alert")
		s.promoteLastCandidate()
	} else {
		s.sendLivenessEmail()
	}
}

func (s *Service) monitorBalance() []string {
	batcherBalance, err := s.l1Client.BalanceAt(s.shutdownCtx, s.cfg.Batcher, nil)
	if err != nil {
		s.logger.Warn("BalanceAt failed for batcher", "err", err)
		return nil
	}
	proposerBalance, err := s.l1Client.BalanceAt(s.shutdownCtx, s.cfg.Proposer, nil)
	if err != nil {
		s.logger.Warn("BalanceAt failed for proposer", "err", err)
		return nil
	}
	challengerBalance, err := s.l1Client.BalanceAt(s.shutdownCtx, s.cfg.Challenger, nil)
	if err != nil {
		s.logger.Warn("BalanceAt failed for challenger", "err", err)
		return nil
	}

	// Monitor batcher's balance in BatchInbox contract
	batchInboxBalance, err := s.getBatchInboxBalance(s.cfg.Batcher)
	if err != nil {
		s.logger.Warn("getBatchInboxBalance failed for batcher", "err", err)
		return nil
	}

	minBalance := big.NewInt(int64(s.cfg.MinBalance * params.Ether))
	var alertMsg []string
	if batcherBalance.Cmp(minBalance) <= 0 {
		alertMsg = append(alertMsg, fmt.Sprintf("batcher balance:%v eth, min:%v eth", toEth(batcherBalance), toEth(minBalance)))
	}
	if proposerBalance.Cmp(minBalance) <= 0 {
		alertMsg = append(alertMsg, fmt.Sprintf("proposer balance:%v eth, min:%v eth", toEth(proposerBalance), toEth(minBalance)))
	}
	if challengerBalance.Cmp(minBalance) <= 0 {
		alertMsg = append(alertMsg, fmt.Sprintf("challenger balance:%v eth, min:%v eth", toEth(challengerBalance), toEth(minBalance)))
	}
	if batchInboxBalance.Cmp(minBalance) <= 0 {
		alertMsg = append(alertMsg, fmt.Sprintf("batcher balance in BatchInbox:%v eth, min:%v eth", toEth(batchInboxBalance), toEth(minBalance)))
	}
	return alertMsg
}

func toEth(balance *big.Int) float64 {
	f, _ := balance.Float64()
	return f / 1e18
}

func (s *Service) getBatchInboxBalance(account common.Address) (*big.Int, error) {
	var result []interface{}
	err := s.batchInboxContract.Call(&bind.CallOpts{Context: s.shutdownCtx}, &result, "balances", account)
	if err != nil {
		return nil, fmt.Errorf("failed to call balances: %w", err)
	}

	if len(result) == 0 {
		return nil, fmt.Errorf("no result returned from balances call")
	}

	balance, ok := result[0].(*big.Int)
	if !ok {
		return nil, fmt.Errorf("unexpected result type from balances call")
	}

	return balance, nil
}

func (s *Service) monitorHeight() []string {

	syncStatus, err := s.l2CLClient.SyncStatus(s.shutdownCtx)
	if err != nil {
		s.logger.Warn("SyncStatus failed", "err", err)
		return nil
	}
	s.logger.Info("syncStatus", "unsafe", syncStatus.UnsafeL2.Number, "safe", syncStatus.SafeL2.Number, "finalized", syncStatus.FinalizedL2.Number)

	return s.updateSyncStatus(syncStatus)
}

func (s *Service) updateSyncStatus(syncStatus *eth.SyncStatus) (alertMsg []string) {
	var lastUnsafeUpdated bool
	now := time.Now()

	if s.lastSyncStatus == nil {
		lastUnsafeUpdated = true
	} else {
		switch {
		case s.lastSyncStatus.UnsafeL2.Number > syncStatus.UnsafeL2.Number:
			lastUnsafeUpdated = true
			alertMsg = append(alertMsg, fmt.Sprintf("unsafe L2 height reorg: from %v to %v", s.lastSyncStatus.UnsafeL2.Number, syncStatus.UnsafeL2.Number))
		case s.lastSyncStatus.UnsafeL2.Number < syncStatus.UnsafeL2.Number:
			lastUnsafeUpdated = true
		case s.lastSyncStatus.UnsafeL2.Number == syncStatus.UnsafeL2.Number:
			duration := now.Sub(*s.lastUnsafeTime)
			if duration > time.Second*time.Duration(s.cfg.MaxL2UnsafeHaltTime) {
				alertMsg = append(alertMsg, fmt.Sprintf("unsafe L2 height halt: height(%d), duration(%v)", syncStatus.UnsafeL2.Number, duration))
			}
		}
	}

	if syncStatus.SafeL2.Number+uint64(s.cfg.MaxL2SafeDelay) < syncStatus.UnsafeL2.Number {
		alertMsg = append(alertMsg,
			fmt.Sprintf("safe L2 gap(%d - %d = %d) > max(%d)",
				syncStatus.UnsafeL2.Number,
				syncStatus.SafeL2.Number,
				syncStatus.UnsafeL2.Number-syncStatus.SafeL2.Number,
				s.cfg.MaxL2SafeDelay))
	}
	if syncStatus.FinalizedL2.Number+uint64(s.cfg.MaxL2FinalizedDelay) < syncStatus.UnsafeL2.Number {
		alertMsg = append(alertMsg,
			fmt.Sprintf("finalized L2 gap(%d - %d = %d) > max(%d)",
				syncStatus.UnsafeL2.Number,
				syncStatus.FinalizedL2.Number,
				syncStatus.UnsafeL2.Number-syncStatus.FinalizedL2.Number,
				s.cfg.MaxL2FinalizedDelay))
	}

	if len(alertMsg) > 0 {
		s.lastSyncStatusCandidate = syncStatus
		if lastUnsafeUpdated {
			s.lastUnsafeTimeCandidate = &now
		}
	} else {
		s.lastSyncStatus = syncStatus
		if lastUnsafeUpdated {
			s.lastUnsafeTime = &now
		}
	}

	return
}

func (s *Service) promoteLastCandidate() {
	if s.lastSyncStatusCandidate != nil {
		s.lastSyncStatus = s.lastSyncStatusCandidate
		s.lastSyncStatusCandidate = nil
	}
	if s.lastUnsafeTimeCandidate != nil {
		s.lastUnsafeTime = s.lastUnsafeTimeCandidate
		s.lastUnsafeTimeCandidate = nil
	}
}

func (s *Service) sendLivenessEmail() {
	if time.Since(*s.lastEmailTime) > time.Hour*24 {
		s.sendEmail("op_monitor is running", "Liveness")
	}
}

func (s *Service) sendEmail(body, subject string) {
	if time.Since(*s.lastEmailTime) < 5*time.Minute {
		s.logger.Warn("Alert suppressed within 5 minutes")
		return
	}

	emailConfig := s.cfg.EmailConfig
	d := gomail.NewDialer(emailConfig.Server, emailConfig.Port, emailConfig.From, emailConfig.Pass)
	d.TLSConfig = &tls.Config{InsecureSkipVerify: true}
	allGood := true
	for _, to := range emailConfig.To {
		msg := gomail.NewMessage()
		msg.SetHeader("From", emailConfig.From)
		msg.SetHeader("To", to)
		msg.SetHeader("Subject", subject)
		msg.SetBody("text/plain", body)

		if err := d.DialAndSend(msg); err != nil {
			allGood = false
			s.logger.Warn("Alert failed to send", "error", err, "to", to)
		} else {
			s.logger.Info("Alert sent", "to", to)
		}
	}

	if allGood {
		now := time.Now()
		s.lastEmailTime = &now
	}
}

const maxUint32 = float64(^uint32(0)) // 4294967295

func (s *Service) monitorScalar() []string {
	// Skip if scalar monitor not configured
	if s.cfg.LastETHQKCRatio == 0 || s.cfg.QKCL1BlobBaseFeeScalar == 0 || s.cfg.QKCL1BaseFeeScalar == 0 ||
		s.cfg.L1BlobBaseFeeScalarMultiplier == 0 || s.cfg.L1BaseFeeScalarMultiplier == 0 {
		return nil
	}

	// Only check once per day to avoid CoinGecko rate limiting
	if s.lastScalarCheckTime != nil && time.Since(*s.lastScalarCheckTime) < 24*time.Hour {
		return nil
	}

	// Fetch latest ETH/QKC ratio (calls CoinGecko API, failure possibility is high)
	latestRatio, err := s.getETHQKCRatio()
	if err != nil {
		s.logger.Warn("getETHQKCRatio failed", "err", err)
		return []string{fmt.Sprintf("Failed to fetch ETH/QKC ratio (CoinGecko API may be unavailable): %v", err)}
	}

	// Update last check time after successful API call
	now := time.Now()
	s.lastScalarCheckTime = &now

	// Calculate ratio change percentage (absolute value)
	ratioChange := (latestRatio - s.cfg.LastETHQKCRatio) / s.cfg.LastETHQKCRatio
	if ratioChange < 0 {
		ratioChange = -ratioChange
	}

	// Calculate: ETH/QKC ratio × QKC scalar × 1.5 / multiplier
	blobMultiplier := s.cfg.L1BlobBaseFeeScalarMultiplier
	baseMultiplier := s.cfg.L1BaseFeeScalarMultiplier

	latestBlobValue := uint32(latestRatio * float64(s.cfg.QKCL1BlobBaseFeeScalar) * 1.5 / float64(blobMultiplier))
	latestBaseValue := uint32(latestRatio * float64(s.cfg.QKCL1BaseFeeScalar) * 1.5 / float64(baseMultiplier))

	s.logger.Info("scalar monitor",
		"latestETHQKCRatio", latestRatio,
		"lastETHQKCRatio", s.cfg.LastETHQKCRatio,
		"ratioChange", fmt.Sprintf("%.2f%%", ratioChange*100),
		"threshold", fmt.Sprintf("%.2f%%", s.cfg.RatioChangeThreshold*100),
		"qkcBlobScalar", s.cfg.QKCL1BlobBaseFeeScalar,
		"qkcBaseScalar", s.cfg.QKCL1BaseFeeScalar,
		"latestBlobValue", latestBlobValue,
		"latestBaseValue", latestBaseValue)

	var alertMsg []string

	// Check if latest values would exceed threshold of uint32 max
	if s.cfg.Uint32OverflowThreshold > 0 {
		overflowThreshold := uint32(maxUint32 * s.cfg.Uint32OverflowThreshold)
		if latestBlobValue > overflowThreshold {
			alertMsg = append(alertMsg, fmt.Sprintf(
				"l1BlobBaseFeeScalar near overflow: %d (= %.4f × %d × 1.5 / %d) > %.0f%% of max uint32 (%d)",
				latestBlobValue, latestRatio, s.cfg.QKCL1BlobBaseFeeScalar, blobMultiplier,
				s.cfg.Uint32OverflowThreshold*100, overflowThreshold))
		}

		if latestBaseValue > overflowThreshold {
			alertMsg = append(alertMsg, fmt.Sprintf(
				"l1BaseFeeScalar near overflow: %d (= %.4f × %d × 1.5 / %d) > %.0f%% of max uint32 (%d)",
				latestBaseValue, latestRatio, s.cfg.QKCL1BaseFeeScalar, baseMultiplier,
				s.cfg.Uint32OverflowThreshold*100, overflowThreshold))
		}
	}

	// Alert if ratio change exceeds threshold
	if ratioChange > s.cfg.RatioChangeThreshold {
		lastBlobValue := uint32(s.cfg.LastETHQKCRatio * float64(s.cfg.QKCL1BlobBaseFeeScalar) * 1.5 / float64(blobMultiplier))
		lastBaseValue := uint32(s.cfg.LastETHQKCRatio * float64(s.cfg.QKCL1BaseFeeScalar) * 1.5 / float64(baseMultiplier))

		blobPctOfMax := float64(latestBlobValue) / maxUint32 * 100
		basePctOfMax := float64(latestBaseValue) / maxUint32 * 100

		alertMsg = append(alertMsg, fmt.Sprintf(
			"Scalar adjustment needed (ratio change %.2f%% > threshold %.2f%%): "+
				"l1BlobBaseFeeScalar: latest(%d, %.4f%% of maxUint32) vs last(%d), "+
				"l1BaseFeeScalar: latest(%d, %.4f%% of maxUint32) vs last(%d)",
			ratioChange*100, s.cfg.RatioChangeThreshold*100,
			latestBlobValue, blobPctOfMax, lastBlobValue,
			latestBaseValue, basePctOfMax, lastBaseValue))
	}

	return alertMsg
}

func (s *Service) getETHQKCRatio() (float64, error) {
	return retry.Do(s.shutdownCtx, 3, retry.Fixed(10*time.Second), func() (float64, error) {
		return s.fetchETHQKCRatio()
	})
}

func (s *Service) fetchETHQKCRatio() (float64, error) {
	// Use CoinGecko API to get ETH and QKC prices in USD, then calculate ratio
	url := "https://api.coingecko.com/api/v3/simple/price?ids=ethereum,quark-chain&vs_currencies=usd"

	ctx, cancel := context.WithTimeout(s.shutdownCtx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return 0, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("failed to fetch price: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		Ethereum   struct{ USD float64 `json:"usd"` } `json:"ethereum"`
		QuarkChain struct{ USD float64 `json:"usd"` } `json:"quark-chain"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, fmt.Errorf("failed to decode response: %w", err)
	}

	if result.QuarkChain.USD == 0 {
		return 0, fmt.Errorf("QKC price is zero")
	}

	// Calculate ETH/QKC ratio
	ratio := result.Ethereum.USD / result.QuarkChain.USD
	return ratio, nil
}

func (s *Service) monitorTransactions() []string {
	var alertMsg []string

	if s.cfg.MaxProposerTxInterval > 0 {
		alertMsg = append(alertMsg, s.monitorAddressTx(
			"op-proposer", s.cfg.Proposer, s.cfg.MaxProposerTxInterval,
			&s.lastProposerTxQueryTime, &s.lastProposerTxTime)...)
	}

	if s.cfg.MaxChallengerTxInterval > 0 {
		alertMsg = append(alertMsg, s.monitorAddressTx(
			"op-challenger", s.cfg.Challenger, s.cfg.MaxChallengerTxInterval,
			&s.lastChallengerTxQueryTime, &s.lastChallengerTxTime)...)
	}

	return alertMsg
}

func (s *Service) monitorAddressTx(name string, address common.Address, maxInterval int, lastQueryTime, lastTxTime **time.Time) []string {
	var alertMsg []string
	queryInterval := time.Duration(maxInterval/2) * time.Second
	maxIntervalDuration := time.Duration(maxInterval) * time.Second

	// Only query Etherscan if enough time has passed since last query
	if *lastQueryTime == nil || time.Since(**lastQueryTime) >= queryInterval {
		txTime, err := s.getLastTransactionTime(address)
		if err != nil {
			s.logger.Warn("getLastTransactionTime failed", "name", name, "err", err)
			alertMsg = append(alertMsg, fmt.Sprintf("Failed to fetch %s transactions from Etherscan: %v", name, err))
		} else {
			// Only update lastQueryTime on first query or when we find transactions
			// This preserves the original query time for "no transactions" alerting
			if *lastQueryTime == nil || txTime != nil {
				now := time.Now()
				*lastQueryTime = &now
			}
			*lastTxTime = txTime
		}
	}

	if *lastTxTime != nil {
		elapsed := time.Since(**lastTxTime)
		s.logger.Info("transaction monitor",
			"name", name,
			"lastTxTime", (**lastTxTime).Format(time.RFC3339),
			"elapsed", elapsed.String(),
			"maxInterval", maxIntervalDuration)

		if elapsed > maxIntervalDuration {
			// Re-fetch to avoid false alarm based on stale cache
			txTime, err := s.getLastTransactionTime(address)
			if err != nil {
				s.logger.Warn("getLastTransactionTime failed", "name", name, "err", err)
				alertMsg = append(alertMsg, fmt.Sprintf("Failed to fetch %s transactions from Etherscan: %v", name, err))
			} else if txTime != nil {
				now := time.Now()
				*lastQueryTime = &now
				*lastTxTime = txTime
				elapsed = time.Since(*txTime)
				if elapsed > maxIntervalDuration {
					alertMsg = append(alertMsg, fmt.Sprintf(
						"%s transaction interval exceeded: last tx %v ago (max %v)",
						name, elapsed.Round(time.Second), maxIntervalDuration))
				}
			}
		}
	} else if *lastQueryTime != nil {
		// No transactions found - alert if service has been running longer than max interval
		elapsed := time.Since(**lastQueryTime)
		if elapsed > maxIntervalDuration {
			alertMsg = append(alertMsg, fmt.Sprintf(
				"%s has no transactions found (checked %v ago, max interval %v)",
				name, elapsed.Round(time.Second), maxIntervalDuration))
		}
	}

	return alertMsg
}

func (s *Service) getLastTransactionTime(address common.Address) (*time.Time, error) {
	return retry.Do(s.shutdownCtx, 3, retry.Fixed(10*time.Second), func() (*time.Time, error) {
		return s.fetchLastTransactionTime(address)
	})
}

func (s *Service) fetchLastTransactionTime(address common.Address) (*time.Time, error) {
	// Query Etherscan v2 API for the latest transaction
	url := fmt.Sprintf("https://api.etherscan.io/v2/api?chainid=%d&module=account&action=txlist&address=%s&startblock=0&endblock=99999999&page=1&offset=1&sort=desc&apikey=%s",
		s.l1ChainID, address.Hex(), s.cfg.EtherscanAPIKey)

	ctx, cancel := context.WithTimeout(s.shutdownCtx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch transactions: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		Status  string `json:"status"`
		Message string `json:"message"`
		Result  []struct {
			TimeStamp string `json:"timeStamp"`
		} `json:"result"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	if result.Status != "1" {
		// Status "0" with "No transactions found" is not an error
		if result.Message == "No transactions found" {
			return nil, nil
		}
		return nil, fmt.Errorf("etherscan API error: %s", result.Message)
	}

	if len(result.Result) == 0 {
		return nil, nil
	}

	// Parse timestamp (Unix timestamp as string)
	var timestamp int64
	if _, err := fmt.Sscanf(result.Result[0].TimeStamp, "%d", &timestamp); err != nil {
		return nil, fmt.Errorf("failed to parse timestamp: %w", err)
	}

	txTime := time.Unix(timestamp, 0)
	return &txTime, nil
}

func (s *Service) Stop(_ context.Context) error {

	s.shutdownCancel()

	s.wg.Wait()
	s.stopped.Store(true)
	return nil
}

func (s *Service) Stopped() bool {
	return s.stopped.Load()
}
