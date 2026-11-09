//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
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

	// Fee revenue is Platform-wide and the ledger outlives each seed, so only its growth counts.
	revenueBefore := feeRevenue(t)

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

	// One Order for the Hold, and the Hold is SOLD. The Order count reads a commerce table,
	// which spec #1 keeps tests away from, because nothing public counts Orders yet; it gives
	// way to the Orders = Journal Entries invariant (#8).
	var orders int
	var paymentIntentID string
	if err := pool.QueryRow(ctx, `SELECT count(*), min(payment_intent_id) FROM orders WHERE hold_id = $1`, hold.Id).Scan(&orders, &paymentIntentID); err != nil {
		t.Fatal(err)
	}
	if orders != 1 {
		t.Errorf("found %d Orders for the Hold, want 1", orders)
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

	// The payment provider charged the PaymentIntent the Order records, tagged with the Hold and
	// its Flash Sale, so a payment webhook can find the Hold from the PaymentIntent alone.
	intent, err := paymentsClient().V1PaymentIntents.Retrieve(ctx, paymentIntentID, nil)
	if err != nil {
		t.Fatalf("retrieve payment intent %q: %v", paymentIntentID, err)
	}
	wantMetadata := map[string]string{"hold_id": hold.Id.String(), "flash_sale_id": aurora.Id.String()}
	if intent.Status != stripe.PaymentIntentStatusSucceeded || intent.Amount != 12900 || !maps.Equal(intent.Metadata, wantMetadata) {
		t.Errorf("payment intent = %s %d %v, want succeeded 12900 with metadata %v", intent.Status, intent.Amount, intent.Metadata, wantMetadata)
	}

	// The Order reaches the ledger through the outbox, relay and stream, and is split as in
	// ADR-0004 at the default 500 bps Fee: of 12900, the Platform takes 645 and the Store 12255.
	if got := waitForStoreBalance(t, aurora.Store.Id, 12255); got != 12255 {
		t.Errorf("store balance = %d, want 12255", got)
	}
	if got := feeRevenue(t) - revenueBefore; got != 645 {
		t.Errorf("fee revenue grew by %d, want 645", got)
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
	reqBody, err := json.Marshal(gen.PayJSONRequestBody{PaymentMethod: gen.PayRequestPaymentMethod(paymentMethod)})
	if err != nil {
		t.Fatal(err)
	}
	url := apiURL() + "/api/flash-sales/" + hold.FlashSaleId.String() + "/holds/" + hold.Id.String() + "/pay"
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(reqBody))
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

// waitForStoreBalance polls the Store's balance until it is want or 10 s pass, since the ledger
// books Orders asynchronously, and returns the last balance read.
func waitForStoreBalance(t *testing.T, storeID uuid.UUID, want int64) int64 {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var balance gen.StoreBalance
		getJSON(t, "/api/stores/"+storeID.String()+"/balance", &balance)
		if balance.BalanceCents == want || time.Now().After(deadline) {
			return balance.BalanceCents
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func feeRevenue(t *testing.T) int64 {
	t.Helper()
	var revenue gen.FeeRevenue
	getJSON(t, "/api/platform/fee-revenue", &revenue)
	return revenue.FeeRevenueCents
}

// getJSON GETs path from api, expecting 200, and decodes the body into out.
func getJSON(t *testing.T, path string, out any) {
	t.Helper()
	res, err := http.Get(apiURL() + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d, want 200", path, res.StatusCode)
	}
	if err := json.NewDecoder(res.Body).Decode(out); err != nil {
		t.Fatalf("GET %s: decode: %v", path, err)
	}
}

func paymentsClient() *stripe.Client {
	return payments.NewStripeClient(config.PaymentsURL(), config.PaymentsSecretKey())
}
