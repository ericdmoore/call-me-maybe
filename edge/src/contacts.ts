import { readMedia } from "./media.ts";

// This is the entire attachment contract with the house. No URLs, blobs,
// photos, addresses or arbitrary vCard properties cross that boundary.
export interface Contact { name: string; number: string }
export type ContactError = "" | "invalid-card" | "too-many-contacts" | "media-too-large" | "unsupported-media" | "media-gone";
export const MAX_CONTACTS = 100;
// Far above any real card within MAX_MEDIA_BYTES; a file of two-byte lines
// is a denial-of-service attempt, not a contact.
const MAX_LINES = 100_000;

class InvalidCard extends Error {}
class TooManyContacts extends Error {}
const invalid = (): never => { throw new InvalidCard(); };

function splitQuoted(value: string, separator: string, first = false): string[] {
  let quoted = false, start = 0;
  const parts: string[] = [];
  for (let i = 0; i < value.length; i++) {
    if (value[i] === '"') quoted = !quoted;
    if (value[i] === separator && !quoted) {
      parts.push(value.slice(start, i));
      start = i + 1;
      if (first) break;
    }
  }
  if (quoted) invalid();
  parts.push(value.slice(start));
  return parts;
}

function splitEscaped(value: string): string[] {
  const parts: string[] = [];
  let start = 0;
  for (let i = 0; i < value.length; i++) {
    if (value[i] === "\\") i++;
    else if (value[i] === ";") { parts.push(value.slice(start, i)); start = i + 1; }
  }
  parts.push(value.slice(start));
  return parts;
}

function unescape(value: string): string {
  return value.replace(/\\(.)/g, (_, ch: string) => /n/i.test(ch) ? " " : ch);
}

function decodeQP(value: string): string {
  const bytes: number[] = [];
  for (let i = 0; i < value.length; i++) {
    if (value[i] === "=") {
      const hex = value.slice(i + 1, i + 3);
      if (!/^[0-9a-f]{2}$/i.test(hex)) invalid();
      bytes.push(parseInt(hex, 16));
      i += 2;
    } else {
      const codepoint = value.codePointAt(i)!;
      bytes.push(...new TextEncoder().encode(String.fromCodePoint(codepoint)));
      if (codepoint > 0xffff) i++;
    }
  }
  return new TextDecoder("utf-8", { fatal: true, ignoreBOM: false }).decode(new Uint8Array(bytes));
}

function cleanName(value: string): string {
  const name = value.replace(/\s+/gu, " ").trim();
  if ([...name].length > 200 || /[\p{Cc}\p{Cs}\uFFFE\uFFFF]/u.test(name)) invalid();
  return name;
}

function phoneNumber(value: string): string {
  // Keep national numbers national: the house owns its country-code policy.
  const number = value.trim().replace(/^tel:/i, "").split(";", 1)[0].trim();
  if (number.length > 64 || !/^\+?[0-9(). -]+$/.test(number)) invalid();
  const digits = number.replace(/[(). -]/g, "");
  if (!/^\+?[0-9]{7,15}$/.test(digits)) invalid();
  return digits;
}

// Unfold physical lines into logical ones in one pass. Appending each fold to
// the accumulated string and testing its tail is quadratic: a 1300 KiB card
// made of two-byte folds then costs seconds of CPU per callback, and the
// carrier retries it.
function unfold(text: string): string[] {
  const lines: string[] = [];
  let parts: string[] = [];
  let qp = false; // the current property is quoted-printable
  let softBreak = false; // the last piece ended with "="
  let count = 0;
  const flush = () => { if (parts.length) lines.push(parts.join("")); parts = []; };
  for (const raw of text.replace(/\r\n?/g, "\n").split("\n")) {
    if (!raw) continue;
    if (++count > MAX_LINES) invalid();
    if (softBreak && qp && parts.length) {
      // A quoted-printable soft break: the "=" goes, and the continuation is
      // data exactly as written, leading whitespace included (RFC 2045 6.7).
      parts[parts.length - 1] = parts[parts.length - 1].slice(0, -1);
      parts.push(raw);
    } else if (/^[ \t]/.test(raw) && parts.length) {
      parts.push(raw.slice(1));
    } else {
      flush();
      parts = [raw];
      qp = /QUOTED-PRINTABLE/i.test(raw.split(":", 1)[0]);
    }
    softBreak = raw.endsWith("=");
  }
  flush();
  return lines;
}

// A bounded subset of vCard 2.1/3.0/4.0, matching the name/TEL forms used
// by phone exporters. Reject incomplete or nested cards as a whole. The card
// count comes from the same walk as the contacts, so the two can never
// disagree about what one file holds.
export function parseCards(bytes: Uint8Array): { contacts: Contact[]; cards: number } {
  const text = new TextDecoder("utf-8", { fatal: true, ignoreBOM: false }).decode(bytes);
  const lines = unfold(text);
  const contacts = new Map<string, Contact>();
  let card: { fn: string; n: string; org: string; numbers: Set<string> } | undefined;
  let cards = 0;
  for (const line of lines) {
    const pair = splitQuoted(line, ":", true);
    if (pair.length !== 2) invalid();
    const [head, raw] = pair;
    const fields = splitQuoted(head, ";");
    const name = fields[0].split(".").at(-1)!.trim().toUpperCase();
    if (!/^[A-Z0-9-]+$/.test(name)) invalid();
    if (name === "BEGIN") {
      if (card || raw.toUpperCase() !== "VCARD") invalid();
      card = { fn: "", n: "", org: "", numbers: new Set() };
      continue;
    }
    if (!card) invalid();
    const current = card!;
    if (name === "END") {
      if (raw.toUpperCase() !== "VCARD" || current.numbers.size === 0) invalid();
      const display = cleanName(current.fn || current.n || current.org);
      for (const number of current.numbers) {
        if (!contacts.has(number)) contacts.set(number, { name: display, number });
        if (contacts.size > MAX_CONTACTS) throw new TooManyContacts();
      }
      cards++;
      card = undefined;
      continue;
    }
    if (/^BEGIN:VCARD/i.test(raw.trim())) invalid();
    if (!["FN", "N", "ORG", "TEL", "VERSION"].includes(name)) continue;
    if (raw.length > 4096) invalid();
    const parameters = fields.slice(1).map(v => v.trim().toUpperCase().replace(/"/g, ""));
    if (parameters.some(v => v.startsWith("CHARSET=") && v !== "CHARSET=UTF-8" && v !== "CHARSET=US-ASCII")) invalid();
    if (parameters.some(v => v === "BASE64" || v === "ENCODING=B" || v === "ENCODING=BASE64")) invalid();
    const value = parameters.some(v => v === "QUOTED-PRINTABLE" || v === "ENCODING=QUOTED-PRINTABLE") ? decodeQP(raw) : raw;
    if (name === "VERSION" && !["2.1", "3.0", "4.0"].includes(value)) invalid();
    if (name === "FN") current.fn = cleanName(unescape(value));
    if (name === "N") {
      const parts = splitEscaped(value);
      current.n = cleanName(unescape((parts[1] || "") + " " + parts[0]));
    }
    if (name === "ORG") current.org = cleanName(unescape(splitEscaped(value)[0]));
    if (name === "TEL") {
      current.numbers.add(phoneNumber(value));
      if (current.numbers.size > MAX_CONTACTS) throw new TooManyContacts();
    }
  }
  if (card || cards === 0) invalid();
  return { contacts: [...contacts.values()], cards };
}

export function parseContacts(bytes: Uint8Array): Contact[] {
  return parseCards(bytes).contacts;
}

// Transient fetch failures escape to the callback as a retryable failure.
// Permanent input failures become bounded metadata, never partial contacts.
export async function captureContacts(media: string[]): Promise<{ contacts: Contact[]; error: ContactError }> {
  const fetched = await Promise.all(media.map(readMedia));
  if (fetched.some(r => r.status === 502)) throw new Error("media unavailable");
  if (fetched.some(r => r.status === 413)) return { contacts: [], error: "media-too-large" };
  // The carrier's copy has expired or been removed. Retrying the same URL
  // cannot bring it back, so the text is queued with that fact rather than
  // refused on every retry until the carrier gives up.
  if (fetched.some(r => r.status === 410)) return { contacts: [], error: "media-gone" };
  if (fetched.some(r => !r.ok)) return { contacts: [], error: "unsupported-media" };
  const contacts = new Map<string, Contact>();
  try {
    for (const response of fetched) {
      for (const contact of parseContacts(new Uint8Array(await response.arrayBuffer()))) {
        if (!contacts.has(contact.number)) contacts.set(contact.number, contact);
        if (contacts.size > MAX_CONTACTS) throw new TooManyContacts();
      }
    }
  } catch (error) {
    return { contacts: [], error: error instanceof TooManyContacts ? "too-many-contacts" : "invalid-card" };
  }
  return { contacts: [...contacts.values()], error: "" };
}
