import createClient from "openapi-fetch";
import type { components, paths } from "./schema.gen";

/** Typed client for `api`, generated from api/openapi.yaml. */
export const api = createClient<paths>({ baseUrl: "/api" });

export type FlashSale = components["schemas"]["FlashSale"];
export type Hold = components["schemas"]["Hold"];
export type ReserveRefusalReason = components["schemas"]["ReserveRefusal"]["reason"];
