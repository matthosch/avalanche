// Package api implements the browser-facing HTTP API described by api/openapi.yaml.
package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/matthosch/avalanche/internal/api/gen"
	"github.com/matthosch/avalanche/internal/commerce"
	"github.com/matthosch/avalanche/internal/inventory"
)

// BaseURL is where the API is mounted; it matches the contract's server URL.
const BaseURL = "/api"

type server struct {
	commerce  *pgxpool.Pool
	inventory *inventory.Inventory
}

// NewHandler returns the HTTP handler for the whole API.
func NewHandler(commerceDB *pgxpool.Pool, inv *inventory.Inventory) http.Handler {
	strict := gen.NewStrictHandlerWithOptions(&server{commerce: commerceDB, inventory: inv}, nil,
		gen.StrictHTTPServerOptions{ResponseErrorHandlerFunc: internalError})
	return gen.HandlerWithOptions(strict, gen.StdHTTPServerOptions{BaseURL: BaseURL})
}

// internalError answers a request whose handler failed: the error goes to the log, and the
// client gets a plain 500 that doesn't expose internals such as Redis or Postgres addresses.
func internalError(w http.ResponseWriter, r *http.Request, err error) {
	slog.ErrorContext(r.Context(), "api request failed", "method", r.Method, "path", r.URL.Path, "err", err)
	http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
}

func (s *server) ListFlashSales(ctx context.Context, _ gen.ListFlashSalesRequestObject) (gen.ListFlashSalesResponseObject, error) {
	flashSales, err := commerce.ListFlashSales(ctx, s.commerce)
	if err != nil {
		return nil, err
	}
	out := make([]gen.FlashSale, 0, len(flashSales))
	for _, flashSale := range flashSales {
		unitsRemaining, err := s.unitsRemaining(ctx, flashSale)
		if err != nil {
			return nil, err
		}
		out = append(out, gen.FlashSale{
			Id:             flashSale.ID,
			Store:          gen.Store{Id: flashSale.StoreID, Name: flashSale.StoreName},
			ProductName:    flashSale.ProductName,
			PriceCents:     flashSale.PriceCents,
			Currency:       gen.FlashSaleCurrency(flashSale.Currency),
			Units:          flashSale.Units,
			StartsAt:       flashSale.StartsAt.UTC(),
			EndsAt:         flashSale.EndsAt.UTC(),
			Status:         gen.FlashSaleStatus(flashSale.Status),
			UnitsRemaining: unitsRemaining,
		})
	}
	return gen.ListFlashSales200JSONResponse{FlashSales: out}, nil
}

// unitsRemaining is a LIVE Flash Sale's available units, read from Redis, which owns its
// inventory (ADR-0001). Other Flash Sales have none to show.
func (s *server) unitsRemaining(ctx context.Context, flashSale commerce.FlashSale) (*int32, error) {
	if flashSale.Status != commerce.Live {
		return nil, nil
	}
	counts, err := s.inventory.Counts(ctx, flashSale.ID)
	if errors.Is(err, inventory.ErrNotLoaded) {
		// Stock is loaded before a Flash Sale goes LIVE, so this means Redis lost it. List the
		// Flash Sale without a count rather than failing the whole listing.
		slog.ErrorContext(ctx, "live flash sale has no inventory", "flash_sale_id", flashSale.ID)
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	remaining := int32(counts.Available)
	return &remaining, nil
}

// SignIn is the stub sign-in: no password, just a new Platform-wide Shopper ID that the
// Shopper sends with later requests.
func (s *server) SignIn(context.Context, gen.SignInRequestObject) (gen.SignInResponseObject, error) {
	shopperID, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}
	return gen.SignIn200JSONResponse{ShopperId: shopperID}, nil
}

// reserveRefusals is the reason given to the Shopper for each way the inventory refuses a Hold.
var reserveRefusals = []struct {
	err    error
	reason gen.ReserveRefusalReason
}{
	{inventory.ErrSoldOut, gen.SOLDOUT},
	{inventory.ErrNotLive, gen.NOTLIVE},
}

// Reserve is the hot path. It touches only Redis: whether the Flash Sale is LIVE is the
// inventory's call, never Postgres's (ADR-0001).
func (s *server) Reserve(ctx context.Context, request gen.ReserveRequestObject) (gen.ReserveResponseObject, error) {
	hold, err := s.inventory.Reserve(ctx, request.FlashSaleId, request.Params.XShopperId)
	var holding *inventory.AlreadyHoldingError
	if errors.As(err, &holding) {
		existing := toHold(holding.Hold)
		return gen.Reserve409JSONResponse{Reason: gen.ALREADYHOLDING, Hold: &existing}, nil
	}
	for _, refusal := range reserveRefusals {
		if errors.Is(err, refusal.err) {
			return gen.Reserve409JSONResponse{Reason: refusal.reason}, nil
		}
	}
	if err != nil {
		return nil, err
	}
	return gen.Reserve201JSONResponse(toHold(hold)), nil
}

func toHold(hold inventory.Hold) gen.Hold {
	return gen.Hold{
		Id:          hold.ID,
		FlashSaleId: hold.FlashSaleID,
		State:       gen.HoldState(hold.State),
		ExpiresAt:   hold.ExpiresAt,
	}
}
