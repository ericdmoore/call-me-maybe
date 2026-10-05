import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { DatabaseSync } from "node:sqlite";
import worker from "../src/index.ts";

const auth = { authorization: "Bearer inbox-example" };
const mediaURL = "https://voip.ms/media.php?id=example-private-url";
const card = "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Jane Smith\r\nTEL:+15125550123\r\nPHOTO:private-photo\r\nEMAIL:private@example.invalid\r\nEND:VCARD\r\n";
function fixture(t) {
  const db = new DatabaseSync(":memory:");
  t.after(() => db.close());
  db.exec(readFileSync(new URL("../schema.sql", import.meta.url), "utf8"));
  db.exec("INSERT INTO messages VALUES ('test', 'old', '2026-10-04', '15125550101', '15125550100', 'ping', NULL)");
  db.exec(readFileSync(new URL("../migrations/0001_mms_media.sql", import.meta.url), "utf8"));
  const env = {
    HOUSE_TEST_CALLBACK_TOKEN: "callback-example",
    HOUSE_TEST_INBOX_TOKEN: "inbox-example",
    HOUSE_OTHER_INBOX_TOKEN: "other-example",
    DB: { prepare(sql) { return { bind(...values) {
      const statement = db.prepare(sql);
      return {
        async run() { return statement.run(...values); },
        async all() { return { results: statement.all(...values) }; },
        async first() { return statement.get(...values) ?? null; },
      };
    } }; } },
  };
  const request = (path, opts = {}) => worker.fetch(new Request("https://edge.example" + path, opts), env);
  const callback = (id, media = mediaURL) => request("/h/test/sms/callback-example?" + new URLSearchParams({ id, from: "15125550101", to: "15125550100", message: "Add to: house", media }));
  const pull = async () => (await (await request("/h/test/inbox/pull", { headers: auth })).json()).messages;
  return { db, request, callback, pull };
}

test("queue stores structured contacts, never the original attachment or its URL", async (t) => {
  const { db, request, callback, pull } = fixture(t);
  let reads = 0;
  t.mock.method(globalThis, "fetch", async () => { reads++; return new Response(card); });
  assert.equal((await callback("card")).status, 200);
  assert.equal((await callback("card")).status, 200);
  assert.equal(reads, 1, "duplicate callback must not fetch an expiring URL again");
  assert.equal((await request("/h/test/inbox/pull")).status, 401);
  assert.deepEqual((await (await request("/h/other/inbox/pull", { headers: { authorization: "Bearer other-example" } })).json()).messages, []);
  const messages = await pull();
  assert.equal(messages.length, 2);
  assert.equal(messages.find(m => m.id === "old").media_count, 0);
  assert.deepEqual(messages.find(m => m.id === "old").contacts, []);
  const captured = messages.find(m => m.id === "card");
  assert.equal(captured.media_count, 1);
  assert.equal(captured.contact_error, "");
  assert.deepEqual(captured.contacts, [{ name: "Jane Smith", number: "+15125550123" }]);
  for (const data of [JSON.stringify(messages), JSON.stringify(db.prepare("SELECT * FROM messages").all())]) {
    assert.doesNotMatch(data, /media\.php|BEGIN:VCARD|private-photo|private@example/);
  }
  assert.equal((await request("/h/test/inbox/media?id=card&index=0", { headers: auth })).status, 404);
  assert.equal((await request("/h/test/inbox/ack", { method: "POST", headers: auth, body: JSON.stringify({ ids: ["card"] }) })).status, 200);
  assert.deepEqual((await pull()).map(m => m.id), ["old"]);
});

test("malformed attachments queue only a safe error and no partial contact batch", async (t) => {
  const { callback, pull } = fixture(t);
  t.mock.method(globalThis, "fetch", async url => new Response(url.toString().endsWith("bad") ? "BEGIN:VCARD\nFN:private" : card));
  assert.equal((await callback("bad-card", mediaURL + ",https://voip.ms/bad")).status, 200);
  const message = (await pull()).find(m => m.id === "bad-card");
  assert.equal(message.contact_error, "invalid-card");
  assert.deepEqual(message.contacts, []);
  assert.equal(message.media_count, 2);
});

test("transient download failures do not acknowledge or queue a partial message", async (t) => {
  const { callback, pull } = fixture(t);
  const mocked = t.mock.method(globalThis, "fetch", async () => { throw new Error("signed-url-secret"); });
  const failure = await callback("retry");
  assert.equal(failure.status, 503);
  assert.equal(await failure.text(), "media unavailable");
  assert.deepEqual((await pull()).map(m => m.id), ["old"]);
  mocked.mock.mockImplementation(async () => new Response(card));
  assert.equal((await callback("retry")).status, 200);
  assert.equal((await pull()).find(m => m.id === "retry").contacts.length, 1);
});

test("ordinary SMS performs no media requests; disallowed media is not fetched", async (t) => {
  const { callback, pull } = fixture(t);
  let reads = 0;
  t.mock.method(globalThis, "fetch", async () => { reads++; throw new Error("unexpected fetch"); });
  assert.equal((await callback("sms", "")).status, 200);
  assert.equal((await callback("ssrf", "https://127.0.0.1/private")).status, 400);
  assert.equal(reads, 0);
  assert.equal((await pull()).find(m => m.id === "sms").contact_error, "");
});

test("maximum contact payloads fit a bounded pull and subsequent messages remain queued", async (t) => {
  const { callback, pull, request } = fixture(t);
  const large = Array.from({ length: 100 }, (_, i) => "BEGIN:VCARD\nFN:" + "😀".repeat(200) + `\nTEL:+151255501${String(i).padStart(2, "0")}\nEND:VCARD\n`).join("");
  t.mock.method(globalThis, "fetch", async () => new Response(large));
  for (let i = 0; i < 11; i++) assert.equal((await callback(`large-${i}`)).status, 200);
  const messages = await pull();
  assert.equal(messages.length, 10);
  assert.ok(new TextEncoder().encode(JSON.stringify({ messages })).length < 1024 * 1024);
  await request("/h/test/inbox/ack", { method: "POST", headers: auth, body: JSON.stringify({ ids: messages.map(m => m.id) }) });
  assert.equal((await pull()).length, 2);
});

test("rejected callback credentials or excessive text do not trigger media fetches", async (t) => {
  const { request } = fixture(t);
  let reads = 0;
  t.mock.method(globalThis, "fetch", async () => { reads++; return new Response(card); });
  const params = new URLSearchParams({ id: "large", from: "15125550101", message: "a".repeat(2049), media: mediaURL });
  assert.equal((await request("/h/test/sms/wrong-token?" + params)).status, 404);
  assert.equal((await request("/h/test/sms/callback-example?" + params)).status, 400);
  assert.equal(reads, 0);
});
