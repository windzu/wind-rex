// Where the page was, kept in this browser (docs/WEB-003-multihost.md,
// Persistence): the shown window, by its host (gorex.place), and the
// hosts the user disconnected (gorex.hostsOff), which stay so. A page from
// before kept only the chosen host (gorex.host); it moves into gorex.place once.
// Every read and write goes on without storage: the page then forgets
// when it closes. No DOM here: web/test/place.test.ts runs it in Node.

/** The shown window, by its host; a host alone shows its first window. */
export interface Place {
  host: string;
  session?: string;
  window?: string;
}

/** What the keys are kept in: localStorage, or a test's. */
export interface Store {
  getItem(key: string): string | null;
  setItem(key: string, value: string): void;
  removeItem(key: string): void;
}

export const placeKey = "gorex.place";
export const offKey = "gorex.hostsOff";
/** The chosen host's ID, as a page from before kept it. */
export const oldHostKey = "gorex.host";

/** A place as kept, or undefined when it is none. */
export function parsePlace(s: string | null): Place | undefined {
  try {
    const p = JSON.parse(s ?? "null");
    if (typeof p?.host !== "string" || !p.host) return undefined;
    const out: Place = { host: p.host };
    if (typeof p.session === "string" && typeof p.window === "string") {
      out.session = p.session;
      out.window = p.window;
    }
    return out;
  } catch {
    return undefined;
  }
}

/**
 * What the keys hold once gorex.host has moved into gorex.place. A place
 * kept already wins. Else the old host is the place only while the host
 * list (hosts, its IDs) still has it.
 */
export function migrate(old: { host: string | null; place: string | null }, hosts: string[]): { place?: Place } {
  let place = parsePlace(old.place);
  const id = old.host ?? "";
  if (!place && id && hosts.includes(id)) place = { host: id };
  return { place };
}

function storage(): Store | undefined {
  try {
    return localStorage;
  } catch {
    return undefined;
  }
}

/**
 * The place kept, after moving gorex.host into it if a page from before
 * left one; as the page starts. hosts: the host list's IDs.
 */
export function startPlace(hosts: string[], store = storage()): Place | undefined {
  let r: ReturnType<typeof migrate>;
  try {
    if (!store) return undefined;
    const host = store.getItem(oldHostKey);
    if (host === null) return parsePlace(store.getItem(placeKey));
    r = migrate({ host, place: store.getItem(placeKey) }, hosts);
  } catch {
    return undefined;
  }
  try {
    if (r.place) store.setItem(placeKey, JSON.stringify(r.place));
    else store.removeItem(placeKey);
    store.removeItem(oldHostKey);
  } catch {
    // Storage that reads but does not write: this load has the place, the next reads gorex.host again.
  }
  return r.place;
}

/** Keeps the shown window's place. */
export function savePlace(p: Place, store = storage()) {
  try {
    store?.setItem(placeKey, JSON.stringify(p));
  } catch {
    // Without storage the place lasts until the page closes.
  }
}

/** The hosts the user disconnected, by ID: they do not connect as the page starts. */
export function loadHostsOff(store = storage()): Set<string> {
  try {
    const l = JSON.parse(store?.getItem(offKey) ?? "[]");
    return new Set(Array.isArray(l) ? l.filter((x): x is string => typeof x === "string" && !!x) : []);
  } catch {
    return new Set();
  }
}

/** Keeps the disconnected hosts; none removes the key. */
export function saveHostsOff(ids: Iterable<string>, store = storage()) {
  const all = [...ids];
  try {
    if (all.length) store?.setItem(offKey, JSON.stringify(all));
    else store?.removeItem(offKey);
  } catch {
    // Without storage they stay disconnected until the page closes.
  }
}
