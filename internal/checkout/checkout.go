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

// ErrHoldNotFound means the Shopper has no such Hold on the Flash Sale. Another Shopper's
// Hold counts as not found, so only the Hold's own Shopper can pay for it.
var ErrHoldNotFound = errors.New("checkout: hold not found")

// ErrNotHeld means the Hold has left HELD, so it can't be paid for: it is already PAYING or
// SOLD, or was RELEASED. Pay returns it as a *NotHeldError carrying the Hold's state.
var ErrNotHeld = errors.New("checkout: hold not held")

// NotHeldError is Pay's refusal of a Hold that has left HELD. It matches ErrNotHeld with
// errors.Is.
type NotHeldError struct {
	State inventory.HoldState
}

func (e *NotHeldError) Error() string { return fmt.Sprintf("%v: hold is %s", ErrNotHeld, e.State) }

func (e *NotHeldError) Is(target error) bool { return target == ErrNotHeld }

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
// It refuses with ErrHoldNotFound when the Shopper has no such Hold on the Flash Sale, and
// with a *NotHeldError when the Hold has left HELD. These are the only refusals: once the
// Hold is PAYING, the inventory finding it in the wrong state is a failure, not a refusal.
//
// Once the Hold is PAYING, any failure leaves it PAYING: the reaper never releases a PAYING
// Hold, so a unit that may have been paid for is never put back on sale (ADR-0006).
func (c *Checkout) Pay(ctx context.Context, shopperID, flashSaleID, holdID uuid.UUID, paymentMethod string) (commerce.Order, error) {
	flashSale, err := commerce.GetFlashSale(ctx, c.commerce, flashSaleID)
	if errors.Is(err, commerce.ErrFlashSaleNotFound) {
		return commerce.Order{}, ErrHoldNotFound
	}
	if err != nil {
		return commerce.Order{}, err
	}
	_, err = c.inventory.BeginPayment(ctx, flashSaleID, holdID, shopperID)
	if errors.Is(err, inventory.ErrHoldNotFound) {
		return commerce.Order{}, ErrHoldNotFound
	}
	var wrong *inventory.WrongStateError
	if errors.As(err, &wrong) {
		return commerce.Order{}, &NotHeldError{State: wrong.Got}
	}
	if err != nil {
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
		return commerce.Order{}, fmt.Errorf("checkout: record payment intent for hold %s: %w", holdID, err)
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
		return commerce.Order{}, fmt.Errorf("checkout: confirm hold %s: %w", holdID, err)
	}
	return order, nil
}
