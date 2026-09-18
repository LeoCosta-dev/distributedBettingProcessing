package http

import (
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/leonardodacosta/distributedBettingProcessing/internal/application/query"
	"github.com/leonardodacosta/distributedBettingProcessing/internal/infrastructure/postgres"
)

// openWallet handles POST /wallets.
//
// Wallet opening is an internal operation: it requires the internal role and is
// not reachable as a provider operation. The wallet identity is generated here,
// and the opening balance is mandatory, including the explicit 0.00 case, which
// creates the wallet without any financial movement. All financial semantics
// come from the application use case.
func (r *Router) openWallet(w http.ResponseWriter, request *http.Request) {
	var body openWalletRequest
	if err := decodeJSON(w, request, &body); err != nil {
		r.writeError(w, request, err)
		return
	}
	playerID, err := required("playerId", body.PlayerID)
	if err != nil {
		r.writeError(w, request, err)
		return
	}
	opening, err := body.OpeningBalance.toMoney("openingBalance")
	if err != nil {
		r.writeError(w, request, err)
		return
	}
	walletID := uuid.New()
	if err := r.financial.OpenWallet(request.Context(), walletID, playerID, opening, time.Now().UTC()); err != nil {
		r.writeError(w, request, err)
		return
	}
	view, err := r.queries.Wallet(request.Context(), walletID)
	if err != nil {
		r.writeError(w, request, err)
		return
	}
	response, err := walletViewResponse(view)
	if err != nil {
		r.writeError(w, request, err)
		return
	}
	writeJSON(w, http.StatusCreated, response)
}

// wallet handles GET /wallets/{walletId}.
func (r *Router) wallet(w http.ResponseWriter, request *http.Request) {
	walletID, err := parseUUID("walletId", request.PathValue("walletId"))
	if err != nil {
		r.writeError(w, request, err)
		return
	}
	view, err := r.queries.Wallet(request.Context(), walletID)
	if err != nil {
		r.writeError(w, request, err)
		return
	}
	response, err := walletViewResponse(view)
	if err != nil {
		r.writeError(w, request, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

// walletLedger handles GET /wallets/{walletId}/ledger.
//
// The page is fetched by the application boundary through a keyset query, so
// the whole ledger is never loaded into memory. The cursor is opaque: the
// adapter only forwards it.
func (r *Router) walletLedger(w http.ResponseWriter, request *http.Request) {
	walletID, err := parseUUID("walletId", request.PathValue("walletId"))
	if err != nil {
		r.writeError(w, request, err)
		return
	}
	page, err := r.queries.Ledger(request.Context(), walletID, request.URL.Query().Get("cursor"), request.URL.Query().Get("limit"))
	if err != nil {
		r.writeError(w, request, err)
		return
	}
	response := ledgerResponse{
		WalletID: page.WalletID.String(),
		Currency: page.Currency,
		Entries:  make([]ledgerEntryResponse, 0, len(page.Entries)),
	}
	for _, entry := range page.Entries {
		amount, err := wireMoney(entry.ValueMinor, entry.Currency)
		if err != nil {
			r.writeError(w, request, err)
			return
		}
		before, err := wireMoney(entry.BalanceBeforeMinor, entry.Currency)
		if err != nil {
			r.writeError(w, request, err)
			return
		}
		after, err := wireMoney(entry.BalanceAfterMinor, entry.Currency)
		if err != nil {
			r.writeError(w, request, err)
			return
		}
		response.Entries = append(response.Entries, ledgerEntryResponse{
			EntryID:       entry.ID.String(),
			WalletID:      page.WalletID.String(),
			TransactionID: entry.TransactionID.String(),
			Direction:     entry.Direction,
			Amount:        amount,
			BalanceBefore: before,
			BalanceAfter:  after,
			Timestamp:     entry.Timestamp,
		})
	}
	if page.NextCursor != "" {
		next := page.NextCursor
		response.NextCursor = &next
	}
	writeJSON(w, http.StatusOK, response)
}

// reconcileWallet handles POST /wallets/{walletId}/reconciliation.
//
// It is a read-only comparison: it reports what the persisted wallet balance
// and the ledger reconstruction say. An inconsistency is a reconciliation
// result reported with 200, never an authorization to repair anything.
func (r *Router) reconcileWallet(w http.ResponseWriter, request *http.Request) {
	walletID, err := parseUUID("walletId", request.PathValue("walletId"))
	if err != nil {
		r.writeError(w, request, err)
		return
	}
	view, err := r.queries.Wallet(request.Context(), walletID)
	if err != nil {
		r.writeError(w, request, err)
		return
	}
	reconciliation, err := r.financial.Reconcile(request.Context(), walletID)
	if errors.Is(err, postgres.ErrLedgerInconsistent) {
		// The stored ledger is not internally consistent, so no ledger balance
		// can be reconstructed. The inconsistency itself is the result.
		walletBalance, balanceErr := wireMoney(view.BalanceMinor, view.Currency)
		if balanceErr != nil {
			r.writeError(w, request, balanceErr)
			return
		}
		writeJSON(w, http.StatusOK, reconciliationResponse{
			WalletID:      walletID.String(),
			Currency:      view.Currency,
			WalletBalance: walletBalance,
			Consistent:    false,
		})
		return
	}
	if err != nil {
		r.writeError(w, request, err)
		return
	}
	walletBalance, err := wireMoney(reconciliation.WalletBalance, view.Currency)
	if err != nil {
		r.writeError(w, request, err)
		return
	}
	ledgerBalance, err := wireMoney(reconciliation.LedgerBalance, view.Currency)
	if err != nil {
		r.writeError(w, request, err)
		return
	}
	writeJSON(w, http.StatusOK, reconciliationResponse{
		WalletID:      walletID.String(),
		Currency:      view.Currency,
		WalletBalance: walletBalance,
		LedgerBalance: &ledgerBalance,
		Consistent:    reconciliation.Consistent,
	})
}

func walletViewResponse(view query.WalletView) (walletResponse, error) {
	balance, err := wireMoney(view.BalanceMinor, view.Currency)
	if err != nil {
		return walletResponse{}, err
	}
	return walletResponse{
		WalletID:  view.ID.String(),
		PlayerID:  view.PlayerID,
		Currency:  view.Currency,
		Balance:   balance,
		Version:   view.Version,
		CreatedAt: view.CreatedAt,
		UpdatedAt: view.UpdatedAt,
	}, nil
}
