import { diagnoseCarrier } from "./carrier-diagnostic.ts";
import { contactAPI } from "./contact-upload.ts";
import { fieldState, messageRef, type Diagnostic } from "./diagnostics.ts";
import { mediaList } from "./media.ts";
import { captureContacts, type Contact, type ContactError } from "./contacts.ts";

// The house's edge inbox.
//
// The carrier callback and the authenticated box routes:
//   GET  /h/:house/sms/:callbackToken?from=&to=&message=&id=&media=&timestamp=   VoIP.ms calls this
//   GET  /h/:house/inbox/pull?wait=20      Bearer inbox token: long-poll for texts
//   POST /h/:house/inbox/ack   {ids}       Bearer inbox token: handled, do not repeat
//   POST /h/:house/inbox/send  {to,message} Bearer inbox token: reply from the house number
//
// VoIP.ms cannot sign callbacks, so the callback token is a path segment:
// the URL is the credential and is never logged. The inbox token rides a
// header. The carrier API key is a secret of this Worker and never leaves it.

export interface Env {
  DB: D1Database;
  VOIPMS_API_USERNAME: string;
  VOIPMS_API_PASSWORD: string;
  [key: string]: unknown;
}

const HOUSE = /^[a-z0-9][a-z0-9_-]*$/;

function houseSecret(env: Env, house: string, name: string): string | undefined {
  const v = env[`HOUSE_${house.toUpperCase().replace(/-/g, "_")}_${name}`];
  return typeof v === "string" && v !== "" ? v : undefined;
}

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { "content-type": "application/json" } });
}

function constantTimeEqual(a: string, b: string): boolean {
  if (a.length !== b.length) return false;
  let out = 0;
  for (let i = 0; i < a.length; i++) out |= a.charCodeAt(i) ^ b.charCodeAt(i);
  return out === 0;
}

function bearer(req: Request): string {
  const h = req.headers.get("authorization") ?? "";
  return h.startsWith("Bearer ") ? h.slice(7) : "";
}

// The reply rule, enforced here as well as on the box: plain ASCII and one
// segment — the two transport facts. A non-ASCII character (an emoji, a
// curly quote) switches the carrier's encoding and halves the segment. The
// clauses this once had for exclamation marks, links, digits and phone
// numbers were carrier folklore and came out on 2026-09-26: the house sends
// links (s19), and a human sending a number knows what they are sending.
function replyProblem(s: string): string | null {
  if (s.trim() === "") return "is empty";
  if (s.length > 160) return `is ${s.length} characters; one segment is 160`;
  for (const ch of s) {
    const c = ch.charCodeAt(0);
    if (c > 126 || (c < 32 && ch !== "\n")) return "has a non-ASCII character";
  }
  return null;
}

async function sendSMS(env: Env, did: string, dst: string, message: string): Promise<{ ok: boolean; detail: string }> {
  const u = new URL("https://voip.ms/api/v1/rest.php");
  u.searchParams.set("api_username", env.VOIPMS_API_USERNAME);
  u.searchParams.set("api_password", env.VOIPMS_API_PASSWORD);
  u.searchParams.set("method", "sendSMS");
  u.searchParams.set("did", did);
  u.searchParams.set("dst", dst);
  u.searchParams.set("message", message);
  // Two tries: the first live send met a transient 525 between Cloudflare
  // and the carrier, and the second went through. A reply is a courtesy;
  // one retry is proportionate, more would be a spammer's loop.
  let status = "";
  for (let attempt = 0; attempt < 2; attempt++) {
    const resp = await fetch(u.toString(), { method: "GET" });
    const text = await resp.text();
    try { status = (JSON.parse(text) as { status?: string }).status ?? ""; } catch { status = text.trim().slice(0, 40); }
    // `success` means accepted, not delivered. There are no receipts.
    if (status === "success") return { ok: true, detail: status };
    if (resp.status < 500 && !status.startsWith("error code")) break; // the carrier answered; retrying will not change it
    await new Promise((res) => setTimeout(res, 1500));
  }
  return { ok: false, detail: status };
}

async function handle(req: Request, env: Env, diagnostic: Diagnostic): Promise<Response> {
  const url = new URL(req.url);
  if (url.pathname === "/api/v1/phonebooks" || url.pathname === "/api/v1/contact-imports" || url.pathname.startsWith("/api/v1/contact-imports/")) return contactAPI(req, env, diagnostic);
  const m = url.pathname.match(/^\/h\/([^/]+)\/(sms|inbox)\/([^/]+)$/);
  if (!m) return new Response("not found", { status: 404 });
  const [, house, area, tail] = m;
  if (!HOUSE.test(house)) return new Response("not found", { status: 404 });

  diagnostic.route = area === "sms" ? "callback" :
    tail === "pull" ? "inbox.pull" : tail === "ack" ? "inbox.ack" :
    tail === "send" ? "inbox.send" : tail === "whoami" ? "inbox.whoami" : tail === "diagnose" ? "inbox.diagnose" : "other";
  diagnostic.stage = "authorization";
  // The carrier's callback: the token is the path.
  if (area === "sms" && req.method === "GET") {
    const want = houseSecret(env, house, "CALLBACK_TOKEN");
    if (!want || !constantTimeEqual(tail, want)) return new Response("not found", { status: 404 });
    diagnostic.stage = "callback-validation";
    const q = url.searchParams;
    diagnostic.media_field = fieldState(q.get("media"), "{MEDIA}");
    const id = q.get("id") ?? "";
    const from = q.get("from") ?? "";
    const to = q.get("to") ?? "";
    const body = q.get("message") ?? "";
    // The carrier does not specify the timestamp's format or timezone.
    // Retain bounded printable ASCII verbatim; never let it change queue order
    // or prevent message delivery when missing, unsafe or left unexpanded. The
    // stored value and the logged state come from one classification, so the
    // log never says "present" for a value that was dropped.
    const timestamp = q.get("timestamp");
    const timestampState = fieldState(timestamp, "{TIMESTAMP}");
    const providerTimestamp = timestampState === "present" && /^[\x20-\x7e]{1,64}$/.test(timestamp!.trim()) ? timestamp!.trim() : "";
    diagnostic.timestamp_field = timestampState === "present" && !providerTimestamp ? "dropped" : timestampState;
    // A media list the Worker cannot use (a host that is not the carrier's,
    // too many entries) must not cost the sender their text: the message is
    // queued with a fixed error and no fetch, and the house tells them no
    // attachment arrived. The placeholder left unexpanded is the carrier's
    // way of saying "none", exactly as for the timestamp.
    let media: string[] = [];
    let mediaProblem = false;
    if (diagnostic.media_field === "present") {
      try { media = mediaList(q.get("media")); }
      catch { mediaProblem = true; }
    }
    diagnostic.media_count = media.length;
    // Together with ten-message pulls and bounded contact fields this
    // keeps every queue response below the house client's 1 MiB limit.
    if (!id || id.length > 128 || !from || from.length > 64 || to.length > 64 || body.length > 2048) {
      diagnostic.outcome = "invalid-message";
      return new Response("bad request", { status: 400 });
    }
    diagnostic.message_ref = await messageRef(house, id);
    diagnostic.stage = "callback-lookup";
    // Retries must not re-fetch expiring URLs after a successful capture.
    const existing = await env.DB.prepare("SELECT media_count FROM messages WHERE house = ? AND id = ?").bind(house, id).first<{ media_count: number }>();
    if (existing) {
      diagnostic.outcome = existing.media_count === 0 && media.length > 0 ? "duplicate-with-new-media" : "duplicate";
      return new Response("ok");
    }
    diagnostic.stage = "media-capture";
    let captured: { contacts: Contact[]; error: ContactError } = { contacts: [], error: mediaProblem ? "unsupported-media" : "" };
    if (!mediaProblem) {
      try { captured = await captureContacts(media); }
      catch { diagnostic.outcome = "media-unavailable"; return new Response("media unavailable", { status: 503 }); }
    }
    diagnostic.contact_count = captured.contacts.length;
    diagnostic.contact_error = captured.error;
    diagnostic.stage = "callback-save";
    // Idempotent by (house, id): VoIP.ms retries.
    await env.DB.prepare(
      "INSERT OR IGNORE INTO messages (house, id, received_at, sender, recipient, body, media_count, contacts, contact_error, provider_timestamp) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
    ).bind(house, id, new Date().toISOString(), from, to, body, media.length, JSON.stringify(captured.contacts), captured.error, providerTimestamp).run();
    diagnostic.outcome = "queued";
    return new Response("ok");
  }

  // Everything else is the box, with its bearer token.
  const want = houseSecret(env, house, "INBOX_TOKEN");
  if (!want || !constantTimeEqual(bearer(req), want)) return new Response("unauthorized", { status: 401 });

  if (area === "inbox" && tail === "diagnose" && req.method === "GET") {
    diagnostic.stage = "inbox-diagnose";
    const id = url.searchParams.get("id") ?? "";
    if (!/^[0-9]{1,20}$/.test(id)) return json({ error: "numeric message id required" }, 400);
    const row = await env.DB.prepare("SELECT sender, recipient, media_count FROM messages WHERE house = ? AND id = ?").bind(house, id).first<{ sender: string; recipient: string; media_count: number }>();
    const did = houseSecret(env, house, "DID");
    if (!row || !did) return json({ error: "message not found" }, 404);
    try {
      const result = await diagnoseCarrier(env, id, did, row.sender);
      const response = json({ callback_media_count: row.media_count, ...result });
      response.headers.set("Cache-Control", "no-store");
      return response;
    } catch { return json({ error: "carrier diagnostic unavailable" }, 502); }
  }

  if (area === "inbox" && tail === "pull" && req.method === "GET") {
    diagnostic.stage = "inbox-pull";
    const wait = Math.min(25, Math.max(0, Number(url.searchParams.get("wait") ?? "0") || 0));
    const deadline = Date.now() + wait * 1000;
    for (;;) {
      const rows = await env.DB.prepare(
        "SELECT id, received_at, sender, recipient, body, media_count, contacts, contact_error, provider_timestamp, source, phonebooks FROM messages WHERE house = ? AND acked_at IS NULL AND (source = 'sms' OR ? = 1) ORDER BY received_at LIMIT 10",
      ).bind(house, url.searchParams.get("contact_uploads") === "1" ? 1 : 0).all<{ id: string; received_at: string; sender: string; recipient: string; body: string; media_count: number; contacts: string; contact_error: string; provider_timestamp: string; source: string; phonebooks: string }>();
      if (rows.results.length > 0 || Date.now() >= deadline) {
        return json({
          messages: rows.results.map((r) => ({ id: r.id, from: r.sender, to: r.recipient, body: r.body, received_at: r.received_at, provider_timestamp: r.provider_timestamp, media_count: r.media_count, contacts: JSON.parse(r.contacts) as Contact[], contact_error: r.contact_error, source: r.source, phonebooks: JSON.parse(r.phonebooks) as string[] })),
        });
      }
      await new Promise((res) => setTimeout(res, 1000));
    }
  }

  if (area === "inbox" && tail === "ack" && req.method === "POST") {
    diagnostic.stage = "inbox-ack";
    const { ids, results = [] } = (await req.json().catch(() => ({}))) as { ids?: string[]; results?: Array<{ id: string; status: string; message: string }> };
    if (!Array.isArray(ids)) return json({ error: "ids required" }, 400);
    if (!Array.isArray(results) || results.length > 200 || results.some(r => !r || typeof r.id !== "string" ||
      !ids.includes(r.id) || !/^upload:[a-f0-9]{64}$/.test(r.id) || !["saved", "failed"].includes(r.status) ||
      typeof r.message !== "string" || r.message.length < 1 || r.message.length > 512 || /[^\x20-\x7e\n]/.test(r.message))) return json({ error: "invalid contact results" }, 400);
    const now = new Date().toISOString();
    for (const id of ids.slice(0, 200)) {
      const result = results.find(r => r.id === id);
      if (result) {
        await env.DB.prepare("UPDATE messages SET acked_at = ?, upload_result = ? WHERE house = ? AND id = ? AND source = 'contact-upload' AND acked_at IS NULL")
          .bind(now, JSON.stringify({ status: result.status, message: result.message }), house, id).run();
      } else {
        // An old consumer cannot acknowledge an upload without recording its
        // actual result; otherwise a queued receipt would remain queued forever.
        await env.DB.prepare("UPDATE messages SET acked_at = ? WHERE house = ? AND id = ? AND source = 'sms' AND acked_at IS NULL").bind(now, house, String(id)).run();
      }
    }
    // Keep the table small: acked rows older than a week are gone. The
    // carrier's email forwarding is the archive.
    await env.DB.prepare("DELETE FROM messages WHERE house = ? AND acked_at IS NOT NULL AND acked_at < ?")
      .bind(house, new Date(Date.now() - 7 * 86400 * 1000).toISOString()).run();
    return json({ acked: ids.length });
  }

  // Setup aid: the address the carrier sees this Worker call from. getIP is
  // the one VoIP.ms method that answers from a non-listed address, which is
  // the whole point — it tells the operator what to allow-list.
  if (area === "inbox" && tail === "whoami" && req.method === "GET") {
    diagnostic.stage = "inbox-whoami";
    const u = new URL("https://voip.ms/api/v1/rest.php");
    u.searchParams.set("api_username", env.VOIPMS_API_USERNAME);
    u.searchParams.set("api_password", env.VOIPMS_API_PASSWORD);
    u.searchParams.set("method", "getIP");
    const r = await fetch(u.toString());
    return json({ carrier_sees: (await r.json().catch(() => ({}))) });
  }

  if (area === "inbox" && tail === "send" && req.method === "POST") {
    diagnostic.stage = "inbox-send";
    const { to, message } = (await req.json().catch(() => ({}))) as { to?: string; message?: string };
    const did = houseSecret(env, house, "DID");
    if (!did) return json({ error: "house has no DID configured" }, 500);
    if (!to || !message) return json({ error: "to and message required" }, 400);
    const why = replyProblem(message);
    if (why) return json({ error: `refusing a reply that ${why}` }, 422);
    const dst = to.replace(/[^0-9]/g, "");
    if (dst.length < 10) return json({ error: "to must be a phone number" }, 400);
    const r = await sendSMS(env, did, dst, message);
    return json({ accepted: r.ok, status: r.detail }, r.ok ? 200 : 502);
  }

  return new Response("not found", { status: 404 });
}

export default {
  async fetch(req: Request, env: Env): Promise<Response> {
    const started = Date.now();
    const diagnostic: Diagnostic = {
      event: "edge.request", request_ref: crypto.randomUUID(), route: "other",
      method: req.method === "GET" || req.method === "POST" ? req.method : "other", stage: "routing",
    };
    let response: Response;
    try { response = await handle(req, env, diagnostic); }
    catch {
      // Runtime exception logs may include signed URLs or SQL bind values.
      // The fixed stage identifies the failing operation without exposing either.
      diagnostic.outcome = "internal-error";
      response = new Response("internal error", { status: 500 });
    }
    console.log({ ...diagnostic, status: response.status, duration_ms: Date.now() - started });
    return response;
  },
};
