//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stripe/stripe-go/v87"

	"github.com/matthosch/avalanche/internal/api/gen"
	"github.com/matthosch/avalanche/internal/config"
	"github.com/matthosch/avalanche/internal/payments"
	"github.com/matthosch/avalanche/internal/seed"
)

func TestShopperPaysForAHoldAndGetsAnOrder(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, config.CommerceDatabaseURL())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	// As in TestSignedInShopperReservesAHold: Aurora is LIVE.
	if err := seed.Run(ctx, pool, time.Now().UTC().Truncate(time.Second).Add(-26*time.Minute)); err != nil {
		t.Fatalf("seed: %v", err)
	}
	aurora := waitForStatuses(t, map[string]gen.FlashSaleStatus{"Aurora Trail Runner": gen.LIVE})["Aurora Trail Runner"]

	shopper := signIn(t)
	status, body := reserve(t, aurora.Id, shopper)
	if status != http.StatusCreated {
		t.Fatalf("reserve: status %d (%s), want 201", status, body)
	}
	var hold gen.Hold
	if err := json.Unmarshal(body, &hold); err != nil {
		t.Fatal(err)
	}

	// Only the Hold's own Shopper may pay for it.
	if status, body := pay(t, hold, signIn(t), "pm_card_visa"); status != http.StatusNotFound {
		t.Errorf("another Shopper pays: status %d (%s), want 404", status, body)
	}

	status, body = pay(t, hold, shopper, "pm_card_visa")
	if status != http.StatusOK {
		t.Fatalf("pay: status %d (%s), want 200", status, body)
	}
	var order gen.Order
	if err := json.Unmarshal(body, &order); err != nil {
		t.Fatal(err)
	}
	if order.HoldId != hold.Id || order.FlashSaleId != aurora.Id || order.AmountCents != 12900 || order.Currency != "USD" || order.Id.Version() != 7 {
		t.Errorf("order = %+v, want a UUIDv7 Order of 12900 USD for Hold %s on %s", order, hold.Id, aurora.Id)
	}

	// One Order and one OrderCompleted outbox row for the Hold, and the Hold is SOLD.
	var orders, outboxRows int
	var paymentIntentID string
	if err := pool.QueryRow(ctx, `SELECT count(*), min(payment_intent_id) FROM orders WHERE hold_id = $1`, hold.Id).Scan(&orders, &paymentIntentID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE event_type = 'OrderCompleted' AND payload->>'order_id' = $1`, order.Id.String()).Scan(&outboxRows); err != nil {
		t.Fatal(err)
	}
	if orders != 1 || outboxRows != 1 {
		t.Errorf("found %d Orders and %d outbox rows for the Hold, want 1 and 1", orders, outboxRows)
	}
	status, body = reserve(t, aurora.Id, shopper)
	var again gen.ReserveRefusal
	_ = json.Unmarshal(body, &again)
	if status != http.StatusConflict || again.Hold == nil || again.Hold.Id != hold.Id || again.Hold.State != gen.SOLD {
		t.Errorf("reserve after paying: status %d, body %s; want 409 with the Hold SOLD", status, body)
	}
	if unitsLeft := waitForStatuses(t, nil)["Aurora Trail Runner"].UnitsRemaining; unitsLeft == nil || *unitsLeft != 499 {
		t.Errorf("Aurora units remaining = %s, want 499", showUnits(unitsLeft))
	}

	// The payment provider charged the PaymentIntent the Order records, tagged with the Hold.
	intent, err := paymentsClient().V1PaymentIntents.Retrieve(ctx, paymentIntentID, nil)
	if err != nil {
		t.Fatalf("retrieve payment intent %q: %v", paymentIntentID, err)
	}
	if intent.Status != stripe.PaymentIntentStatusSucceeded || intent.Amount != 12900 || intent.Metadata["hold_id"] != hold.Id.String() {
		t.Errorf("payment intent = %s %d %v, want succeeded 12900 with hold_id %s", intent.Status, intent.Amount, intent.Metadata, hold.Id)
	}

	// A SOLD Hold can't be paid for again.
	if status, body := pay(t, hold, shopper, "pm_card_visa"); status != http.StatusConflict {
		t.Errorf("pay again: status %d (%s), want 409", status, body)
	}
}

// The payments mock, driven through the real Stripe SDK (ADR-0007): creating a PaymentIntent
// twice with one Idempotency-Key gives back the first one.
func TestPaymentsMockHonorsIdempotencyKey(t *testing.T) {
	ctx := context.Background()
	client := paymentsClient()
	key := uuid.Must(uuid.NewV7()).String()
	create := func() *stripe.PaymentIntent {
		t.Helper()
		params := &stripe.PaymentIntentCreateParams{Amount: stripe.Int64(4500), Currency: stripe.String("usd")}
		params.SetIdempotencyKey(key)
		params.AddMetadata("hold_id", key)
		intent, err := client.V1PaymentIntents.Create(ctx, params)
		if err != nil {
			t.Fatalf("create payment intent: %v", err)
		}
		return intent
	}

	first, second := create(), create()

	if second.ID != first.ID {
		t.Errorf("second create with the same key gave %s, want %s", second.ID, first.ID)
	}
	if first.Status != stripe.PaymentIntentStatusRequiresPaymentMethod || first.Amount != 4500 || first.Metadata["hold_id"] != key {
		t.Errorf("created = %s %d %v, want requires_payment_method 4500 with hold_id", first.Status, first.Amount, first.Metadata)
	}
	confirmed, err := client.V1PaymentIntents.Confirm(ctx, first.ID, &stripe.PaymentIntentConfirmParams{PaymentMethod: stripe.String("pm_card_visa")})
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if confirmed.Status != stripe.PaymentIntentStatusSucceeded {
		t.Errorf("confirmed status = %s, want succeeded", confirmed.Status)
	}
	if replayed := create(); replayed.ID != first.ID || replayed.Status != stripe.PaymentIntentStatusRequiresPaymentMethod {
		t.Errorf("create replayed after confirm = %s %s, want the saved create response for %s", replayed.ID, replayed.Status, first.ID)
	}
}

// pay pays for hold as shopper with a test payment method and returns the status and raw body.
func pay(t *testing.T, hold gen.Hold, shopper uuid.UUID, paymentMethod string) (int, []byte) {
	t.Helper()
	reqBody, err := json.Marshal(gen.PayJSONRequestBody{FlashSaleId: hold.FlashSaleId, PaymentMethod: gen.PayRequestPaymentMethod(paymentMethod)})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, apiURL()+"/api/holds/"+hold.Id.String()+"/pay", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Shopper-Id", shopper.String())
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("pay: %v", err)
	}
	defer res.Body.Close()
	var body bytes.Buffer
	if _, err := body.ReadFrom(res.Body); err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, body.Bytes()
}

func paymentsClient() *stripe.Client {
	return payments.NewStripeClient(config.PaymentsURL(), config.PaymentsSecretKey())
}
