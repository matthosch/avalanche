import { useEffect, useState } from "react";
import { api, type FlashSale, type Hold, type Order, type ReserveRefusalReason } from "./api/client";
import { signIn, signOut, storedShopperId } from "./shopper";

const priceFormat = new Intl.NumberFormat("en-US", { style: "currency", currency: "USD" });
const timeFormat = new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short" });
const holdTimeFormat = new Intl.DateTimeFormat(undefined, { timeStyle: "medium" });

/** Flash Sales go LIVE and ENDED on their own, so the listing refreshes on this interval. */
const refreshMs = 3000;

type State =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "ready"; flashSales: FlashSale[] };

/** Where the Shopper's attempt to get a Hold on a Flash Sale, and then to pay for it, stands. */
type HoldAttempt =
  | { status: "reserving" }
  | { status: "held"; hold: Hold; paying?: boolean; payError?: string }
  | { status: "ordered"; hold: Hold; order: Order }
  | { status: "refused"; message: string };

const refusalMessages: Record<ReserveRefusalReason, string> = {
  SOLD_OUT: "Sold out. Units may come back if other Holds are released.",
  ALREADY_HOLDING: "You already have a Hold on this Flash Sale.",
  NOT_LIVE: "This Flash Sale isn't live.",
};

export function App() {
  const [state, setState] = useState<State>({ status: "loading" });
  const [shopperId, setShopperId] = useState<string | null>(storedShopperId);
  const [holdAttempts, setHoldAttempts] = useState<Record<string, HoldAttempt>>({});

  /** Reserves a unit, or on an ENDED Flash Sale (where no new Hold can be made) finds the
   * Shopper's existing Hold: either way the API hands back the Hold they already have. */
  const reserve = async (shopper: string, flashSaleId: string, ended: boolean) => {
    const setAttempt = (attempt: HoldAttempt) => setHoldAttempts((a) => ({ ...a, [flashSaleId]: attempt }));
    setAttempt({ status: "reserving" });
    try {
      const { data, error } = await api.POST("/flash-sales/{flashSaleId}/holds", {
        params: { path: { flashSaleId }, header: { "X-Shopper-Id": shopper } },
      });
      // ALREADY_HOLDING carries the Shopper's existing Hold: after a reload or a lost reply,
      // reserving again is how they get it back.
      const hold = data ?? error?.hold;
      if (hold) setAttempt({ status: "held", hold });
      // Only a 409 has a typed body; anything else (a 500's plain text) has no reason.
      else if (ended && error?.reason === "NOT_LIVE") setAttempt({ status: "refused", message: "You have no Hold on this Flash Sale." });
      else setAttempt({ status: "refused", message: (error && refusalMessages[error.reason]) || "Reserve failed." });
    } catch (err: unknown) {
      setAttempt({ status: "refused", message: `Reserve failed: ${String(err)}` });
    }
  };

  /** Pays for a HELD Hold with the test card; the payment succeeding makes it an Order. */
  const pay = async (shopper: string, hold: Hold) => {
    const setAttempt = (attempt: HoldAttempt) => setHoldAttempts((a) => ({ ...a, [hold.flashSaleId]: attempt }));
    setAttempt({ status: "held", hold, paying: true });
    try {
      const { data, error, response } = await api.POST("/flash-sales/{flashSaleId}/holds/{holdId}/pay", {
        params: { path: { flashSaleId: hold.flashSaleId, holdId: hold.id }, header: { "X-Shopper-Id": shopper } },
        body: { paymentMethod: "pm_card_visa" },
      });
      if (data) setAttempt({ status: "ordered", hold: { ...hold, state: "SOLD" }, order: data });
      // A 409 says where the Hold is now, such as SOLD after a payment whose reply was lost.
      else if (error) setAttempt({ status: "held", hold: { ...hold, state: error.state } });
      else setAttempt({ status: "held", hold, payError: `Payment failed (${response.status}).` });
    } catch (err: unknown) {
      setAttempt({ status: "held", hold, payError: `Payment failed: ${String(err)}` });
    }
  };

  useEffect(() => {
    const load = () =>
      api.GET("/flash-sales").then(({ data, error }) => {
        if (data) setState({ status: "ready", flashSales: data.flashSales });
        else setState({ status: "error", message: String(error ?? "request failed") });
      }, (err: unknown) => setState({ status: "error", message: String(err) }));
    void load();
    const timer = setInterval(load, refreshMs);
    return () => clearInterval(timer);
  }, []);

  return (
    <main>
      <header className="top">
        <h1>Flash Sales</h1>
        {shopperId ? (
          <p className="shopper">
            Shopper <code>{shopperId.slice(-8)}</code>{" "}
            <button
              type="button"
              onClick={() => {
                signOut();
                setShopperId(null);
                setHoldAttempts({});
              }}
            >
              Sign out
            </button>
          </p>
        ) : (
          <button type="button" onClick={() => signIn().then(setShopperId, (err: unknown) => alert(String(err)))}>
            Sign in
          </button>
        )}
      </header>
      {state.status === "loading" && <p>Loading…</p>}
      {state.status === "error" && <p role="alert">Couldn't load Flash Sales: {state.message}</p>}
      {state.status === "ready" && state.flashSales.length === 0 && <p>No Flash Sales scheduled.</p>}
      {state.status === "ready" && state.flashSales.length > 0 && (
        <ul className="flash-sales">
          {state.flashSales.map((flashSale) => (
            <li key={flashSale.id} className="flash-sale">
              <p className="store">
                {flashSale.store.name}
                <span className={`status status-${flashSale.status.toLowerCase()}`}>{flashSale.status}</span>
              </p>
              <h2>{flashSale.productName}</h2>
              <p className="price">
                {priceFormat.format(flashSale.priceCents / 100)} ·{" "}
                {flashSale.unitsRemaining === undefined
                  ? `${flashSale.units} units`
                  : `${flashSale.unitsRemaining} of ${flashSale.units} units left`}
              </p>
              <p className="schedule">
                <time dateTime={flashSale.startsAt}>{timeFormat.format(new Date(flashSale.startsAt))}</time> –{" "}
                <time dateTime={flashSale.endsAt}>{timeFormat.format(new Date(flashSale.endsAt))}</time>
              </p>
              {/* A Hold made before the end may still be paid until it expires, so an ENDED
                  Flash Sale lets the Shopper find theirs, and a Hold outlives its LIVE sale. */}
              {shopperId && (flashSale.status === "LIVE" || flashSale.status === "ENDED") && (
                <ReserveControl
                  ended={flashSale.status === "ENDED"}
                  attempt={holdAttempts[flashSale.id]}
                  onReserve={() => void reserve(shopperId, flashSale.id, flashSale.status === "ENDED")}
                  onPay={(hold) => void pay(shopperId, hold)}
                />
              )}
            </li>
          ))}
        </ul>
      )}
    </main>
  );
}

/** The Reserve button for a LIVE Flash Sale (Show my Hold once it has ENDED), replaced by the
 * Shopper's Hold once they have one, with a Pay button while it is HELD, and finally by their
 * Order confirmation. */
function ReserveControl({
  ended,
  attempt,
  onReserve,
  onPay,
}: {
  ended: boolean;
  attempt: HoldAttempt | undefined;
  onReserve: () => void;
  onPay: (hold: Hold) => void;
}) {
  if (attempt?.status === "ordered") {
    const { order } = attempt;
    return (
      <div className="order" role="status">
        <p>
          <span className="status status-live">SOLD</span> Order confirmed
        </p>
        <p className="order-detail">
          {priceFormat.format(order.amountCents / 100)} paid · Order <code>{order.id.slice(-8)}</code>
        </p>
      </div>
    );
  }
  if (attempt?.status === "held") {
    const { hold } = attempt;
    if (hold.state === "SOLD") {
      return (
        <p className="hold" role="status">
          <span className="status status-live">SOLD</span> You bought this.
        </p>
      );
    }
    return (
      <div className="hold-pay">
        <p className="hold" role="status">
          <span className="status status-live">{hold.state}</span> Hold until{" "}
          <time dateTime={hold.expiresAt}>{holdTimeFormat.format(new Date(hold.expiresAt))}</time>
        </p>
        {hold.state === "HELD" && (
          <button type="button" onClick={() => onPay(hold)} disabled={attempt.paying}>
            {attempt.paying ? "Paying…" : "Pay with test card"}
          </button>
        )}
        {attempt.payError && <p role="alert">{attempt.payError}</p>}
      </div>
    );
  }
  return (
    <div className="reserve">
      <button type="button" onClick={onReserve} disabled={attempt?.status === "reserving"}>
        {attempt?.status === "reserving" ? (ended ? "Looking…" : "Reserving…") : ended ? "Show my Hold" : "Reserve"}
      </button>
      {attempt?.status === "refused" && <p role="alert">{attempt.message}</p>}
    </div>
  );
}
