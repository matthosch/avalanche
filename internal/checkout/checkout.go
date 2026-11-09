// Package checkout turns a Shopper's HELD Hold into an Order: it moves the Hold to PAYING,
// charges it through the payment provider, writes the Order, then marks the Hold SOLD.
package checkout

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stripe/stripe-go/v87"

	"github.com/matthosch/avalanche/internal/commerce"
	"github.com/matthosch/avalanche/internal/inventory"
)

// Checkout pays for Holds.
type Checkout struct {
	commerce  *pgxpool.Pool
	inventory *inventory.Inventory
	payments  *stripe.Client
	feeBPS    int
}

// New returns a Checkout that records Orders in commerceDB, moves Holds in inv, charges
// through payments, and captures feeBPS as each Order's Fee rate.
func New(commerceDB *pgxpool.Pool, inv *inventory.Inventory, payments *stripe.Client, feeBPS int) *Checkout {
	return &Checkout{commerce: commerceDB, inventory: inv, payments: payments, feeBPS: feeBPS}
}

// Pay charges a Shopper's HELD Hold with paymentMethod and returns the Order it becomes.
//
// It refuses with inventory.ErrHoldNotFound when the Shopper has no such Hold on the Flash
// Sale (another Shopper's Hold included), and with an *inventory.NotHeldError when the Hold
// has left HELD.
//
// Once the Hold is PAYING, any failure leaves it PAYING: the reaper never releases a PAYING
// Hold, so a unit that may have been paid for is never put back on sale (ADR-0006).
func (c *Checkout) Pay(ctx context.Context, shopperID, flashSaleID, holdID uuid.UUID, paymentMethod string) (commerce.Order, error) {
	flashSale, err := commerce.GetFlashSale(ctx, c.commerce, flashSaleID)
	if errors.Is(err, commerce.ErrFlashSaleNotFound) {
		return commerce.Order{}, inventory.ErrHoldNotFound
	}
	if err != nil {
		return commerce.Order{}, err
	}
	if _, err := c.inventory.BeginPayment(ctx, flashSaleID, holdID, shopperID); err != nil {
		return commerce.Order{}, err
	}

	// Keyed by the Hold, so a create that is sent again gets the first PaymentIntent back,
	// never a second one (ADR-0007).
	create := &stripe.PaymentIntentCreateParams{
		Amount:   stripe.Int64(flashSale.PriceCents),
		Currency: stripe.String(strings.ToLower(flashSale.Currency)),
	}
	create.SetIdempotencyKey(holdID.String())
	create.AddMetadata("hold_id", holdID.String())
	intent, err := c.payments.V1PaymentIntents.Create(ctx, create)
	if err != nil {
		return commerce.Order{}, fmt.Errorf("checkout: create payment intent for hold %s: %w", holdID, err)
	}
	// Stored before confirming, so recovery can always find the charge by its ID.
	if err := c.inventory.RecordPaymentIntent(ctx, flashSaleID, holdID, intent.ID); err != nil {
		return commerce.Order{}, err
	}
	intent, err = c.payments.V1PaymentIntents.Confirm(ctx, intent.ID, &stripe.PaymentIntentConfirmParams{
		PaymentMethod: stripe.String(paymentMethod),
	})
	if err != nil {
		return commerce.Order{}, fmt.Errorf("checkout: confirm payment intent for hold %s: %w", holdID, err)
	}
	if intent.Status != stripe.PaymentIntentStatusSucceeded {
		return commerce.Order{}, fmt.Errorf("checkout: payment intent %s for hold %s is %s, not succeeded", intent.ID, holdID, intent.Status)
	}

	// The Order first, then SOLD: the Order is the durable record that payment happened
	// (ADR-0006).
	order, err := commerce.RecordOrder(ctx, c.commerce, commerce.NewOrder{
		HoldID:          holdID,
		FlashSaleID:     flashSaleID,
		StoreID:         flashSale.StoreID,
		ShopperID:       shopperID,
		AmountCents:     flashSale.PriceCents,
		Currency:        flashSale.Currency,
		PaymentIntentID: intent.ID,
		FeeBPS:          c.feeBPS,
	})
	if err != nil {
		return commerce.Order{}, err
	}
	if err := c.inventory.Confirm(ctx, flashSaleID, holdID); err != nil {
		return commerce.Order{}, err
	}
	return order, nil
}
