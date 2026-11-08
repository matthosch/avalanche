// Package api implements the browser-facing HTTP API described by api/openapi.yaml.
package api

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/matthosch/avalanche/internal/api/gen"
	"github.com/matthosch/avalanche/internal/commerce"
)

// BaseURL is where the API is mounted; it matches the contract's server URL.
const BaseURL = "/api"

type server struct {
	commerce *pgxpool.Pool
}

// NewHandler returns the HTTP handler for the whole API.
func NewHandler(commerceDB *pgxpool.Pool) http.Handler {
	strict := gen.NewStrictHandler(&server{commerce: commerceDB}, nil)
	return gen.HandlerWithOptions(strict, gen.StdHTTPServerOptions{BaseURL: BaseURL})
}

func (s *server) ListFlashSales(ctx context.Context, _ gen.ListFlashSalesRequestObject) (gen.ListFlashSalesResponseObject, error) {
	flashSales, err := commerce.ListFlashSales(ctx, s.commerce)
	if err != nil {
		return nil, err
	}
	out := make([]gen.FlashSale, 0, len(flashSales))
	for _, flashSale := range flashSales {
		out = append(out, gen.FlashSale{
			Id:          flashSale.ID,
			Store:       gen.Store{Id: flashSale.StoreID, Name: flashSale.StoreName},
			ProductName: flashSale.ProductName,
			PriceCents:  flashSale.PriceCents,
			Currency:    gen.FlashSaleCurrency(flashSale.Currency),
			Units:       flashSale.Units,
			StartsAt:    flashSale.StartsAt.UTC(),
			EndsAt:      flashSale.EndsAt.UTC(),
		})
	}
	return gen.ListFlashSales200JSONResponse{FlashSales: out}, nil
}
