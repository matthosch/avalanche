// Package api implements the browser-facing HTTP API described by api/openapi.yaml.
package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

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
	strict := gen.NewStrictHandler(&server{commerce: commerceDB, inventory: inv}, nil)
	return gen.HandlerWithOptions(strict, gen.StdHTTPServerOptions{BaseURL: BaseURL})
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
