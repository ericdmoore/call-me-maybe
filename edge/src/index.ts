// The house's edge inbox.
//
// Three doors, each behind its own credential:
//   GET  /h/:house/sms/:callbackToken?from=&to=&message=&id=   VoIP.ms calls this
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

// The reply rule, enforced here as well as on the box: plain ASCII, one
// segment, no digits, no links, no exclamation marks.
function replyProblem(s: string): string | null {
  if (s.trim() === "") return "is empty";
  if (s.length > 160) return `is ${s.length} characters; one segment is 160`;
  if (s.includes("!")) return "has an exclamation mark";
  if (/http/i.test(s)) return "has a link";
  if (/[0-9]/.test(s)) return "has digits";
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
  const resp = await fetch(u.toString(), { method: "GET" });
  const text = await resp.text();
  let status = "";
  try { status = (JSON.parse(text) as { status?: string }).status ?? ""; } catch { status = text.slice(0, 40); }
  // `success` means accepted, not delivered. There are no receipts.
  return { ok: status === "success", detail: status };
}

export default {
  async fetch(req: Request, env: Env): Promise<Response> {
    const url = new URL(req.url);
    const m = url.pathname.match(/^\/h\/([^/]+)\/(sms|inbox)\/([^/]+)$/);
    if (!m) return new Response("not found", { status: 404 });
    const [, house, area, tail] = m;
    if (!HOUSE.test(house)) return new Response("not found", { status: 404 });

    // The carrier's callback: the token is the path.
    if (area === "sms" && req.method === "GET") {
      const want = houseSecret(env, house, "CALLBACK_TOKEN");
      if (!want || !constantTimeEqual(tail, want)) return new Response("not found", { status: 404 });
      const q = url.searchParams;
      const id = q.get("id") ?? "";
      const from = q.get("from") ?? "";
      const to = q.get("to") ?? "";
      const body = q.get("message") ?? "";
      if (!id || !from) return new Response("bad request", { status: 400 });
      // Idempotent by (house, id): VoIP.ms retries.
      await env.DB.prepare(
        "INSERT OR IGNORE INTO messages (house, id, received_at, sender, recipient, body) VALUES (?, ?, ?, ?, ?, ?)",
      ).bind(house, id, new Date().toISOString(), from, to, body).run();
      return new Response("ok");
    }

    // Everything else is the box, with its bearer token.
    const want = houseSecret(env, house, "INBOX_TOKEN");
    if (!want || !constantTimeEqual(bearer(req), want)) return new Response("unauthorized", { status: 401 });

    if (area === "inbox" && tail === "pull" && req.method === "GET") {
      const wait = Math.min(25, Math.max(0, Number(url.searchParams.get("wait") ?? "0") || 0));
      const deadline = Date.now() + wait * 1000;
      for (;;) {
        const rows = await env.DB.prepare(
          "SELECT id, received_at, sender, recipient, body FROM messages WHERE house = ? AND acked_at IS NULL ORDER BY received_at LIMIT 50",
        ).bind(house).all<{ id: string; received_at: string; sender: string; recipient: string; body: string }>();
        if (rows.results.length > 0 || Date.now() >= deadline) {
          return json({
            messages: rows.results.map((r) => ({ id: r.id, from: r.sender, to: r.recipient, body: r.body, received_at: r.received_at })),
          });
        }
        await new Promise((res) => setTimeout(res, 1000));
      }
    }

    if (area === "inbox" && tail === "ack" && req.method === "POST") {
      const { ids } = (await req.json().catch(() => ({}))) as { ids?: string[] };
      if (!Array.isArray(ids)) return json({ error: "ids required" }, 400);
      const now = new Date().toISOString();
      for (const id of ids.slice(0, 200)) {
        await env.DB.prepare("UPDATE messages SET acked_at = ? WHERE house = ? AND id = ? AND acked_at IS NULL").bind(now, house, String(id)).run();
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
      const u = new URL("https://voip.ms/api/v1/rest.php");
      u.searchParams.set("api_username", env.VOIPMS_API_USERNAME);
      u.searchParams.set("api_password", env.VOIPMS_API_PASSWORD);
      u.searchParams.set("method", "getIP");
      const r = await fetch(u.toString());
      return json({ carrier_sees: (await r.json().catch(() => ({}))) });
    }

    if (area === "inbox" && tail === "send" && req.method === "POST") {
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
  },
};
