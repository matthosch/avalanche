package ledger

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
)

// The ledger's internal REST API, called only by api; the browser never calls the ledger
// (ADR-0003). These types are its contract, shared by the handler and the Client.

// StoreBalanceResponse is what the Platform owes a Store, computed from Postings.
type StoreBalanceResponse struct {
	StoreID      uuid.UUID `json:"store_id"`
	BalanceCents int64     `json:"balance_cents"`
	Currency     string    `json:"currency"`
}

// FeeRevenueResponse is the Platform's Fee revenue, computed from Postings.
type FeeRevenueResponse struct {
	FeeRevenueCents int64  `json:"fee_revenue_cents"`
	Currency        string `json:"currency"`
}

const (
	storeBalancePath = "/stores/{storeId}/balance"
	feeRevenuePath   = "/platform/fee-revenue"
	// currency is the only one the ledger books (ADR-0004).
	currency = "USD"
)

// NewHandler serves the ledger's internal REST API.
func NewHandler(l *Ledger) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+storeBalancePath, func(w http.ResponseWriter, r *http.Request) {
		storeID, err := uuid.Parse(r.PathValue("storeId"))
		if err != nil {
			http.Error(w, "storeId must be a UUID", http.StatusBadRequest)
			return
		}
		balance, err := l.StoreBalance(r.Context(), storeID)
		if err != nil {
			internalError(w, r, err)
			return
		}
		writeJSON(w, StoreBalanceResponse{StoreID: storeID, BalanceCents: balance, Currency: currency})
	})
	mux.HandleFunc("GET "+feeRevenuePath, func(w http.ResponseWriter, r *http.Request) {
		revenue, err := l.FeeRevenue(r.Context())
		if err != nil {
			internalError(w, r, err)
			return
		}
		writeJSON(w, FeeRevenueResponse{FeeRevenueCents: revenue, Currency: currency})
	})
	return mux
}

func internalError(w http.ResponseWriter, r *http.Request, err error) {
	slog.ErrorContext(r.Context(), "ledger request failed", "method", r.Method, "path", r.URL.Path, "err", err)
	http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
}

func writeJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

// Client calls the ledger's internal REST API.
type Client struct {
	baseURL string
	http    *http.Client
}

// NewClient returns a Client for the ledger at baseURL.
func NewClient(baseURL string) *Client {
	return &Client{baseURL: baseURL, http: &http.Client{Timeout: 5 * time.Second}}
}

// StoreBalance asks the ledger what the Platform owes a Store.
func (c *Client) StoreBalance(ctx context.Context, storeID uuid.UUID) (StoreBalanceResponse, error) {
	var out StoreBalanceResponse
	err := c.get(ctx, "/stores/"+storeID.String()+"/balance", &out)
	return out, err
}

// FeeRevenue asks the ledger for the Platform's Fee revenue.
func (c *Client) FeeRevenue(ctx context.Context) (FeeRevenueResponse, error) {
	var out FeeRevenueResponse
	err := c.get(ctx, feeRevenuePath, &out)
	return out, err
}

func (c *Client) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return fmt.Errorf("ledger: GET %s: %w", path, err)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("ledger: GET %s: %w", path, err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("ledger: GET %s: status %d", path, res.StatusCode)
	}
	if err := json.NewDecoder(res.Body).Decode(out); err != nil {
		return fmt.Errorf("ledger: GET %s: decode: %w", path, err)
	}
	return nil
}
