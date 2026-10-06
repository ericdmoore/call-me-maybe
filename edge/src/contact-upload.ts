import { parseContacts } from "./contacts.ts";
import { messageRef, type Diagnostic } from "./diagnostics.ts";
import type { Env } from "./index.ts";
import { MAX_MEDIA_BYTES } from "./media.ts";

interface UploadIdentity { house: string; sender: string; phonebooks: string[] }

function reply(status: number, message: string, extra: Record<string, unknown> = {}): Response {
  return new Response(JSON.stringify({ message, ...extra }), {
    status, headers: { "content-type": "application/json", "cache-control": "no-store" },
  });
}

async function digest(value: string | Uint8Array): Promise<string> {
  const bytes = typeof value === "string" ? new TextEncoder().encode(value) : value;
  const hash = await crypto.subtle.digest("SHA-256", bytes);
  return Array.from(new Uint8Array(hash), b => b.toString(16).padStart(2, "0")).join("");
}

async function identity(req: Request, env: Env): Promise<{ who: UploadIdentity; keyHash: string } | null> {
  const token = (req.headers.get("authorization") ?? "").match(/^Bearer (ddc_[A-Za-z0-9_-]{43})$/)?.[1];
  if (!token || typeof env.CONTACT_UPLOAD_KEYS !== "string") return null;
  // Only digests live at the edge. Each entry binds a key to an existing
  // caller; request fields can never choose who sent the card or which house.
  const keyHash = await digest(token);
  const keys: unknown = JSON.parse(env.CONTACT_UPLOAD_KEYS);
  if (!keys || typeof keys !== "object" || !Object.hasOwn(keys, keyHash)) return null;
  const who = (keys as Record<string, UploadIdentity>)[keyHash];
  if (!who || typeof who.house !== "string" || !/^[a-z0-9][a-z0-9_-]{0,63}$/.test(who.house) ||
      typeof who.sender !== "string" || !/^\+[1-9][0-9]{7,14}$/.test(who.sender) ||
      !Array.isArray(who.phonebooks) || who.phonebooks.length < 1 || who.phonebooks.length > 20 ||
      who.phonebooks.some(book => typeof book !== "string" || !/^[a-z0-9]{1,32}$/.test(book))) return null;
  return { who, keyHash };
}

async function readCard(req: Request): Promise<Uint8Array | null> {
  if (!req.body) return new Uint8Array();
  const reader = req.body.getReader();
  const chunks: Uint8Array[] = [];
  let size = 0;
  try {
    for (;;) {
      const { value, done } = await reader.read();
      if (done) break;
      size += value.byteLength;
      if (size > MAX_MEDIA_BYTES) return null;
      chunks.push(value);
    }
  } finally { await reader.cancel().catch(() => {}); }
  const bytes = new Uint8Array(size);
  let at = 0;
  for (const chunk of chunks) { bytes.set(chunk, at); at += chunk.byteLength; }
  return bytes;
}

export async function contactAPI(req: Request, env: Env, diagnostic: Diagnostic): Promise<Response> {
  const url = new URL(req.url);
  const list = url.pathname === "/api/v1/phonebooks";
  const receipt = url.pathname.match(/^\/api\/v1\/contact-imports\/(upload:[a-f0-9]{64})$/)?.[1];
  diagnostic.route = list ? "contacts.books" : receipt ? "contacts.status" : "contacts.upload";
  diagnostic.stage = "upload-authorization";
  const auth = await identity(req, env);
  if (!auth) return reply(401, "This contact-upload key is missing, invalid or revoked.");
  if (list) {
    if (req.method !== "GET") return reply(405, "Use GET to list phone books.");
    if (url.search) return reply(400, "Unexpected query parameters.");
    return reply(200, "Choose the phone books for this contact.", {
      phonebooks: [...new Set(auth.who.phonebooks)].map(book => book[0].toUpperCase() + book.slice(1)),
    });
  }
  if (receipt) {
    if (req.method !== "GET") return reply(405, "Use GET to check a contact import.");
    diagnostic.stage = "upload-status";
    const wait = url.searchParams.get("wait") ?? "0";
    if (!/^(0|[1-9]|1[0-9]|20)$/.test(wait) || [...url.searchParams.keys()].some(key => key !== "wait")) return reply(400, "wait must be 0-20 seconds.");
    const deadline = Date.now() + Number(wait) * 1000;
    for (;;) {
      const row = await env.DB.prepare("SELECT upload_result FROM messages WHERE house = ? AND id = ? AND upload_key_hash = ? AND source = 'contact-upload'")
        .bind(auth.who.house, receipt, auth.keyHash).first<{ upload_result: string }>();
      if (!row) return reply(404, "Contact import not found.");
      if (row.upload_result) {
        const result = JSON.parse(row.upload_result) as { status: string; message: string };
        return reply(200, result.message, { status: result.status, receipt });
      }
      if (Date.now() >= deadline) return reply(200, "Waiting for the house to save this contact. It has not confirmed a save yet.", { status: "queued", receipt });
      await new Promise(resolve => setTimeout(resolve, 1000));
    }
  }
  if (url.pathname !== "/api/v1/contact-imports") return reply(404, "Not found.");
  if (req.method !== "POST") return reply(405, "Use POST to upload a vCard.");
  diagnostic.stage = "upload-validation";
  if (new URL(req.url).search) return reply(400, "Put the key in Authorization and the vCard in the request body.");
  const destinations = req.headers.get("x-phonebooks") ?? "";
  if (destinations.length > 660) return reply(400, "Select one or more phone books in the Shortcut.");
  const phonebooks = [...new Set(destinations.toLowerCase().split(",").map(book => book.trim()))].sort();
  if (phonebooks.length < 1 || phonebooks.length > 20 || phonebooks.some(book => !/^[a-z0-9]{1,32}$/.test(book))) return reply(400, "Select one or more phone books in the Shortcut.");
  if (phonebooks.some(book => !auth.who.phonebooks.includes(book))) return reply(403, "This key cannot add contacts to one of the selected phone books.");
  const type = (req.headers.get("content-type") ?? "").split(";", 1)[0].trim().toLowerCase();
  if (!["text/vcard", "text/x-vcard"].includes(type)) return reply(415, "Send a vCard file with Content-Type: text/vcard.");
  const length = req.headers.get("content-length");
  if (length && (!/^[0-9]+$/.test(length) || Number(length) > MAX_MEDIA_BYTES)) return reply(413, "The vCard is too large. Maximum size is 1300 KiB.");
  const key = req.headers.get("idempotency-key") ?? crypto.randomUUID();
  if (!/^[A-Za-z0-9_-]{1,80}$/.test(key)) return reply(400, "Idempotency-Key must be 1-80 letters, digits, underscores or hyphens.");
  const did = env[`HOUSE_${auth.who.house.toUpperCase().replace(/-/g, "_")}_DID`];
  if (typeof did !== "string" || !/^[1-9][0-9]{7,14}$/.test(did)) return reply(503, "Contact uploads are not configured for this house.");
  const bytes = await readCard(req);
  if (!bytes) return reply(413, "The vCard is too large. Maximum size is 1300 KiB.");
  let contacts, cards;
  try {
    contacts = parseContacts(bytes);
    cards = new TextDecoder("utf-8", { fatal: true, ignoreBOM: false }).decode(bytes).replace(/\r\n?/g, "\n").match(/^BEGIN:VCARD$/gim)?.length ?? 0;
    if (cards < 1 || cards > 3) return reply(422, "Send one to three contact cards at a time.");
  } catch { return reply(422, "This file is not a supported vCard with a usable phone number. No contacts were queued."); }
  const id = "upload:" + await digest(JSON.stringify([auth.keyHash, key]));
  const fingerprint = await digest(JSON.stringify([await digest(bytes), phonebooks]));
  const { house, sender } = auth.who;
  diagnostic.message_ref = await messageRef(house, id);
  diagnostic.contact_count = contacts.length;
  diagnostic.media_count = cards;
  diagnostic.stage = "upload-save";
  const lookup = () => env.DB.prepare("SELECT upload_fingerprint FROM messages WHERE house = ? AND id = ?")
    .bind(house, id).first<{ upload_fingerprint: string }>();
  let row = await lookup();
  const existed = !!row;
  if (!row) {
    const now = new Date();
    // Bound both new requests and offline backlog per person across all their
    // keys. One SQL statement makes concurrent uploads share the same limit.
    await env.DB.prepare(
      "INSERT OR IGNORE INTO messages (house, id, received_at, sender, recipient, body, media_count, contacts, contact_error, provider_timestamp, upload_fingerprint, source, phonebooks, upload_key_hash) " +
      "SELECT ?, ?, ?, ?, ?, '', ?, ?, '', '', ?, 'contact-upload', ?, ? WHERE " +
      "(SELECT count(*) FROM messages WHERE house = ? AND sender = ? AND id LIKE 'upload:%' AND received_at >= ?) < 30 AND " +
      "(SELECT count(*) FROM messages WHERE house = ? AND sender = ? AND id LIKE 'upload:%' AND acked_at IS NULL) < 30",
    ).bind(house, id, now.toISOString(), sender, did, cards, JSON.stringify(contacts), fingerprint, JSON.stringify(phonebooks), auth.keyHash,
      house, sender, new Date(now.getTime() - 3600000).toISOString(), house, sender).run();
    row = await lookup();
    if (!row) {
      const response = reply(429, "Too many contact uploads are waiting or were sent recently. Please try later.");
      response.headers.set("retry-after", "3600");
      return response;
    }
  }
  if (row.upload_fingerprint !== fingerprint) return reply(409, "That upload ID was already used for a different file. Start a new upload.");
  diagnostic.outcome = existed ? "duplicate" : "queued";
  return reply(202, "Upload received. Waiting for the house to save it to the selected phone books.",
    { status: "queued", receipt: id, number_count: contacts.length });
}
