package main

import (
	"fmt"
	"math"
)

// The scenes show only behaviour that is built and covered by tests in the main module.

// tracerBullet follows one paid Order through every service (issues #4–#6).
func tracerBullet() *flowScene {
	const top, mid, bottom = 78.0, 130.0, 314.0
	col := func(i int) float64 { return 30 + float64(i)*240 }
	browser := &flowNode{id: "browser", title: "Browser", sub: "storefront · React", x: col(0), y: mid, w: 180, h: 80, accent: colBlue,
		lines: []string{"Shopper signed in"}}
	api := &flowNode{id: "api", title: "api", sub: "Go · OpenAPI", x: col(1), y: mid, w: 180, h: 80, accent: colBlue,
		lines: []string{"reserve · pay", "balances"}}
	redis := &flowNode{id: "redis", title: "Redis", sub: "inventory · Lua scripts", x: col(2), y: top, w: 180, h: 80, accent: colRed,
		lines: []string{"available 500", "Hold —"}}
	mock := &flowNode{id: "mock", title: "payments-mock", sub: "Stripe-compatible", x: col(2), y: 184, w: 180, h: 80, accent: colAmber,
		lines: []string{"no PaymentIntent"}}
	commerce := &flowNode{id: "commerce", title: "commerce DB", sub: "Postgres", x: col(3), y: mid, w: 180, h: 80, accent: colCyan,
		lines: []string{"orders 0", "outbox 0 waiting"}}
	worker := &flowNode{id: "worker", title: "worker", sub: "outbox relay", x: col(3), y: bottom, w: 180, h: 84, accent: colPurple,
		lines: []string{"polls every 200 ms"}}
	stream := &flowNode{id: "stream", title: "orders.completed", sub: "Redis Stream", x: col(2), y: bottom, w: 180, h: 84, accent: colRed,
		lines: []string{"0 messages"}}
	ledger := &flowNode{id: "ledger", title: "ledger", sub: "consumer group", x: col(1), y: bottom, w: 180, h: 84, accent: colPurple,
		lines: []string{"group: ledger"}}
	ledgerDB := &flowNode{id: "ledgerdb", title: "ledger DB", sub: "Postgres · append-only", x: col(0), y: bottom, w: 180, h: 84, accent: colCyan,
		lines: []string{"0 Journal Entries"}}

	step := func(from, to, label string, c rgb, set map[string][]string) flowStep {
		return flowStep{from: from, to: to, label: label, col: c, set: set}
	}
	return &flowScene{
		title:     "One paid Order, end to end",
		nodes:     []*flowNode{browser, api, redis, mock, commerce, worker, stream, ledger, ledgerDB},
		edges:     [][2]string{{"browser", "api"}, {"api", "redis"}, {"api", "mock"}, {"api", "commerce"}, {"commerce", "worker"}, {"worker", "stream"}, {"stream", "ledger"}, {"ledger", "ledgerdb"}, {"api", "ledger"}},
		lanes:     []lane{{"SYNCHRONOUS · THE SHOPPER WAITS", 66}, {"ASYNCHRONOUS · NOBODY WAITS ON THE LEDGER", 302}},
		separator: 284,
		stepDur:   0.75,
		beats: []flowBeat{
			{"Reserve: one Lua script checks stock and creates the Hold in a single atomic step, so the last unit can't be sold twice.", []flowStep{
				step("browser", "api", "POST /holds", colBlue, nil),
				step("api", "redis", "reserve.lua", colRed, map[string][]string{"redis": {"available 499", "Hold HELD"}}),
				step("api", "browser", "201 Hold", colGreen, map[string][]string{"browser": {"Hold HELD", "2:00 to pay"}}),
			}},
			{"Pay: the Hold moves HELD → PAYING first, so the expiry reaper can never release a unit that may be paid for.", []flowStep{
				step("browser", "api", "POST /pay", colBlue, nil),
				step("api", "redis", "begin_payment", colRed, map[string][]string{"redis": {"available 499", "Hold PAYING"}}),
			}},
			{"Charge through the real stripe-go SDK. Idempotency-Key = hold_id, so a retried create never charges twice.", []flowStep{
				step("api", "mock", "create PaymentIntent", colAmber, nil),
				step("api", "mock", "confirm pm_card_visa", colAmber, map[string][]string{"mock": {"pi_… succeeded", "$129.00"}}),
			}},
			{"The Order and its OrderCompleted outbox row commit in one transaction, before the Hold is marked SOLD.", []flowStep{
				step("api", "commerce", "Order + outbox, 1 tx", colCyan, map[string][]string{"commerce": {"orders 1", "outbox 1 waiting"}}),
			}},
			{"Only then PAYING → SOLD, and the Shopper gets their Order back. Everything after this is asynchronous.", []flowStep{
				step("api", "redis", "confirm.lua", colRed, map[string][]string{"redis": {"available 499", "Hold SOLD"}}),
				step("api", "browser", "200 Order", colGreen, map[string][]string{"browser": {"Order $129.00", "paid"}}),
			}},
			{"The relay claims outbox rows FOR UPDATE SKIP LOCKED and publishes them to the Redis Stream.", []flowStep{
				step("commerce", "worker", "claim row", colPurple, nil),
				step("worker", "stream", "XADD OrderCompleted", colPurple, map[string][]string{"stream": {"1 OrderCompleted", "pending"}, "commerce": {"orders 1", "outbox 0 waiting"}}),
			}},
			{"The ledger reads its consumer group and books one balanced Journal Entry, keyed by the event's unique event_id.", []flowStep{
				step("stream", "ledger", "XREADGROUP", colPurple, nil),
				step("ledger", "ledgerdb", "Journal Entry, 1 tx", colCyan, map[string][]string{"ledgerdb": {"1 Journal Entry", "Σ Postings = 0"}}),
			}},
			{"XACK only after the commit. A crash before it means a redelivery, and the unique event_id makes that a no-op.", []flowStep{
				step("ledger", "stream", "XACK", colGreen, map[string][]string{"stream": {"1 OrderCompleted", "acknowledged"}}),
			}},
			{"Balances are summed from Postings. The browser reads them through api and never calls the ledger itself.", []flowStep{
				step("browser", "api", "GET balance", colBlue, nil),
				step("api", "ledger", "internal REST", colPurple, nil),
				step("ledger", "ledgerdb", "SUM(postings)", colCyan, nil),
				step("ledger", "api", "12255 · 645", colGreen, nil),
				step("api", "browser", "balances", colGreen, map[string][]string{"browser": {"Store  $122.55", "Fee      $6.45"}}),
			}},
		},
		outro: "Store $122.55 + Platform Fee $6.45 = the $129.00 the Shopper paid. Every cent is in a Journal Entry.",
	}
}

// atLeastOnce shows duplicates and crashes on the async half still booking each Order once.
func atLeastOnce() *flowScene {
	const y, low = 104.0, 290.0
	col := func(i int) float64 { return 30 + float64(i)*240 }
	commerce := &flowNode{id: "commerce", title: "outbox", sub: "commerce Postgres", x: col(0), y: y, w: 180, h: 92, accent: colCyan,
		lines: []string{"A waiting"}}
	relay := &flowNode{id: "relay", title: "relay", sub: "worker", x: col(1), y: y, w: 180, h: 92, accent: colPurple,
		lines: []string{"running"}}
	stream := &flowNode{id: "stream", title: "orders.completed", sub: "Redis Stream", x: col(2), y: y, w: 180, h: 92, accent: colRed,
		lines: []string{"(empty)"}}
	ledger := &flowNode{id: "ledger", title: "ledger", sub: "consumer group", x: col(3), y: y, w: 180, h: 92, accent: colPurple,
		lines: []string{"running"}}
	db := &flowNode{id: "db", title: "ledger DB", sub: "unique event_id", x: col(3), y: low, w: 180, h: 92, accent: colCyan,
		lines: []string{"Journal Entries 0"}}

	step := func(from, to, label string, c rgb, set map[string][]string) flowStep {
		return flowStep{from: from, to: to, label: label, col: c, set: set}
	}
	crash := func(id string, set map[string][]string) flowStep {
		return flowStep{from: id, crash: true, set: set}
	}
	tally := &flowNode{id: "tally", title: "Tally", sub: "what happened vs. what was booked", x: col(0), y: low, w: 420, h: 92, accent: colGreen,
		lines: []string{"deliveries 0 · crashes 0", "Journal Entries 0"}}
	count := func(deliveries, crashes, entries int) []string {
		return []string{fmt.Sprintf("deliveries %d · crashes %d", deliveries, crashes), fmt.Sprintf("Journal Entries %d", entries)}
	}
	return &flowScene{
		title:   "At-least-once delivery, exactly-once books",
		nodes:   []*flowNode{commerce, relay, stream, ledger, db, tally},
		edges:   [][2]string{{"commerce", "relay"}, {"relay", "stream"}, {"stream", "ledger"}, {"ledger", "db"}},
		stepDur: 0.8,
		beats: []flowBeat{
			{"The relay claims outbox row A and publishes it to the stream…", []flowStep{
				step("commerce", "relay", "claim A", colPurple, nil),
				step("relay", "stream", "XADD A", colPurple, map[string][]string{"stream": {"A"}}),
			}},
			{"…then crashes before its transaction marks A published, so the claim rolls back.", []flowStep{
				crash("relay", map[string][]string{"relay": {"restarted"}, "tally": count(0, 1, 0)}),
			}},
			{"A is still waiting, so the restarted relay publishes it again. The stream now holds a duplicate.", []flowStep{
				step("commerce", "relay", "claim A", colPurple, nil),
				step("relay", "stream", "XADD A", colPurple, map[string][]string{"stream": {"A", "A (duplicate)"}}),
				step("relay", "commerce", "mark published", colGreen, map[string][]string{"commerce": {"A published"}}),
			}},
			{"The ledger books the first copy: one Journal Entry for event_id A, then XACK.", []flowStep{
				step("stream", "ledger", "A", colPurple, map[string][]string{"tally": count(1, 1, 0)}),
				step("ledger", "db", "INSERT JE for A", colCyan, map[string][]string{"db": {"Journal Entries 1"}, "tally": count(1, 1, 1)}),
				step("ledger", "stream", "XACK A", colGreen, map[string][]string{"stream": {"A acked", "A (duplicate)"}}),
			}},
			{"The duplicate hits the unique event_id: ON CONFLICT DO NOTHING writes nothing, and it is acknowledged.", []flowStep{
				step("stream", "ledger", "A again", colPurple, map[string][]string{"tally": count(2, 1, 1)}),
				step("ledger", "db", "event_id A exists", colAmber, nil),
				step("ledger", "stream", "XACK A", colGreen, map[string][]string{"stream": {"A acked", "A (dup) acked"}}),
			}},
			{"Order B: the ledger commits its Journal Entry, then crashes before XACK. B stays pending.", []flowStep{
				step("relay", "stream", "XADD B", colPurple, map[string][]string{"stream": {"A acked ×2", "B"}, "commerce": {"A, B published"}}),
				step("stream", "ledger", "B", colPurple, map[string][]string{"tally": count(3, 1, 1)}),
				step("ledger", "db", "INSERT JE for B", colCyan, map[string][]string{"db": {"Journal Entries 2"}, "tally": count(3, 1, 2)}),
				crash("ledger", map[string][]string{"ledger": {"restarted"}, "stream": {"A acked ×2", "B pending"}, "tally": count(3, 2, 2)}),
			}},
			{"The restarted ledger reclaims B with XAUTOCLAIM. It is already booked, so nothing new is written; then XACK.", []flowStep{
				step("stream", "ledger", "XAUTOCLAIM B", colPurple, map[string][]string{"tally": count(4, 2, 2)}),
				step("ledger", "db", "event_id B exists", colAmber, nil),
				step("ledger", "stream", "XACK B", colGreen, map[string][]string{"stream": {"A acked ×2", "B acked"}}),
			}},
		},
		outro: "2 Orders, 4 deliveries, 2 crashes → exactly 2 Journal Entries. Duplicates are expected; the unique event_id absorbs them.",
	}
}

// journalEntry shows one Order's double-entry split and the database refusing an unbalanced
// Journal Entry (ADR-0004).
type journalEntry struct{}

func (journalEntry) duration() float64 { return 17 }

func (journalEntry) draw(c *canvas, t float64) {
	c.clear(colBG)
	c.text("Double-entry, append-only, in integer cents", 30, 38, face(bold, 22), colText, 1, left)

	// The event, on the left.
	ev := phase(t, 0.2, 0.6) * (1 - 0.65*phase(t, 8.4, 0.5)) // dims once the example is no longer this event
	c.box(30, 70, 300, 222, 12, colPanel, colBorder, 1.5)
	c.text("OrderCompleted", 50, 100, face(bold, 16), colPurple, ev, left)
	c.text("from orders.completed", 50, 118, face(regular, 12), colMuted, ev, left)
	fields := [][2]string{{"event_id", "0192f3…"}, {"store_id", "Summit Supply"}, {"amount_cents", "12900"}, {"currency", "USD"}, {"fee_bps", "500"}}
	for i, f := range fields {
		y := 148 + float64(i)*26
		c.text(f[0], 50, y, face(mono, 13), colMuted, ev, left)
		c.text(f[1], 310, y, face(monoBold, 13), colText, ev, right)
	}

	// The Journal Entry, on the right.
	const tx, tw = 360.0, 570.0
	c.box(tx, 70, tw, 222, 12, colPanel, colBorder, 1.5)
	c.text("Journal Entry", tx+20, 100, face(bold, 16), colText, 1, left)
	c.text("Ledger Account", tx+20, 128, face(monoBold, 12), colMuted, 1, left)
	c.text("Debit", tx+420, 128, face(monoBold, 12), colMuted, 1, right)
	c.text("Credit", tx+tw-20, 128, face(monoBold, 12), colMuted, 1, right)
	c.line(tx+20, 138, tx+tw-20, 138, 1, colBorder, 1)

	type row struct {
		account string
		amount  int
		at      float64
	}
	balanced := []row{{"processor_receivable", 12900, 1.4}, {"store_payable:summit", -12255, 2.6}, {"platform_fee_revenue", -645, 3.8}}
	unbalanced := []row{{"processor_receivable", 10000, 9.6}, {"store_payable:summit", -9999, 10.6}}

	rows, fade := balanced, 1-phase(t, 8.4, 0.5)
	if t >= 8.9 {
		rows, fade = unbalanced, 1.0
	}
	sum := 0
	for i, r := range rows {
		a := phase(t, r.at, 0.4) * fade
		if a <= 0 {
			continue
		}
		sum += r.amount
		y := 164 + float64(i)*30 + (1-ease(phase(t, r.at, 0.4)))*10
		c.text(r.account, tx+20, y, face(mono, 14), colText, a, left)
		if r.amount > 0 {
			c.text(cents(r.amount), tx+420, y, face(monoBold, 14), colGreen, a, right)
		} else {
			c.text(cents(-r.amount), tx+tw-20, y, face(monoBold, 14), colAmber, a, right)
		}
	}
	c.line(tx+20, 254, tx+tw-20, 254, 1, colBorder, 1)
	sumAlpha := phase(t, 1.4, 0.4) * fade
	if t >= 8.9 {
		sumAlpha = phase(t, 9.6, 0.4)
	}
	sumCol := colAmber
	if sum == 0 {
		sumCol = colGreen
	}
	c.text("Σ Postings", tx+20, 278, face(monoBold, 14), colMuted, sumAlpha, left)
	sumText := "0"
	if sum != 0 {
		sumText = fmt.Sprintf("%+d", sum)
	}
	c.text(sumText, tx+tw-20, 278, face(monoBold, 16), sumCol, sumAlpha, right)

	// Verdicts.
	switch {
	case t >= 11.4:
		a := phase(t, 11.4, 0.4)
		c.box(tx+tw/2-150, 300-4, 300, 34, 17, mix(colBG, colRed, 0.2), colRed, 1.5)
		c.text("ROLLED BACK · nothing kept", tx+tw/2, 318, face(monoBold, 14), colRed, a, center)
		// Strike through the refused rows.
		for i := range rows {
			y := 160 + float64(i)*30
			c.line(tx+20, y, tx+20+(tw-40)*ease(phase(t, 11.4, 0.6)), y, 2, colRed, 0.9)
		}
	case t >= 4.6 && t < 8.6:
		a := phase(t, 4.6, 0.4) * fade
		c.box(tx+tw/2-150, 300-4, 300, 34, 17, mix(colBG, colGreen, 0.2), colGreen, 1.5)
		c.text("COMMITTED · sums to 0", tx+tw/2, 318, face(monoBold, 14), colGreen, a, center)
	}

	// The rule under way, in the caption.
	var number, caption string
	switch {
	case t < 1.4:
		number, caption = "1", "The ledger consumes an OrderCompleted. The Fee rate (fee_bps) travels in the event, captured when the Order was made."
	case t < 4.6:
		number, caption = "2", "Fee = 12900 × 500 / 10000 = 645, rounded half up to a whole cent. The Store gets the remainder: 12255."
	case t < 8.6:
		number, caption = "3", "Debits are positive and credits negative, so a balanced Journal Entry's Postings sum to exactly 0."
	case t < 11.4:
		number, caption = "4", "Now try a Journal Entry that is off by one cent…"
	case t < 14.2:
		number, caption = "5", "Postgres refuses it at COMMIT: a deferred constraint trigger checks every Journal Entry sums to 0. Go never gets a say."
	default:
		number, caption = "6", "And no UPDATE or DELETE, ever: triggers refuse both. Balances are summed from Postings, never stored."
	}
	drawCaption(c, number, caption)
	if t >= 14.2 {
		a := phase(t, 14.2, 0.4)
		for i, s := range []string{"UPDATE postings …  → refused", "DELETE FROM postings …  → refused"} {
			c.text(s, 30+float64(i)*450, 372, face(monoBold, 14), colRed, a, left)
		}
	} else if t >= 11.4 {
		c.text("ERROR: journal entry 0192f4… postings sum to 1, not 0 (SQLSTATE 23514)", 30, 372, face(mono, 14), colRed, phase(t, 11.4, 0.4), left)
	}
}

// cents formats 12255 as "12,255".
func cents(v int) string {
	s := fmt.Sprint(v)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// reserveRace shows many Shoppers racing for a few units: Redis runs one script at a time, so
// exactly as many Holds succeed as there are units (TestRacingShoppersGetExactlyAsManyHoldsAsUnits).
type reserveRace struct{}

const (
	raceShoppers = 8
	raceUnits    = 3
	raceArrive   = 1.6  // when the last reserve reaches Redis
	raceEach     = 0.75 // time the script takes per Shopper, slowed down to watch
)

// raceOrder is the order the requests happen to reach Redis: not the Shoppers' order.
var raceOrder = []int{4, 1, 6, 0, 7, 2, 5, 3}

func (reserveRace) duration() float64 { return raceArrive + raceShoppers*raceEach + 1.4 }

func (reserveRace) draw(c *canvas, t float64) {
	c.clear(colBG)
	c.text("8 Shoppers race for 3 units", 30, 38, face(bold, 22), colText, 1, left)

	rowY := func(i int) float64 { return 86 + float64(i)*42 }
	const shopX, queueX, boxX, boxW, resultX = 80.0, 380.0, 470.0, 220.0, 790.0
	boxY, boxH := 150.0, 160.0
	boxCY := boxY + boxH/2
	spotX := boxX + 52 // where the script handles a request

	// When each Shopper's request is handled, and how many units remain.
	turn := make([]int, raceShoppers)
	for k, s := range raceOrder {
		turn[s] = k
	}
	handled := 0
	for k := range raceShoppers {
		if t >= raceArrive+float64(k+1)*raceEach {
			handled = k + 1
		}
	}
	available := raceUnits - min(handled, raceUnits)

	// Redis.
	running := t >= raceArrive && handled < raceShoppers
	border := colBorder
	if running {
		border = colRed
	}
	c.box(boxX, boxY, boxW, boxH, 12, colPanel, border, 2)
	c.text("Redis", boxX+18, boxY+28, face(bold, 16), colText, 1, left)
	c.text("reserve.lua · one at a time", boxX+18, boxY+46, face(regular, 12), colMuted, 1, left)
	counterX := boxX + boxW - 62
	c.text(fmt.Sprint(available), counterX, boxY+118, face(bold, 44), map[bool]rgb{true: colGreen, false: colRed}[available > 0], 1, center)
	c.text("available", counterX, boxY+140, face(mono, 12), colMuted, 1, center)

	c.text("SHOPPERS", shopX-30, 70, face(monoBold, 11), colMuted, 1, left)
	c.text("RESULT", resultX-10, 70, face(monoBold, 11), colMuted, 1, left)

	for s := range raceShoppers {
		y := rowY(s)
		c.circle(shopX-14, y, 13, colPanel2, 1)
		c.text(fmt.Sprintf("S%d", s+1), shopX-14, y+5, face(bold, 13), colText, 1, center)

		k := turn[s]
		won := k < raceUnits
		resCol := colRed
		if won {
			resCol = colGreen
		}
		depart := 0.3 + float64(s)*0.05
		arrive := 0.9 + float64(k)*0.09
		start := raceArrive + float64(k)*raceEach
		done := start + raceEach

		// Waiting in line: the k-th to arrive queues k places back.
		qx := func(pos int) float64 { return queueX - float64(pos)*30 }
		qy := boxCY
		var px, py float64
		switch {
		case t < depart:
			continue
		case t < arrive:
			p := ease(phase(t, depart, arrive-depart))
			px, py = shopX+(qx(k)-shopX)*p, y+(qy-y)*p
		case t < start:
			// Move up the line as those ahead are handled.
			ahead := k - min(handled, k)
			if t >= raceArrive {
				ahead = k - int(math.Min(float64(k), math.Floor((t-raceArrive)/raceEach)))
			}
			px, py = qx(ahead), qy
		case t < done:
			p := ease(phase(t, start, raceEach*0.4))
			px, py = qx(0)+(spotX-qx(0))*p, qy+20*p
		default:
			p := ease(phase(t, done, 0.5))
			px, py = spotX+(resultX-spotX)*p, qy+20+(y-qy-20)*p
			if p >= 1 {
				label := "SOLD_OUT"
				if won {
					label = "HELD"
				}
				c.pill(label, resultX+40, y, face(monoBold, 12), resCol, colBG, 1)
				continue
			}
		}
		dot := colBlue
		if t >= done {
			dot = resCol
		}
		c.circle(px, py, 9, dot, 1)
	}

	caption, number := "Every Shopper hits reserve at once. The requests reach Redis in whatever order the network delivers them.", "1"
	switch {
	case t >= raceArrive+float64(raceShoppers)*raceEach:
		caption, number = "Exactly 3 Holds for 3 units, never 4. Proven with many goroutines against real Redis in the inventory tests.", "3"
	case t >= raceArrive:
		caption, number = "Redis runs one Lua script at a time, so checking stock and taking a unit is a single step that can't interleave.", "2"
	}
	drawCaption(c, number, caption)
}
