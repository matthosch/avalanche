import { api } from "./api/client";

/** Where the browser keeps the signed-in Shopper's ID, so a reload stays signed in. */
const storageKey = "avalanche.shopperId";

/** The signed-in Shopper's ID, or null when nobody is signed in. */
export function storedShopperId(): string | null {
  try {
    return localStorage.getItem(storageKey);
  } catch {
    return null;
  }
}

/** Stub sign-in: asks `api` for a new Shopper ID and remembers it. */
export async function signIn(): Promise<string> {
  const { data, error } = await api.POST("/sign-in");
  if (!data) throw new Error(String(error ?? "sign-in failed"));
  try {
    localStorage.setItem(storageKey, data.shopperId);
  } catch {
    // Without storage the Shopper stays signed in until the page reloads.
  }
  return data.shopperId;
}

/** Forgets the Shopper ID; the next sign-in issues a new one. */
export function signOut(): void {
  try {
    localStorage.removeItem(storageKey);
  } catch {
    // Nothing stored, nothing to forget.
  }
}
