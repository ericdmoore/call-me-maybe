// Read-only, count-only inspection of an already queued house message. The
// carrier key and any media URLs stay at the edge; this cannot send or import.
export interface CarrierCredentials {
  VOIPMS_API_USERNAME: string;
  VOIPMS_API_PASSWORD: string;
}

type ObjectValue = Record<string, unknown>;
const object = (v: unknown): v is ObjectValue => !!v && typeof v === "object" && !Array.isArray(v);
const digits = (v: unknown): string => typeof v === "string" ? v.replace(/[^0-9]/g, "").replace(/^1(?=[0-9]{10}$)/, "") : "";

async function readCarrier(env: CarrierCredentials, method: string, args: Record<string, string>): Promise<ObjectValue> {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), 10000);
  try { return await readCarrierWithin(env, method, args, controller.signal); }
  finally { clearTimeout(timer); }
}

async function readCarrierWithin(env: CarrierCredentials, method: string, args: Record<string, string>, signal: AbortSignal): Promise<ObjectValue> {
  const params = new URLSearchParams({ api_username: env.VOIPMS_API_USERNAME, api_password: env.VOIPMS_API_PASSWORD, method, ...args });
  let response: Response;
  try {
    // Use the same REST GET transport as the working SMS sender. The
    // carrier returned HTTP 500 for POST lookups during live diagnosis.
    // Never log this URL or propagate a raw fetch exception.
    response = await fetch("https://voip.ms/api/v1/rest.php?" + params, {
      method: "GET", redirect: "manual", signal,
    });
  } catch { return { status: "carrier-network-error" }; }
  if (!response.ok || !response.body) return { status: "carrier-http-error", http_status: response.status };
  const reader = response.body.getReader();
  const chunks: Uint8Array[] = [];
  let length = 0;
  try {
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      length += value.byteLength;
      if (length > 65536) throw new Error("carrier diagnostic response too large");
      chunks.push(value);
    }
  } finally { await reader.cancel(); }
  const bytes = new Uint8Array(length);
  let offset = 0;
  for (const chunk of chunks) { bytes.set(chunk, offset); offset += chunk.length; }
  let data: unknown;
  try { data = JSON.parse(new TextDecoder().decode(bytes)); }
  catch { return { status: "carrier-invalid-json", http_status: response.status }; }
  if (!object(data)) return { status: "carrier-invalid-json", http_status: response.status };
  return data;
}

function status(data: ObjectValue): string {
  // Unknown status strings may contain message content or credentials.
  return ["success", "no_mms", "no_sms", "no_media", "invalid_id", "invalid_mms", "invalid_credentials", "api_not_enabled", "ip_not_allowed", "api_limit_exceeded", "carrier-http-error", "carrier-network-error", "carrier-invalid-json"].includes(String(data.status)) ? String(data.status) : "carrier-error";
}

function mediaValues(data: ObjectValue): string[] {
  const media = data.media;
  const values: unknown[] = Array.isArray(media) ? media : object(media) ? Object.values(media) : [];
  return [...new Set([...values, data.col_media1, data.col_media2, data.col_media3].filter((v): v is string => typeof v === "string" && v.trim() !== ""))];
}

export async function diagnoseCarrier(env: CarrierCredentials, id: string, did: string, sender: string): Promise<ObjectValue> {
  const found = await readCarrier(env, "getMMS", { mms: id, did, type: "1", limit: "1" });
  const result: ObjectValue = { lookup_status: status(found), message_found: false };
  if (typeof found.http_status === "number") result.lookup_http_status = found.http_status;
  if (found.status !== "success") return result;
  const rows = Array.isArray(found.sms) ? found.sms : object(found.sms) ? [found.sms] : [];
  const row = rows.find((v: unknown) => object(v) && String(v.id) === id && String(v.type) === "1" && digits(v.did) === digits(did) && digits(v.contact) === digits(sender));
  if (!object(row)) return result;
  // Check both DID and sender before exposing even counts. IDs alone are not
  // evidence that a carrier record belongs to this house's conversation.
  result.message_found = true;
  result.record_media_count = mediaValues(row).length;
  const media = await readCarrier(env, "getMediaMMS", { id, media_as_array: "1" });
  result.media_lookup_status = status(media);
  if (typeof media.http_status === "number") result.media_lookup_http_status = media.http_status;
  if (media.status === "success" && String(media.id) === id) {
    result.retrievable_media_count = mediaValues(media).length;
  }
  return result;
}
