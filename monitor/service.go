package monitor

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"crypto/tls"

	"github.com/ethereum-optimism/optimism/op-service/dial"
	"github.com/ethereum-optimism/optimism/op-service/eth"
	"github.com/ethereum-optimism/optimism/op-service/sources"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/params"
	"gopkg.in/gomail.v2"
)

type Service struct {
	wg             sync.WaitGroup
	logger         log.Logger
	l1Client       *ethclient.Client
	l2ELClient     *ethclient.Client
	l2CLClient     *sources.RollupClient
	stopped        atomic.Bool
	shutdownCtx    context.Context
	shutdownCancel context.CancelFunc
	cfg            *Config

	lastAlertTime           *time.Time
	lastSyncStatus          *eth.SyncStatus
	lastUnsafeTime          *time.Time
	lastSyncStatusCandidate *eth.SyncStatus
	lastUnsafeTimeCandidate *time.Time
}

func ServiceFromCLIConfig(ctx context.Context, cfg *Config, logger log.Logger) (*Service, error) {
	s := &Service{
		logger: logger,
		cfg:    cfg,
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
	return nil
}

func (s *Service) initL1Client(ctx context.Context, cfg *Config) error {
	l1Client, err := dial.DialEthClientWithTimeout(ctx, dial.DefaultDialTimeout, s.logger, cfg.L1RPC)
	if err != nil {
		return fmt.Errorf("failed to dial L1: %w", err)
	}
	s.l1Client = l1Client
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
	if len(alertMsg) > 0 {
		s.alert(strings.Join(alertMsg, "<br />"))
		s.promoteLastCandidate()
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
	return alertMsg
}

func toEth(balance *big.Int) float64 {
	f, _ := balance.Float64()
	return f / 1e18
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
}

func (s *Service) alert(m string) {
	if s.lastAlertTime != nil && time.Since(*s.lastAlertTime) < 5*time.Minute {
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
		msg.SetHeader("Subject", "Alert")
		msg.SetBody("text/plain", m)

		if err := d.DialAndSend(msg); err != nil {
			allGood = false
			s.logger.Warn("Alert failed to send", "error", err, "to", to)
		} else {
			s.logger.Info("Alert sent", "to", to)
		}
	}

	if allGood {
		now := time.Now()
		s.lastAlertTime = &now
	}
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
