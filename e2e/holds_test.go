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

	"github.com/matthosch/avalanche/internal/api/gen"
	"github.com/matthosch/avalanche/internal/config"
	"github.com/matthosch/avalanche/internal/seed"
)

func TestSignedInShopperReservesAHold(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, config.CommerceDatabaseURL())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	// As in TestFlashSalesGoLiveAndEndOnSchedule: Aurora is LIVE, Ridgeline SCHEDULED, Halo ENDED.
	if err := seed.Run(ctx, pool, time.Now().UTC().Truncate(time.Second).Add(-26*time.Minute)); err != nil {
		t.Fatalf("seed: %v", err)
	}
	flashSales := waitForStatuses(t, map[string]gen.FlashSaleStatus{
		"Aurora Trail Runner":      gen.LIVE,
		"Ridgeline Daypack":        gen.SCHEDULED,
		"Halo Wireless Headphones": gen.ENDED,
	})
	aurora := flashSales["Aurora Trail Runner"]

	shopper := signIn(t)
	if shopper.Version() != 7 {
		t.Errorf("shopper ID %s is UUID version %d, want 7", shopper, shopper.Version())
	}

	status, body := reserve(t, aurora.Id, shopper)
	if status != http.StatusCreated {
		t.Fatalf("reserve: status %d (%s), want 201", status, body)
	}
	var hold gen.Hold
	if err := json.Unmarshal(body, &hold); err != nil {
		t.Fatal(err)
	}
	if hold.FlashSaleId != aurora.Id || hold.State != gen.HELD || hold.Id.Version() != 7 {
		t.Errorf("hold = %+v, want a UUIDv7 HELD Hold on %s", hold, aurora.Id)
	}
	if left := time.Until(hold.ExpiresAt); left < time.Minute || left > 3*time.Minute {
		t.Errorf("hold expires in %s, want about 2m", left)
	}

	if unitsLeft := waitForStatuses(t, nil)["Aurora Trail Runner"].UnitsRemaining; unitsLeft == nil || *unitsLeft != 499 {
		t.Errorf("Aurora units remaining = %s, want 499", showUnits(unitsLeft))
	}

	// Reserving again (a retry, a lost reply, a reload) hands back the Shopper's Hold.
	status, body = reserve(t, aurora.Id, shopper)
	var again gen.ReserveRefusal
	if err := json.Unmarshal(body, &again); err != nil {
		t.Fatalf("reserve again: status %d, body %s: %v", status, body, err)
	}
	if status != http.StatusConflict || again.Reason != gen.ALREADYHOLDING {
		t.Errorf("reserve again: status %d, body %s; want 409 ALREADY_HOLDING", status, body)
	}
	if again.Hold == nil || again.Hold.Id != hold.Id || !again.Hold.ExpiresAt.Equal(hold.ExpiresAt) || again.Hold.State != gen.HELD {
		t.Errorf("reserve again: hold %+v, want the first Hold %+v", again.Hold, hold)
	}

	for _, refused := range []struct {
		name      string
		flashSale uuid.UUID
		want      gen.ReserveRefusalReason
	}{
		{"scheduled Flash Sale", flashSales["Ridgeline Daypack"].Id, gen.NOTLIVE},
		{"ended Flash Sale", flashSales["Halo Wireless Headphones"].Id, gen.NOTLIVE},
		{"unknown Flash Sale", uuid.Must(uuid.NewV7()), gen.NOTLIVE},
	} {
		status, body := reserve(t, refused.flashSale, shopper)
		var refusal gen.ReserveRefusal
		_ = json.Unmarshal(body, &refusal)
		if status != http.StatusConflict || refusal.Reason != refused.want {
			t.Errorf("%s: status %d, body %s; want 409 %s", refused.name, status, body, refused.want)
		}
	}
}

// waitForStatuses polls the listing until each named Flash Sale has its wanted status, as the
// worker moves them on its own tick, and returns the listing by product name.
func waitForStatuses(t *testing.T, wants map[string]gen.FlashSaleStatus) map[string]gen.FlashSale {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		byProduct := map[string]gen.FlashSale{}
		for _, flashSale := range listFlashSales(t) {
			byProduct[flashSale.ProductName] = flashSale
		}
		ready := true
		for product, status := range wants {
			if byProduct[product].Status != status {
				ready = false
			}
		}
		if ready {
			return byProduct
		}
		if time.Now().After(deadline) {
			t.Fatalf("Flash Sales did not reach %v in time", wants)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func signIn(t *testing.T) uuid.UUID {
	t.Helper()
	res, err := http.Post(apiURL()+"/api/sign-in", "application/json", nil)
	if err != nil {
		t.Fatalf("sign in: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("sign in: status %d, want 200", res.StatusCode)
	}
	var body gen.SignIn200JSONResponse
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	return body.ShopperId
}

// reserve asks for a Hold on flashSale as shopper and returns the status and raw body.
func reserve(t *testing.T, flashSale, shopper uuid.UUID) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, apiURL()+"/api/flash-sales/"+flashSale.String()+"/holds", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Shopper-Id", shopper.String())
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	defer res.Body.Close()
	var body bytes.Buffer
	if _, err := body.ReadFrom(res.Body); err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, body.Bytes()
}

func apiURL() string {
	return config.Env("API_URL", "http://localhost:8080")
}
