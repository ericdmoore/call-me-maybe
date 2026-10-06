// Fixed labels and counts only. Callback paths contain credentials, and
// captions, media URLs and thrown errors can all contain private data.
export interface Diagnostic {
  event: "edge.request";
  request_ref: string;
  route: "other" | "callback" | "inbox.pull" | "inbox.ack" | "inbox.send" | "inbox.whoami" | "inbox.diagnose" | "contacts.upload" | "contacts.books" | "contacts.status";
  method: "GET" | "POST" | "other";
  stage: "routing" | "authorization" | "callback-validation" | "callback-lookup" | "media-capture" | "callback-save" | "inbox-pull" | "inbox-ack" | "inbox-send" | "inbox-whoami" | "inbox-diagnose" | "upload-authorization" | "upload-validation" | "upload-save" | "upload-status";
  outcome?: "invalid-media" | "invalid-message" | "duplicate" | "duplicate-with-new-media" | "media-unavailable" | "queued" | "internal-error";
  message_ref?: string;
  media_field?: "missing" | "empty" | "unexpanded" | "present";
  media_count?: number;
  contact_count?: number;
  contact_error?: "" | "invalid-card" | "too-many-contacts" | "media-too-large" | "unsupported-media";
  timestamp_field?: "missing" | "empty" | "unexpanded" | "present";
}

export function fieldState(value: string | null, placeholder: string): "missing" | "empty" | "unexpanded" | "present" {
  if (value === null) return "missing";
  if (!value.trim()) return "empty";
  return value.trim() === placeholder ? "unexpanded" : "present";
}

// Correlate carrier retries without logging an arbitrary carrier-supplied ID.
export async function messageRef(house: string, id: string): Promise<string> {
  const bytes = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(JSON.stringify([house, id])));
  return Array.from(new Uint8Array(bytes)).map(b => b.toString(16).padStart(2, "0")).join("").slice(0, 24);
}
