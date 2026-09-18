package financial

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/leonardodacosta/distributedBettingProcessing/internal/domain/ledger"
	"github.com/leonardodacosta/distributedBettingProcessing/internal/domain/money"
	"github.com/leonardodacosta/distributedBettingProcessing/internal/domain/wager"
	"github.com/leonardodacosta/distributedBettingProcessing/internal/domain/wallet"
	"github.com/leonardodacosta/distributedBettingProcessing/internal/infrastructure/postgres"
	"github.com/leonardodacosta/distributedBettingProcessing/internal/observability"
)

var (
	ErrInvalidCommand       = errors.New("invalid financial command")
	ErrReferenceInvalid     = errors.New("invalid reference")
	ErrIdempotencyConflict  = errors.New("idempotency conflict")
	ErrReplayUnavailable    = errors.New("persisted idempotency result unavailable")
	ErrInboxPayloadMismatch = errors.New("inbox payload hash mismatch")
)

const (
	FailureCodeInsufficientBalance         = "INSUFFICIENT_BALANCE"
	FailureCodeReversalInsufficientBalance = "REVERSAL_INSUFFICIENT_BALANCE"
	FailureCodePlayerWalletMismatch        = "PLAYER_WALLET_MISMATCH"
	FailureCodeReferenceRequired           = "REFERENCE_REQUIRED"
	FailureCodeReferenceInvalid            = "REFERENCE_INVALID"
	FailureCodeReferenceNotFound           = "REFERENCE_NOT_FOUND"
	FailureCodeReferenceNotResolved        = "REFERENCE_NOT_RESOLVED"
	FailureCodeReferenceNotSuccessful      = "REFERENCE_NOT_SUCCESSFUL"
)

const legacyIdentity = "legacy"

type Command struct {
	ID, WalletID                                                                   uuid.UUID
	ExternalID, ProviderID, PlayerID, GameID, RoundID, IdempotencyKey, PayloadHash string
	ReferenceExternalID                                                            string
	Type                                                                           wager.Type
	Amount                                                                         money.Money
}

type Result struct {
	TransactionID uuid.UUID
	State         wager.State
	Balance       int64
	Amount        money.Money
	FailureCode   string
}

type Reconciliation struct {
	WalletBalance, LedgerBalance int64
	Consistent                   bool
}

type eventPayload struct {
	TransactionID uuid.UUID   `json:"transactionId"`
	WalletID      uuid.UUID   `json:"walletId"`
	Type          wager.Type  `json:"type"`
	State         wager.State `json:"state"`
	Amount        money.Money `json:"amount"`
	Balance       int64       `json:"balance"`
	FailureCode   string      `json:"failureCode,omitempty"`
}

type walletBalanceChangedPayload struct {
	TransactionID uuid.UUID   `json:"transactionId"`
	WalletID      uuid.UUID   `json:"walletId"`
	Direction     string      `json:"direction"`
	Money         money.Money `json:"money"`
	BalanceBefore money.Money `json:"balanceBefore"`
	BalanceAfter  money.Money `json:"balanceAfter"`
	WalletVersion int64       `json:"walletVersion"`
	Type          wager.Type  `json:"type"`
	State         wager.State `json:"state"`
}

type balanceChange struct {
	Direction     ledger.Direction
	BalanceBefore money.Money
	BalanceAfter  money.Money
	WalletVersion int64
}

type Service struct {
	db      *postgres.Repository
	metrics *observability.Metrics
}

func NewService(db *postgres.Repository, metricSets ...*observability.Metrics) *Service {
	var metrics *observability.Metrics
	if len(metricSets) > 0 {
		metrics = metricSets[0]
	}
	return &Service{db: db, metrics: metrics}
}

func (s *Service) OpenWallet(ctx context.Context, id uuid.UUID, playerID string, opening money.Money, now time.Time) error {
	return s.db.WithTx(ctx, func(ctx context.Context, tx *postgres.Repository) error {
		if _, err := wallet.New(id.String(), playerID, opening); err != nil {
			return err
		}
		if err := postgres.NewWalletRepository(tx).Insert(ctx, postgres.WalletRecord{ID: id, PlayerID: playerID, Currency: opening.Currency(), Balance: opening.Minor(), Version: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
			return err
		}
		if opening.Minor() == 0 {
			return nil
		}
		openingID := uuid.New()
		openingTx, err := wager.NewOpening(openingID.String(), id.String(), opening)
		if err != nil {
			return err
		}
		if err := openingTx.Transition(wager.Processed); err != nil {
			return err
		}
		externalID := "opening-" + id.String()
		openingResult := Result{TransactionID: openingID, State: wager.Processed, Balance: opening.Minor(), Amount: opening}
		resultData, err := marshalResult(openingResult)
		if err != nil {
			return err
		}
		if err := postgres.NewWagerTransactionRepository(tx).Insert(ctx, postgres.WagerTransactionRecord{
			ID: openingID, ExternalID: externalID, ProviderID: "internal", WalletID: id,
			PlayerID: playerID, GameID: "internal", RoundID: "opening", Type: string(wager.Opening), Amount: opening.Minor(),
			Currency: opening.Currency(), State: string(wager.Processed), IdempotencyKey: externalID,
			PayloadHash: externalID, Result: resultData, CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			return err
		}
		before, _ := money.Zero(opening.Currency())
		entry, err := ledger.New(uuid.New().String(), id.String(), openingID.String(), ledger.Credit, opening, before, opening, now)
		if err != nil {
			return err
		}
		if err := postgres.NewLedgerRepository(tx).Insert(ctx, ledgerRecord(entry)); err != nil {
			return err
		}
		return insertFinancialEvents(ctx, tx, wager.Opening, id, openingResult, 1, now, externalID, true, balanceChange{Direction: ledger.Credit, BalanceBefore: before, BalanceAfter: opening, WalletVersion: 1})
	})
}

func (s *Service) Process(ctx context.Context, cmd Command, now time.Time) (Result, error) {
	started := time.Now()
	prepared, err := prepareCommand(cmd)
	if err != nil {
		return Result{}, err
	}
	cmd = prepared
	var result Result
	err = s.db.WithTx(ctx, func(ctx context.Context, tx *postgres.Repository) error {
		var err error
		result, err = s.processInTx(ctx, tx, cmd, now)
		return err
	})
	if err == nil {
		if result.TransactionID != cmd.ID && s.metrics != nil {
			s.metrics.ObserveDuplicate(observability.DuplicateKindHTTPIdempotency)
		}
		s.observeResult(result, time.Since(started))
		return result, nil
	}
	if !isKnownIdentityViolation(err) {
		s.observeError(err, time.Since(started))
		return Result{}, err
	}
	if isReversalIdentityViolation(err) {
		s.observeError(ErrIdempotencyConflict, time.Since(started))
		return Result{}, ErrIdempotencyConflict
	}
	result, err = s.resolveIdentityConflict(ctx, cmd)
	if err != nil {
		s.observeError(err, time.Since(started))
	} else {
		if s.metrics != nil {
			s.metrics.ObserveDuplicate(observability.DuplicateKindHTTPIdempotency)
		}
		s.observeResult(result, time.Since(started))
	}
	return result, err
}

// ProcessMessage atomically records an SQS inbox identity, executes the
// financial command and marks the inbox record complete. A duplicate message
// returns the persisted financial result and duplicate=true without applying
// another balance mutation.
func (s *Service) ProcessMessage(ctx context.Context, consumerName, messageID, payloadHash string, cmd Command, now time.Time) (result Result, duplicate bool, err error) {
	started := time.Now()
	prepared, err := prepareCommand(cmd)
	if err != nil {
		return Result{}, false, err
	}
	cmd = prepared
	if cmd.PayloadHash != payloadHash {
		return Result{}, false, ErrInboxPayloadMismatch
	}
	err = s.db.WithTx(ctx, func(ctx context.Context, tx *postgres.Repository) error {
		inbox := postgres.NewInboxRepository(tx)
		inserted, err := inbox.InsertIfAbsent(ctx, postgres.InboxRecord{
			ConsumerName: consumerName,
			MessageID:    messageID,
			PayloadHash:  payloadHash,
			ReceivedAt:   now,
		})
		if err != nil {
			return err
		}
		if !inserted {
			existing, findErr := inbox.Find(ctx, consumerName, messageID)
			if findErr != nil {
				return findErr
			}
			if existing.PayloadHash != payloadHash {
				return ErrInboxPayloadMismatch
			}
			duplicate = existing.CompletedAt != nil
		}

		result, err = s.processInTx(ctx, tx, cmd, now)
		if err != nil {
			return err
		}
		return inbox.MarkCompleted(ctx, consumerName, messageID, now)
	})
	if err == nil {
		if duplicate {
			s.metrics.ObserveDuplicate(observability.DuplicateKindSQSInbox)
		}
		s.observeResult(result, time.Since(started))
		return result, duplicate, nil
	}
	// An identity race must roll back the inbox together with every financial
	// write. In particular, do not resolve it through Process (which owns an
	// independent transaction) and then complete the inbox later: that would
	// expose a committed financial result without its durable inbox completion.
	// The message remains unacknowledged and follows the configured permanent
	// failure/redrive policy instead.
	if isKnownIdentityViolation(err) {
		s.observeError(ErrIdempotencyConflict, time.Since(started))
		return Result{}, false, ErrIdempotencyConflict
	}
	s.observeError(err, time.Since(started))
	return Result{}, false, err
}

func (s *Service) observeResult(result Result, duration time.Duration) {
	if s.metrics != nil {
		s.metrics.ObserveProcessing(string(result.State), duration)
	}
}

func (s *Service) observeError(err error, duration time.Duration) {
	if s.metrics == nil {
		return
	}
	if errors.Is(err, ErrIdempotencyConflict) {
		s.metrics.ObserveIdempotencyConflict()
	}
	s.metrics.ObserveProcessing("error", duration)
}

func prepareCommand(cmd Command) (Command, error) {
	if err := validateCommand(cmd); err != nil {
		return Command{}, err
	}
	payloadHash, err := canonicalPayloadHash(cmd)
	if err != nil {
		return Command{}, err
	}
	cmd.PayloadHash = payloadHash
	return cmd, nil
}

func (s *Service) processInTx(ctx context.Context, tx *postgres.Repository, cmd Command, now time.Time) (Result, error) {
	var result Result
	tr := postgres.NewWagerTransactionRepository(tx)
	if err := tr.LockReferenceIdentity(ctx, cmd.ProviderID, cmd.ExternalID); err != nil {
		return result, err
	}
	existing, found, err := findExistingTransaction(ctx, tr, cmd)
	if err != nil {
		return result, err
	}
	if found {
		result, err = replayResult(existing)
		return result, err
	}

	wr := postgres.NewWalletRepository(tx)
	w, err := wr.FindForUpdate(ctx, cmd.WalletID)
	if err != nil {
		return result, err
	}
	// A concurrent transaction may have committed after the first lookup
	// while this request waited for the wallet lock. Resolve it again before
	// applying any financial mutation.
	existing, found, err = findExistingTransaction(ctx, tr, cmd)
	if err != nil {
		return result, err
	}
	if found {
		result, err = replayResult(existing)
		return result, err
	}
	txDomain, err := wager.New(cmd.ID.String(), cmd.ExternalID, cmd.ProviderID, cmd.WalletID.String(), cmd.Type, cmd.Amount)
	if err != nil {
		return result, err
	}
	if cmd.PlayerID != w.PlayerID {
		err := persistRejected(ctx, tx, cmd, w, now, txDomain, &result, FailureCodePlayerWalletMismatch)
		return result, err
	}
	var reference *postgres.WagerTransactionRecord
	if cmd.Type == wager.Refund || cmd.Type == wager.Rollback {
		if cmd.ReferenceExternalID == "" {
			err := persistRejected(ctx, tx, cmd, w, now, txDomain, &result, FailureCodeReferenceRequired)
			return result, err
		}
		foundReference, findErr := tr.FindByExternal(ctx, cmd.ProviderID, cmd.ReferenceExternalID)
		if errors.Is(findErr, pgx.ErrNoRows) {
			return result, persistPendingReference(ctx, tx, cmd, w, now, txDomain, &result)
		}
		if findErr != nil {
			return result, findErr
		}
		if !validReference(cmd, foundReference) {
			err := persistRejected(ctx, tx, cmd, w, now, txDomain, &result, FailureCodeReferenceInvalid)
			return result, err
		}
		reference = &foundReference
	}

	return s.applyOperationInTx(ctx, tx, cmd, w, txDomain, reference, nil, now)
}

func (s *Service) applyOperationInTx(ctx context.Context, tx *postgres.Repository, cmd Command, w postgres.WalletRecord, txDomain wager.Transaction, reference *postgres.WagerTransactionRecord, existing *postgres.WagerTransactionRecord, now time.Time) (Result, error) {
	before := w.Balance
	after := before
	movement := cmd.Amount.Minor()
	direction := ledger.Debit
	domainWallet, err := walletFromRecord(w)
	if err != nil {
		return Result{}, err
	}
	var operationErr error
	switch cmd.Type {
	case wager.Bet:
		direction, operationErr = ledger.Debit, domainWallet.Debit(cmd.Amount)
	case wager.Win, wager.Refund:
		direction, operationErr = ledger.Credit, domainWallet.Credit(cmd.Amount)
	case wager.Rollback:
		if reference == nil {
			return Result{}, ErrReferenceInvalid
		}
		if reference.Type == string(wager.Bet) {
			direction, operationErr = ledger.Credit, domainWallet.Credit(cmd.Amount)
		} else {
			direction, operationErr = ledger.Debit, domainWallet.Debit(cmd.Amount)
		}
	case wager.Loss:
		movement = 0
	default:
		return Result{}, ErrReferenceInvalid
	}
	state := wager.Processed
	failureCode := ""
	if operationErr != nil {
		state = wager.Rejected
		if cmd.Type == wager.Bet {
			failureCode = FailureCodeInsufficientBalance
		} else {
			failureCode = FailureCodeReversalInsufficientBalance
		}
	} else if movement > 0 {
		after = domainWallet.Balance().Minor()
	}
	if err := txDomain.Transition(state); err != nil {
		return Result{}, err
	}
	result := Result{TransactionID: cmd.ID, State: state, Balance: after, Amount: cmd.Amount, FailureCode: failureCode}
	if existing == nil {
		if err := insertTransaction(ctx, tx, cmd, result, now); err != nil {
			return result, err
		}
	} else {
		existing.State = string(state)
		existing.Result, err = marshalResult(result)
		if err != nil {
			return result, err
		}
		existing.ReferenceNextAttemptAt = nil
		existing.FailureCode = failureCode
		existing.UpdatedAt = now
		if err := postgres.NewWagerTransactionRepository(tx).UpdatePendingReference(ctx, *existing); err != nil {
			return result, err
		}
	}
	if state == wager.Processed && movement > 0 {
		if err := postgres.NewWalletRepository(tx).UpdateBalance(ctx, cmd.WalletID, after, w.Version+1, now); err != nil {
			return result, err
		}
		value, err := moneyFromMinor(movement, cmd.Amount.Currency())
		if err != nil {
			return result, err
		}
		beforeMoney, err := moneyFromMinor(before, cmd.Amount.Currency())
		if err != nil {
			return result, err
		}
		afterMoney, err := moneyFromMinor(after, cmd.Amount.Currency())
		if err != nil {
			return result, err
		}
		entry, err := ledger.New(uuid.New().String(), cmd.WalletID.String(), cmd.ID.String(), direction, value, beforeMoney, afterMoney, now)
		if err != nil {
			return result, err
		}
		if err := postgres.NewLedgerRepository(tx).Insert(ctx, ledgerRecord(entry)); err != nil {
			return result, err
		}
	}
	version := int64(w.Version)
	if state == wager.Processed && movement > 0 {
		version++
	}
	if state == wager.Processed {
		beforeMoney, err := moneyFromMinor(before, cmd.Amount.Currency())
		if err != nil {
			return result, err
		}
		afterMoney, err := moneyFromMinor(after, cmd.Amount.Currency())
		if err != nil {
			return result, err
		}
		return result, insertFinancialEvents(ctx, tx, cmd.Type, cmd.WalletID, result, version, now, cmd.IdempotencyKey, movement > 0, balanceChange{Direction: direction, BalanceBefore: beforeMoney, BalanceAfter: afterMoney, WalletVersion: version})
	}
	return result, insertEvent(ctx, tx, "WagerTransactionRejected", cmd.Type, cmd.ID, cmd.WalletID, result, version, now, cmd.IdempotencyKey)
}

func (s *Service) Reconcile(ctx context.Context, walletID uuid.UUID) (Reconciliation, error) {
	w, err := postgres.NewWalletRepository(s.db).Find(ctx, walletID)
	if err != nil {
		return Reconciliation{}, err
	}
	ledgerBalance, err := postgres.NewLedgerRepository(s.db).Reconstruct(ctx, walletID)
	if err != nil {
		return Reconciliation{}, err
	}
	consistent := w.Balance == ledgerBalance
	if !consistent {
		s.metrics.ObserveReconciliationDivergence()
	}
	return Reconciliation{WalletBalance: w.Balance, LedgerBalance: ledgerBalance, Consistent: consistent}, nil
}

// ProcessPendingReference claims one due pending reversal and resolves it in a
// single PostgreSQL transaction. The row lock and SKIP LOCKED query make the
// worker safe to run in multiple application instances.
func (s *Service) ProcessPendingReference(ctx context.Context, now time.Time, maxAttempts int, backoff time.Duration) (bool, error) {
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	processed := false
	retryScheduled := false
	err := s.db.WithTx(ctx, func(ctx context.Context, tx *postgres.Repository) error {
		repository := postgres.NewWagerTransactionRepository(tx)
		record, err := repository.FindNextPendingReferenceForUpdate(ctx, now)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		processed = true
		var processErr error
		retryScheduled, processErr = s.processPendingReferenceInTx(ctx, tx, record, now, maxAttempts, backoff)
		return processErr
	})
	if err == nil && retryScheduled && s.metrics != nil {
		s.metrics.ObserveRetry("pending_reference")
	}
	return processed, err
}

func (s *Service) processPendingReferenceInTx(ctx context.Context, tx *postgres.Repository, record postgres.WagerTransactionRecord, now time.Time, maxAttempts int, backoff time.Duration) (bool, error) {
	command, err := commandFromRecord(record)
	if err != nil {
		return false, err
	}
	repository := postgres.NewWagerTransactionRepository(tx)
	if err := repository.LockReferenceIdentity(ctx, record.ProviderID, record.ReferenceExternalID); err != nil {
		return false, err
	}
	reference, err := repository.FindByExternal(ctx, record.ProviderID, record.ReferenceExternalID)
	if errors.Is(err, pgx.ErrNoRows) {
		return s.advancePendingReferenceInTx(ctx, tx, record, now, maxAttempts, backoff, FailureCodeReferenceNotFound)
	}
	if err != nil {
		return false, err
	}
	if reference.State != string(wager.Processed) {
		if reference.State == string(wager.Pending) || reference.State == string(wager.PendingReference) {
			return s.advancePendingReferenceInTx(ctx, tx, record, now, maxAttempts, backoff, FailureCodeReferenceNotResolved)
		}
		_, rejectErr := s.rejectPendingReferenceInTx(ctx, tx, record, command, now, FailureCodeReferenceNotSuccessful)
		return false, rejectErr
	}
	if !validReference(command, reference) {
		_, rejectErr := s.rejectPendingReferenceInTx(ctx, tx, record, command, now, FailureCodeReferenceInvalid)
		return false, rejectErr
	}
	walletRecord, err := postgres.NewWalletRepository(tx).FindForUpdate(ctx, command.WalletID)
	if err != nil {
		return false, err
	}
	txDomain, err := wager.New(command.ID.String(), command.ExternalID, command.ProviderID, command.WalletID.String(), command.Type, command.Amount)
	if err != nil {
		return false, err
	}
	if err := txDomain.Transition(wager.PendingReference); err != nil {
		return false, err
	}
	_, err = s.applyOperationInTx(ctx, tx, command, walletRecord, txDomain, &reference, &record, now)
	return false, err
}

func (s *Service) advancePendingReferenceInTx(ctx context.Context, tx *postgres.Repository, record postgres.WagerTransactionRecord, now time.Time, maxAttempts int, backoff time.Duration, exhaustionCode string) (bool, error) {
	record.ReferenceAttempts++
	if record.ReferenceAttempts >= maxAttempts {
		command, err := commandFromRecord(record)
		if err != nil {
			return false, err
		}
		_, rejectErr := s.rejectPendingReferenceInTx(ctx, tx, record, command, now, exhaustionCode)
		return false, rejectErr
	}
	record.ReferenceNextAttemptAt = nextReferenceAttempt(now, backoff, record.ReferenceAttempts)
	record.UpdatedAt = now
	return true, postgres.NewWagerTransactionRepository(tx).UpdatePendingReference(ctx, record)
}

func (s *Service) rejectPendingReferenceInTx(ctx context.Context, tx *postgres.Repository, record postgres.WagerTransactionRecord, command Command, now time.Time, failureCode string) (bool, error) {
	walletRecord, err := postgres.NewWalletRepository(tx).FindForUpdate(ctx, command.WalletID)
	if err != nil {
		return false, err
	}
	txDomain, err := wager.New(command.ID.String(), command.ExternalID, command.ProviderID, command.WalletID.String(), command.Type, command.Amount)
	if err != nil {
		return false, err
	}
	if err := txDomain.Transition(wager.PendingReference); err != nil {
		return false, err
	}
	if err := txDomain.Transition(wager.Rejected); err != nil {
		return false, err
	}
	result := Result{TransactionID: command.ID, State: wager.Rejected, Balance: walletRecord.Balance, Amount: command.Amount, FailureCode: failureCode}
	record.State = string(result.State)
	record.Result, err = marshalResult(result)
	if err != nil {
		return false, err
	}
	record.ReferenceNextAttemptAt = nil
	record.FailureCode = failureCode
	record.UpdatedAt = now
	if err := postgres.NewWagerTransactionRepository(tx).UpdatePendingReference(ctx, record); err != nil {
		return false, err
	}
	return false, insertEvent(ctx, tx, "WagerTransactionRejected", command.Type, command.ID, command.WalletID, result, walletRecord.Version, now, command.IdempotencyKey)
}

func commandFromRecord(record postgres.WagerTransactionRecord) (Command, error) {
	amount, err := moneyFromMinor(record.Amount, record.Currency)
	if err != nil {
		return Command{}, err
	}
	return Command{
		ID:                  record.ID,
		WalletID:            record.WalletID,
		ExternalID:          record.ExternalID,
		ProviderID:          record.ProviderID,
		PlayerID:            record.PlayerID,
		GameID:              record.GameID,
		RoundID:             record.RoundID,
		IdempotencyKey:      record.IdempotencyKey,
		PayloadHash:         record.PayloadHash,
		ReferenceExternalID: record.ReferenceExternalID,
		Type:                wager.Type(record.Type),
		Amount:              amount,
	}, nil
}

func nextReferenceAttempt(now time.Time, base time.Duration, attempt int) *time.Time {
	if base <= 0 {
		return &now
	}
	shift := attempt - 1
	if shift < 0 {
		shift = 0
	}
	if shift > 30 {
		shift = 30
	}
	delay := base * time.Duration(uint64(1)<<shift)
	if delay < 0 {
		delay = 24 * time.Hour
	}
	next := now.Add(delay)
	return &next
}

func validateCommand(cmd Command) error {
	if cmd.ID == uuid.Nil || cmd.WalletID == uuid.Nil || cmd.ExternalID == "" || cmd.ProviderID == "" || cmd.PlayerID == "" || cmd.GameID == "" || cmd.RoundID == "" || cmd.IdempotencyKey == "" {
		return ErrInvalidCommand
	}
	if cmd.PlayerID == legacyIdentity || cmd.GameID == legacyIdentity || cmd.RoundID == legacyIdentity {
		return ErrInvalidCommand
	}
	return nil
}

func findExistingTransaction(ctx context.Context, repository *postgres.WagerTransactionRepository, cmd Command) (postgres.WagerTransactionRecord, bool, error) {
	byID, err := repository.FindByID(ctx, cmd.ID)
	if err == nil {
		if byID.IdempotencyKey != cmd.IdempotencyKey || !sameBusinessPayload(byID, cmd) {
			return postgres.WagerTransactionRecord{}, false, ErrIdempotencyConflict
		}
		if byID.PayloadHash != cmd.PayloadHash {
			return postgres.WagerTransactionRecord{}, false, ErrReplayUnavailable
		}
		return byID, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return postgres.WagerTransactionRecord{}, false, err
	}

	byKey, err := repository.FindByIdempotency(ctx, cmd.ProviderID, cmd.IdempotencyKey)
	if err == nil {
		if !sameBusinessPayload(byKey, cmd) {
			return postgres.WagerTransactionRecord{}, false, ErrIdempotencyConflict
		}
		if byKey.PayloadHash != cmd.PayloadHash {
			return postgres.WagerTransactionRecord{}, false, ErrReplayUnavailable
		}
		return byKey, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return postgres.WagerTransactionRecord{}, false, err
	}

	byExternal, err := repository.FindByExternal(ctx, cmd.ProviderID, cmd.ExternalID)
	if err == nil {
		if byExternal.IdempotencyKey != cmd.IdempotencyKey || !sameBusinessPayload(byExternal, cmd) {
			return postgres.WagerTransactionRecord{}, false, ErrIdempotencyConflict
		}
		if byExternal.PayloadHash != cmd.PayloadHash {
			return postgres.WagerTransactionRecord{}, false, ErrReplayUnavailable
		}
		return byExternal, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return postgres.WagerTransactionRecord{}, false, err
	}
	return postgres.WagerTransactionRecord{}, false, nil
}

func (s *Service) resolveIdentityConflict(ctx context.Context, cmd Command) (Result, error) {
	var result Result
	err := s.db.WithTx(ctx, func(ctx context.Context, tx *postgres.Repository) error {
		existing, found, err := findExistingTransaction(ctx, postgres.NewWagerTransactionRepository(tx), cmd)
		if err != nil {
			return err
		}
		if !found {
			return ErrIdempotencyConflict
		}
		result, err = replayResult(existing)
		return err
	})
	if err != nil {
		return Result{}, err
	}
	return result, nil
}

func isKnownIdentityViolation(err error) bool {
	var postgresErr *pgconn.PgError
	if !errors.As(err, &postgresErr) || postgresErr.Code != "23505" {
		return false
	}
	switch postgresErr.ConstraintName {
	case "wager_transactions_pkey", "wager_transactions_provider_id_external_id_key", "wager_transactions_provider_id_idempotency_key_key", "wager_transactions_reversal_idx":
		return true
	default:
		return false
	}
}

func isReversalIdentityViolation(err error) bool {
	var postgresErr *pgconn.PgError
	return errors.As(err, &postgresErr) && postgresErr.Code == "23505" && postgresErr.ConstraintName == "wager_transactions_reversal_idx"
}

func sameBusinessPayload(record postgres.WagerTransactionRecord, cmd Command) bool {
	return record.ProviderID == cmd.ProviderID && record.ExternalID == cmd.ExternalID && record.WalletID == cmd.WalletID && record.PlayerID == cmd.PlayerID && record.GameID == cmd.GameID && record.RoundID == cmd.RoundID && record.Type == string(cmd.Type) && record.Amount == cmd.Amount.Minor() && record.Currency == cmd.Amount.Currency() && record.ReferenceExternalID == cmd.ReferenceExternalID
}

func replayResult(record postgres.WagerTransactionRecord) (Result, error) {
	if len(record.Result) == 0 {
		return Result{}, ErrReplayUnavailable
	}
	result, err := unmarshalResult(record.Result)
	if err != nil {
		return Result{}, ErrReplayUnavailable
	}
	if result.TransactionID != record.ID || result.State != wager.State(record.State) || result.Balance < 0 || result.Amount.Minor() != record.Amount || result.Amount.Currency() != record.Currency {
		return Result{}, ErrReplayUnavailable
	}
	return result, nil
}

func persistPendingReference(ctx context.Context, tx *postgres.Repository, cmd Command, w postgres.WalletRecord, now time.Time, txDomain wager.Transaction, output *Result) error {
	if err := txDomain.Transition(wager.PendingReference); err != nil {
		return err
	}
	result := Result{TransactionID: cmd.ID, State: wager.PendingReference, Balance: w.Balance, Amount: cmd.Amount}
	if err := insertTransaction(ctx, tx, cmd, result, now); err != nil {
		return err
	}
	*output = result
	return insertEvent(ctx, tx, "WagerTransactionPendingReference", cmd.Type, cmd.ID, cmd.WalletID, result, int64(w.Version), now, cmd.IdempotencyKey)
}

func persistRejected(ctx context.Context, tx *postgres.Repository, cmd Command, w postgres.WalletRecord, now time.Time, txDomain wager.Transaction, output *Result, failureCode string) error {
	if err := txDomain.Transition(wager.Rejected); err != nil {
		return err
	}
	result := Result{TransactionID: cmd.ID, State: wager.Rejected, Balance: w.Balance, Amount: cmd.Amount, FailureCode: failureCode}
	if err := insertTransaction(ctx, tx, cmd, result, now); err != nil {
		return err
	}
	*output = result
	return insertEvent(ctx, tx, "WagerTransactionRejected", cmd.Type, cmd.ID, cmd.WalletID, result, int64(w.Version), now, cmd.IdempotencyKey)
}

func insertTransaction(ctx context.Context, tx *postgres.Repository, cmd Command, result Result, now time.Time) error {
	resultData, err := marshalResult(result)
	if err != nil {
		return err
	}
	return postgres.NewWagerTransactionRepository(tx).Insert(ctx, postgres.WagerTransactionRecord{
		ID: cmd.ID, ExternalID: cmd.ExternalID, ProviderID: cmd.ProviderID, WalletID: cmd.WalletID,
		PlayerID: cmd.PlayerID, GameID: cmd.GameID, RoundID: cmd.RoundID, Type: string(cmd.Type), Amount: cmd.Amount.Minor(),
		Currency: cmd.Amount.Currency(), State: string(result.State), IdempotencyKey: cmd.IdempotencyKey,
		PayloadHash: cmd.PayloadHash, Result: resultData, ReferenceExternalID: cmd.ReferenceExternalID, ReferenceNextAttemptAt: pendingReferenceNextAttempt(result.State, now), FailureCode: result.FailureCode, CreatedAt: now, UpdatedAt: now,
	})
}

func pendingReferenceNextAttempt(state wager.State, now time.Time) *time.Time {
	if state != wager.PendingReference {
		return nil
	}
	return &now
}

func insertFinancialEvents(ctx context.Context, tx *postgres.Repository, typ wager.Type, walletID uuid.UUID, result Result, version int64, now time.Time, correlation string, balanceChanged bool, change balanceChange) error {
	if err := insertEvent(ctx, tx, "WagerTransactionProcessed", typ, result.TransactionID, walletID, result, version, now, correlation); err != nil {
		return err
	}
	if !balanceChanged {
		return nil
	}
	return insertEvent(ctx, tx, "WalletBalanceChanged", typ, walletID, walletID, result, version, now, correlation, change)
}

func insertEvent(ctx context.Context, tx *postgres.Repository, eventType string, typ wager.Type, aggregateID, walletID uuid.UUID, result Result, version int64, now time.Time, correlation string, changes ...balanceChange) error {
	if len(changes) > 0 {
		change := changes[0]
		data, err := json.Marshal(walletBalanceChangedPayload{
			TransactionID: result.TransactionID,
			WalletID:      walletID,
			Direction:     string(change.Direction),
			Money:         result.Amount,
			BalanceBefore: change.BalanceBefore,
			BalanceAfter:  change.BalanceAfter,
			WalletVersion: change.WalletVersion,
			Type:          typ,
			State:         result.State,
		})
		if err != nil {
			return err
		}
		return postgres.NewOutboxRepository(tx).InsertWithMetadata(ctx, uuid.New(), eventType, aggregateID, correlation, result.TransactionID.String(), version, now, data, now)
	}
	payload := eventPayload{TransactionID: result.TransactionID, WalletID: walletID, Type: typ, State: result.State, Amount: result.Amount, Balance: result.Balance, FailureCode: result.FailureCode}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return postgres.NewOutboxRepository(tx).InsertWithMetadata(ctx, uuid.New(), eventType, aggregateID, correlation, result.TransactionID.String(), version, now, data, now)
}

func validReference(cmd Command, reference postgres.WagerTransactionRecord) bool {
	if reference.State != string(wager.Processed) || reference.PlayerID == legacyIdentity || reference.GameID == legacyIdentity || reference.RoundID == legacyIdentity || reference.WalletID != cmd.WalletID || reference.PlayerID != cmd.PlayerID || reference.GameID != cmd.GameID || reference.RoundID != cmd.RoundID || reference.Currency != cmd.Amount.Currency() || reference.Amount != cmd.Amount.Minor() {
		return false
	}
	if cmd.Type == wager.Refund {
		return reference.Type == string(wager.Bet)
	}
	return reference.Type == string(wager.Bet) || reference.Type == string(wager.Win) || reference.Type == string(wager.Refund)
}

func ledgerRecord(entry ledger.Entry) postgres.LedgerRecord {
	return postgres.LedgerRecord{ID: uuid.MustParse(entry.ID()), WalletID: uuid.MustParse(entry.WalletID()), TransactionID: uuid.MustParse(entry.TransactionID()), Direction: string(entry.Direction()), Value: entry.Value().Minor(), Currency: entry.Value().Currency(), BalanceBefore: entry.BalanceBefore().Minor(), BalanceAfter: entry.BalanceAfter().Minor(), Timestamp: entry.Timestamp()}
}

func moneyFromMinor(minor int64, currency string) (money.Money, error) {
	return money.New(fmt.Sprintf("%d.%02d", minor/100, minor%100), currency)
}

func walletFromRecord(w postgres.WalletRecord) (wallet.Wallet, error) {
	m, err := moneyFromMinor(w.Balance, w.Currency)
	if err != nil {
		return wallet.Wallet{}, err
	}
	return wallet.New(w.ID.String(), w.PlayerID, m)
}
