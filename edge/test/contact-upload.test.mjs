import test from "node:test";
import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { readFileSync, readdirSync } from "node:fs";
import { DatabaseSync } from "node:sqlite";
import worker from "../src/index.ts";
import { MAX_MEDIA_BYTES } from "../src/media.ts";

const token = "ddc_" + "a".repeat(43);
const secondToken = "ddc_" + "b".repeat(43);
const hash = value => createHash("sha256").update(value).digest("hex");
const card = "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Jane Example\r\nTEL:+15125550123\r\nPHOTO:private-photo\r\nEMAIL:private@example.invalid\r\nEND:VCARD\r\n";

function fixture(t) {
  const logs = [];
  t.mock.method(console, "log", entry => logs.push(entry));
  t.mock.method(globalThis, "fetch", async () => { throw new Error("An upload must never make an external request"); });
  const db = new DatabaseSync(":memory:");
  t.after(() => db.close());
  db.exec(readFileSync(new URL("../schema.sql", import.meta.url), "utf8"));
  const migrations = new URL("../migrations/", import.meta.url);
  for (const name of readdirSync(migrations).filter(name => name.endsWith(".sql")).sort()) db.exec(readFileSync(new URL(name, migrations), "utf8"));
  const keys = {
    [hash(token)]: { house: "test", sender: "+15125550101", phonebooks: ["house", "kitchen"] },
    [hash(secondToken)]: { house: "other", sender: "+15125550102", phonebooks: ["house"] },
  };
  const env = {
    CONTACT_UPLOAD_KEYS: JSON.stringify(keys),
    HOUSE_TEST_DID: "15125550100", HOUSE_OTHER_DID: "15125550199",
    HOUSE_TEST_INBOX_TOKEN: "inbox-example", HOUSE_TEST_CALLBACK_TOKEN: "callback-example",
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
  const upload = (body = card, headers = {}, extra = {}) => request("/api/v1/contact-imports", {
    method: "POST", body,
    headers: { authorization: "Bearer " + token, "content-type": "text/vcard", "x-phonebooks": "House", "idempotency-key": "test-upload", ...headers }, ...extra,
  });
  return { db, logs, env, keys, upload, request };
}

test("upload keys only submit contacts with server-bound identity; files and secrets never reach logs", async t => {
  const { db, request, upload, logs } = fixture(t);
  for (const value of ["", "Bearer inbox-example", "Bearer callback-example", "Bearer ddc_" + "x".repeat(43)]) {
    assert.equal((await upload(card, { authorization: value })).status, 401);
  }
  assert.equal((await request("/h/test/inbox/pull", { headers: { authorization: "Bearer " + token } })).status, 401);
  assert.equal((await request("/h/test/inbox/send", { method: "POST", headers: { authorization: "Bearer " + token }, body: JSON.stringify({ to: "15125550101", message: "ping" }) })).status, 401);
  const response = await upload(card, { "x-sender": "+15125550199", "x-house": "other", "x-caption": "garage" });
  assert.equal(response.status, 202);
  assert.equal(response.headers.get("cache-control"), "no-store");
  const result = await response.json();
  assert.equal(result.status, "queued");
  assert.match(result.message, /Waiting for the house/);
  assert.match(result.receipt, /^upload:[a-f0-9]{64}$/);
  const row = db.prepare("SELECT * FROM messages").get();
  assert.equal(row.house, "test");
  assert.equal(row.sender, "+15125550101");
  assert.equal(row.recipient, "15125550100");
  assert.equal(row.body, "");
  assert.equal(row.source, "contact-upload");
  assert.deepEqual(JSON.parse(row.phonebooks), ["house"]);
  assert.equal(row.media_count, 1);
  assert.equal(row.acked_at, null);
  assert.deepEqual(JSON.parse(row.contacts), [{ name: "Jane Example", number: "+15125550123" }]);
  assert.doesNotMatch(JSON.stringify(row), /BEGIN:VCARD|private-photo|private@example|ddc_/);
  assert.doesNotMatch(JSON.stringify(logs), /Jane|Example|1512555|private|ddc_|test-upload/);
  assert.equal(logs.at(-1).route, "contacts.upload");
});

test("retries preserve the receipt and deadline, reject changed files, and isolate identities", async t => {
  const { db, upload } = fixture(t);
  const first = await (await upload()).json();
  const row = db.prepare("SELECT * FROM messages").get();
  db.prepare("UPDATE messages SET acked_at = ?").run(new Date().toISOString());
  const again = await (await upload()).json();
  assert.equal(again.receipt, first.receipt);
  assert.equal(db.prepare("SELECT count(*) AS n FROM messages").get().n, 1);
  assert.equal(db.prepare("SELECT received_at FROM messages").get().received_at, row.received_at);
  assert.ok(db.prepare("SELECT acked_at FROM messages").get().acked_at);
  assert.equal((await upload(card.replace("Jane", "Different"))).status, 409);
  assert.equal((await upload(card, { "x-phonebooks": "Kitchen" })).status, 409);
  const other = await (await upload(card, { authorization: "Bearer " + secondToken })).json();
  assert.notEqual(other.receipt, first.receipt);
  assert.equal(db.prepare("SELECT house FROM messages WHERE id = ?").get(other.receipt).house, "other");
});

test("the picker lists scoped books and every upload requires an explicit complete selection", async t => {
  const { db, upload, request } = fixture(t);
  const headers = { authorization: "Bearer " + token };
  assert.equal((await request("/api/v1/phonebooks")).status, 401);
  assert.deepEqual((await (await request("/api/v1/phonebooks", { headers })).json()).phonebooks, ["House", "Kitchen"]);
  for (const book of ["", ",", "house,", "house kitchen"]) assert.equal((await upload(card, { "x-phonebooks": book })).status, 400);
  assert.equal((await upload(card, { "x-phonebooks": "house,garage" })).status, 403);
  assert.equal(db.prepare("SELECT count(*) AS n FROM messages").get().n, 0);
  assert.equal((await upload(card, { "x-phonebooks": " Kitchen, HOUSE, kitchen " })).status, 202);
  assert.deepEqual(JSON.parse(db.prepare("SELECT phonebooks FROM messages").get().phonebooks), ["house", "kitchen"]);
  assert.equal((await upload(card, { "x-phonebooks": "house,kitchen" })).status, 202);
});

test("receipts show saved only after a scoped result from the house; old consumers cannot take uploads", async t => {
  const { upload, request, env } = fixture(t);
  const user = { authorization: "Bearer " + token };
  const box = { authorization: "Bearer inbox-example" };
  const { receipt } = await (await upload()).json();
  const path = "/api/v1/contact-imports/" + receipt;
  assert.equal((await request(path)).status, 401);
  assert.equal((await request(path, { headers: { authorization: "Bearer " + secondToken } })).status, 404);
  assert.equal((await (await request(path, { headers: user })).json()).status, "queued");
  assert.equal((await request(path + "?wait=21", { headers: user })).status, 400);
  const old = await (await request("/h/test/inbox/pull", { headers: box })).json();
  assert.deepEqual(old.messages, []);
  const compatible = await (await request("/h/test/inbox/pull?contact_uploads=1", { headers: box })).json();
  assert.equal(compatible.messages[0].source, "contact-upload");
  assert.deepEqual(compatible.messages[0].phonebooks, ["house"]);
  const ack = results => request("/h/test/inbox/ack", { method: "POST", headers: box, body: JSON.stringify({ ids: [receipt], ...(results ? { results } : {}) }) });
  await ack();
  assert.equal((await (await request(path, { headers: user })).json()).status, "queued");
  assert.equal((await ack([{ id: receipt, status: "saved", message: "private\u0000bad" }])).status, 400);
  const results = [{ id: receipt, status: "saved", message: "Saved 1 new number to the selected book." }];
  assert.equal((await ack(results)).status, 200);
  assert.equal((await (await request(path, { headers: user })).json()).status, "saved");
  await ack([{ id: receipt, status: "failed", message: "Should not replace the terminal result." }]);
  assert.equal((await (await request(path, { headers: user })).json()).status, "saved");
  env.CONTACT_UPLOAD_KEYS = "{}";
  assert.equal((await request(path, { headers: user })).status, 401);
});

test("malformed, oversized and excessive cards enqueue nothing, including streamed bodies", async t => {
  const { db, upload, request } = fixture(t);
  assert.equal((await upload("", {}, { method: "GET", body: undefined })).status, 405);
  assert.equal((await request("/api/v1/contact-imports?sender=spoof", { method: "POST", headers: { authorization: "Bearer " + token }, body: card })).status, 400);
  assert.equal((await upload(card, { "content-type": "application/json" })).status, 415);
  assert.equal((await upload(card, { "idempotency-key": "bad value" })).status, 400);
  assert.equal((await upload(card, { "content-length": String(MAX_MEDIA_BYTES + 1) })).status, 413);
  for (const data of ["", "private invalid card", "BEGIN:VCARD\nFN:no phone\nEND:VCARD", card.repeat(4), card + "BEGIN:VCARD\nFN:partial"]) {
    assert.equal((await upload(data)).status, 422);
  }
  const stream = new ReadableStream({ start(controller) {
    controller.enqueue(new Uint8Array(MAX_MEDIA_BYTES));
    controller.enqueue(new Uint8Array(1));
    controller.close();
  } });
  assert.equal((await upload(stream, {}, { duplex: "half" })).status, 413);
  assert.equal(db.prepare("SELECT count(*) AS n FROM messages").get().n, 0);
  const valid = await upload("\uFEFF" + card, { "content-type": "text/x-vcard; charset=utf-8" });
  assert.equal(valid.status, 202);
});

test("revocation and bad configuration fail closed with no private errors", async t => {
  const { db, upload, env, keys, logs } = fixture(t);
  delete keys[hash(token)];
  env.CONTACT_UPLOAD_KEYS = JSON.stringify(keys);
  assert.equal((await upload()).status, 401);
  keys[hash(token)] = { house: "test", sender: "spoof", phonebooks: ["house"] };
  env.CONTACT_UPLOAD_KEYS = JSON.stringify(keys);
  assert.equal((await upload()).status, 401);
  keys[hash(token)].sender = "+15125550101";
  env.CONTACT_UPLOAD_KEYS = JSON.stringify(keys);
  delete env.HOUSE_TEST_DID;
  assert.equal((await upload()).status, 503);
  env.CONTACT_UPLOAD_KEYS = "private-config-error";
  const broken = await upload();
  assert.equal(broken.status, 500);
  assert.doesNotMatch(await broken.text(), /private/);
  assert.doesNotMatch(JSON.stringify(logs), /private|ddc_|1512555/);
  assert.equal(db.prepare("SELECT count(*) AS n FROM messages").get().n, 0);
});

test("rate and pending limits apply across a person's keys, with retries still allowed", async t => {
  const { db, upload, env, keys } = fixture(t);
  keys[hash(secondToken)] = keys[hash(token)];
  env.CONTACT_UPLOAD_KEYS = JSON.stringify(keys);
  for (let i = 0; i < 30; i++) assert.equal((await upload(card, { "idempotency-key": String(i) })).status, 202);
  assert.equal((await upload(card, { "idempotency-key": "0" })).status, 202);
  assert.equal((await upload(card, { authorization: "Bearer " + secondToken })).status, 429);
  db.prepare("UPDATE messages SET received_at = ?").run("2000-01-01T00:00:00.000Z");
  assert.equal((await upload()).status, 429, "offline backlog is bounded even after the hourly limit expires");
  db.prepare("UPDATE messages SET acked_at = ?").run(new Date().toISOString());
  assert.equal((await upload()).status, 202);
});
