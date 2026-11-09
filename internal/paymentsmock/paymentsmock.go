// Package paymentsmock is a Stripe-compatible subset of the PaymentIntents API, for the real
// stripe-go SDK to talk to (ADR-0007): create (honoring Idempotency-Key), confirm and
// retrieve. It keeps everything in memory, so a restart forgets every PaymentIntent.
package paymentsmock

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"time"
)

// succeedingPaymentMethods lists the test payment methods that charge successfully.
var succeedingPaymentMethods = map[string]bool{"pm_card_visa": true}

type paymentIntent struct {
	ID            string            `json:"id"`
	Object        string            `json:"object"`
	Amount        int64             `json:"amount"`
	Currency      string            `json:"currency"`
	Status        string            `json:"status"`
	Metadata      map[string]string `json:"metadata"`
	PaymentMethod *string           `json:"payment_method"`
	ClientSecret  string            `json:"client_secret"`
	Created       int64             `json:"created"`
	Livemode      bool              `json:"livemode"`
}

// savedResponse is what a POST with an Idempotency-Key answered, replayed to every later
// request with that key.
type savedResponse struct {
	requestHash string
	status      int
	body        []byte
}

type mock struct {
	// mu is held for the whole of every request: the mock serves a handful of payments, so
	// serializing them is simpler than settling concurrent requests with the same key.
	mu      sync.Mutex
	intents map[string]*paymentIntent
	saved   map[string]savedResponse
}

// NewHandler returns the mock's HTTP handler, with no PaymentIntents yet.
func NewHandler() http.Handler {
	m := &mock{intents: map[string]*paymentIntent{}, saved: map[string]savedResponse{}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/payment_intents", m.idempotent(m.create))
	mux.HandleFunc("POST /v1/payment_intents/{id}/confirm", m.idempotent(m.confirm))
	mux.HandleFunc("GET /v1/payment_intents/{id}", m.locked(m.retrieve))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	return mux
}

func (m *mock) locked(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		h(w, r)
	}
}

// idempotent answers a repeated Idempotency-Key with the response saved the first time, as
// Stripe does, and refuses a key reused for a different request.
func (m *mock) idempotent(h http.HandlerFunc) http.HandlerFunc {
	return m.locked(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("Idempotency-Key")
		if key == "" {
			h(w, r)
			return
		}
		body, err := readBody(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request_error", "", "", err.Error())
			return
		}
		sum := sha256.Sum256(append([]byte(r.Method+" "+r.URL.Path+"\n"), body...))
		hash := hex.EncodeToString(sum[:])
		if saved, ok := m.saved[key]; ok {
			if saved.requestHash != hash {
				writeError(w, http.StatusBadRequest, "idempotency_error", "", "",
					"Keys for idempotent requests can only be used with the same parameters they were first used with.")
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Idempotent-Replayed", "true")
			w.WriteHeader(saved.status)
			_, _ = w.Write(saved.body)
			return
		}
		rec := httptest.NewRecorder()
		h(rec, r)
		m.saved[key] = savedResponse{requestHash: hash, status: rec.Code, body: rec.Body.Bytes()}
		for k, v := range rec.Header() {
			w.Header()[k] = v
		}
		w.WriteHeader(rec.Code)
		_, _ = w.Write(rec.Body.Bytes())
	})
}

// readBody reads the request body and puts it back, so the handler can still parse the form.
func readBody(r *http.Request) ([]byte, error) {
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r.Body); err != nil {
		return nil, err
	}
	r.Body.Close()
	r.Body = readCloser{bytes.NewReader(buf.Bytes())}
	return buf.Bytes(), nil
}

type readCloser struct{ *bytes.Reader }

func (readCloser) Close() error { return nil }

func (m *mock) create(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "", "", err.Error())
		return
	}
	amount, err := strconv.ParseInt(r.PostForm.Get("amount"), 10, 64)
	if err != nil || amount <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "parameter_invalid_integer", "amount",
			"Amount must be a positive integer of the smallest currency unit.")
		return
	}
	currency := r.PostForm.Get("currency")
	if currency == "" {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "parameter_missing", "currency",
			"Missing required param: currency.")
		return
	}
	metadata := map[string]string{}
	for field, values := range r.PostForm {
		if key, ok := strings.CutPrefix(field, "metadata["); ok && strings.HasSuffix(key, "]") {
			metadata[strings.TrimSuffix(key, "]")] = values[0]
		}
	}
	id := "pi_" + randomHex(12)
	intent := &paymentIntent{
		ID:           id,
		Object:       "payment_intent",
		Amount:       amount,
		Currency:     strings.ToLower(currency),
		Status:       "requires_payment_method",
		Metadata:     metadata,
		ClientSecret: id + "_secret_" + randomHex(12),
		Created:      time.Now().Unix(),
	}
	if pm := r.PostForm.Get("payment_method"); pm != "" {
		intent.PaymentMethod = &pm
		intent.Status = "requires_confirmation"
	}
	m.intents[id] = intent
	writeJSON(w, http.StatusOK, intent)
}

func (m *mock) confirm(w http.ResponseWriter, r *http.Request) {
	intent, ok := m.find(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "", "", err.Error())
		return
	}
	if intent.Status != "requires_payment_method" && intent.Status != "requires_confirmation" {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "payment_intent_unexpected_state", "",
			fmt.Sprintf("This PaymentIntent's status is %s, so it can't be confirmed.", intent.Status))
		return
	}
	pm := r.PostForm.Get("payment_method")
	if pm == "" && intent.PaymentMethod != nil {
		pm = *intent.PaymentMethod
	}
	if !succeedingPaymentMethods[pm] {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "resource_missing", "payment_method",
			fmt.Sprintf("No such PaymentMethod: '%s'", pm))
		return
	}
	intent.PaymentMethod = &pm
	intent.Status = "succeeded"
	writeJSON(w, http.StatusOK, intent)
}

func (m *mock) retrieve(w http.ResponseWriter, r *http.Request) {
	if intent, ok := m.find(w, r); ok {
		writeJSON(w, http.StatusOK, intent)
	}
}

// find looks up the PaymentIntent named in the path, answering 404 when there is none.
func (m *mock) find(w http.ResponseWriter, r *http.Request) (*paymentIntent, bool) {
	id := r.PathValue("id")
	intent, ok := m.intents[id]
	if !ok {
		writeError(w, http.StatusNotFound, "invalid_request_error", "resource_missing", "intent",
			fmt.Sprintf("No such payment_intent: '%s'", id))
	}
	return intent, ok
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError answers in Stripe's error shape, which stripe-go decodes into a *stripe.Error.
func writeError(w http.ResponseWriter, status int, errType, code, param, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{
		"type": errType, "code": code, "param": param, "message": message,
	}})
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
