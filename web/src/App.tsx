import { useEffect, useState } from "react";
import { api, type FlashSale } from "./api/client";

const priceFormat = new Intl.NumberFormat("en-US", { style: "currency", currency: "USD" });
const timeFormat = new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short" });

type State =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "ready"; flashSales: FlashSale[] };

export function App() {
  const [state, setState] = useState<State>({ status: "loading" });

  useEffect(() => {
    api.GET("/flash-sales").then(({ data, error }) => {
      if (data) setState({ status: "ready", flashSales: data.flashSales });
      else setState({ status: "error", message: String(error ?? "request failed") });
    }, (err: unknown) => setState({ status: "error", message: String(err) }));
  }, []);

  return (
    <main>
      <h1>Flash Sales</h1>
      {state.status === "loading" && <p>Loading…</p>}
      {state.status === "error" && <p role="alert">Couldn't load Flash Sales: {state.message}</p>}
      {state.status === "ready" && state.flashSales.length === 0 && <p>No Flash Sales scheduled.</p>}
      {state.status === "ready" && state.flashSales.length > 0 && (
        <ul className="flash-sales">
          {state.flashSales.map((flashSale) => (
            <li key={flashSale.id} className="flash-sale">
              <p className="store">{flashSale.store.name}</p>
              <h2>{flashSale.productName}</h2>
              <p className="price">
                {priceFormat.format(flashSale.priceCents / 100)} · {flashSale.units} units
              </p>
              <p className="schedule">
                <time dateTime={flashSale.startsAt}>{timeFormat.format(new Date(flashSale.startsAt))}</time> –{" "}
                <time dateTime={flashSale.endsAt}>{timeFormat.format(new Date(flashSale.endsAt))}</time>
              </p>
            </li>
          ))}
        </ul>
      )}
    </main>
  );
}
