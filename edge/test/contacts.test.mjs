import test from "node:test";
import assert from "node:assert/strict";
import { parseContacts, parseCards, captureContacts } from "../src/contacts.ts";
import { MAX_MEDIA_BYTES } from "../src/media.ts";

const parse = text => parseContacts(new TextEncoder().encode(text));
const wrap = text => "BEGIN:VCARD\r\n" + text + "\r\nEND:VCARD\r\n";
const simple = wrap("VERSION:3.0\nFN:Jane Smith\nTEL:+15125550123");

test("vCard 2.1, 3.0, 4.0 exporters reduce to names and dial strings", () => {
  for (const [body, expected] of [
    ["VERSION:3.0\nFN:Jane\\, Smith\nitem1.TEL;TYPE=CELL:(512) 555-0123\nPHOTO:ignored", { name: "Jane, Smith", number: "5125550123" }],
    ["VERSION:2.1\nFN;ENCODING=QUOTED-PRINTABLE:Jane=20=\nSmith\nTEL;HOME:5125550123", { name: "Jane Smith", number: "5125550123" }],
    ['VERSION:4.0\nN:Smith;Jane;;;\nTEL;VALUE=uri;TYPE="voice,cell":tel:+15125550123;ext=42', { name: "Jane Smith", number: "+15125550123" }],
    ["FN:Jane \\nSmith\nTEL:+15125550123", { name: "Jane Smith", number: "+15125550123" }],
    ["FN:Jane Smi\n th\nTEL:+15125550123", { name: "Jane Smith", number: "+15125550123" }],
    ["N:Smith;Jane;;;\nFN:Preferred\nTEL:+15125550123", { name: "Preferred", number: "+15125550123" }],
    ["ORG:Shop\\; Sons;ignored\nTEL:+15125550123", { name: "Shop; Sons", number: "+15125550123" }],
    ["TEL:+15125550123", { name: "", number: "+15125550123" }],
    ["FN;ENCODING=QUOTED-PRINTABLE:Jos=C3=A9\nTEL:+15125550123", { name: "José", number: "+15125550123" }],
  ]) assert.deepEqual(parse(wrap(body)), [expected]);
});

test("incomplete cards and unsafe retained fields fail the entire document", () => {
  for (const text of [
    "not a vcard", "BEGIN:VCARD\nFN:unfinished", simple + "unfinished",
    wrap("FN:Jane"), wrap("TEL:not-a-number"), wrap("TEL:https://example.invalid/15125550123"),
    wrap("FN:Jane\nBEGIN:VCARD\nTEL:+15125550123\nEND:VCARD"),
    wrap("TEL:+15125550123\nAGENT:BEGIN:VCARD\nTEL:+15125550124\nEND:VCARD"),
    wrap("FN;ENCODING=QUOTED-PRINTABLE:Jane=00Smith\nTEL:+15125550123"),
    wrap("FN;ENCODING=QUOTED-PRINTABLE:Jos=E9\nTEL:+15125550123"),
    wrap("FN;ENCODING=QUOTED-PRINTABLE:Jane=ZZ\nTEL:+15125550123"),
    wrap("FN;CHARSET=ISO-8859-1:Jane\nTEL:+15125550123"),
    wrap("FN;ENCODING=b:SmFuZQ==\nTEL:+15125550123"),
    wrap("FN:" + "a".repeat(201) + "\nTEL:+15125550123"),
    wrap("FN:bad\uFFFF\nTEL:+15125550123"),
    wrap('TEL;TYPE="cell:+15125550123'),
    wrap("VERSION:5.0\nTEL:+15125550123"),
  ]) assert.throws(() => parse(text), undefined, text.slice(0, 80));
  assert.throws(() => parseContacts(new Uint8Array([0xff])));
});

test("multiple numbers and cards are deduplicated with the first name preserved", () => {
  const data = wrap("FN:First\nTEL:+15125550123\nTEL:+15125550124") + wrap("FN:Second\nTEL:+15125550123");
  assert.deepEqual(parse(data), [{ name:"First", number:"+15125550123" }, { name:"First", number:"+15125550124" }]);
});

test("capture caps numbers across all attachments and returns no partial data", async (t) => {
  const hundred = Array.from({ length:100 }, (_, i) => wrap(`FN:Example\nTEL:+151255501${String(i).padStart(2, "0")}`)).join("");
  const mocked = t.mock.method(globalThis, "fetch", async () => new Response(hundred));
  assert.equal((await captureContacts(["https://voip.ms/a", "https://voip.ms/b"])).contacts.length, 100);
  mocked.mock.mockImplementation(async url => new Response(url.toString().endsWith("b") ? wrap("FN:Extra\nTEL:+12145550123") : hundred));
  assert.deepEqual(await captureContacts(["https://voip.ms/a", "https://voip.ms/b"]), { contacts:[], error:"too-many-contacts" });
  mocked.mock.mockImplementation(async () => new Response(new Uint8Array(MAX_MEDIA_BYTES + 1)));
  assert.deepEqual(await captureContacts(["https://voip.ms/a"]), { contacts:[], error:"media-too-large" });
});

test("an expired or removed carrier file is a fixed error, not a transient failure", async (t) => {
  const mocked = t.mock.method(globalThis, "fetch", async () => new Response("gone", { status: 404 }));
  assert.deepEqual(await captureContacts(["https://voip.ms/a"]), { contacts:[], error:"media-gone" });
  mocked.mock.mockImplementation(async () => new Response("later", { status: 503 }));
  await assert.rejects(captureContacts(["https://voip.ms/a"]));
});

test("a quoted-printable soft break keeps the continuation's leading whitespace", () => {
  assert.deepEqual(parse(wrap("FN;ENCODING=QUOTED-PRINTABLE:Jane=\n Smith\nTEL:+15125550123")), [{ name: "Jane Smith", number: "+15125550123" }]);
});

test("the card count comes from the parser, so grouped openers count too", () => {
  const grouped = "g.BEGIN:VCARD\nFN:A\nTEL:+15125550123\nEND:VCARD\n";
  assert.equal(parseCards(new TextEncoder().encode(simple + grouped + grouped)).cards, 3);
  assert.equal(parseCards(new TextEncoder().encode(simple)).cards, 1);
});

test("unfolding a card of tiny folds is linear in its size", () => {
  // A NOTE of 99,000 short folds (1.2 MB, under the line cap) took seconds to
  // unfold when each fold re-scanned the accumulated line; it must now stay
  // well under a second, and one line over the cap is refused outright.
  const text = wrap("TEL:+15125550123\nNOTE:" + (" " + "A".repeat(10) + "\n").repeat(99_000).trimEnd());
  assert.ok(new TextEncoder().encode(text).length < MAX_MEDIA_BYTES);
  const started = performance.now();
  assert.deepEqual(parse(text), [{ name: "", number: "+15125550123" }]);
  assert.ok(performance.now() - started < 1000, "unfolding took too long");
  assert.throws(() => parse(wrap("TEL:+15125550123\nNOTE:" + " A\n".repeat(100_001).trimEnd())));
});
