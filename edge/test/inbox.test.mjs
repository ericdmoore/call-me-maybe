import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync, readdirSync } from "node:fs";
import { DatabaseSync } from "node:sqlite";
import worker from "../src/index.ts";

const auth = { authorization: "Bearer inbox-example" };
const mediaURL = "https://voip.ms/media.php?id=example-private-url";
const card = "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Jane Smith\r\nTEL:+15125550123\r\nPHOTO:private-photo\r\nEMAIL:private@example.invalid\r\nEND:VCARD\r\n";
function fixture(t) {
  const logs = [];
  t.mock.method(console, "log", entry => logs.push(entry));
  const db = new DatabaseSync(":memory:");
  t.after(() => db.close());
  db.exec(readFileSync(new URL("../schema.sql", import.meta.url), "utf8"));
  db.exec("INSERT INTO messages VALUES ('test', 'old', '2026-10-04', '15125550101', '15125550100', 'ping', NULL)");
  const migrations = new URL("../migrations/", import.meta.url);
  for (const name of readdirSync(migrations).filter(name => name.endsWith(".sql")).sort()) {
    db.exec(readFileSync(new URL(name, migrations), "utf8"));
  }
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
  const callback = (id, media = mediaURL, extra = {}) => request("/h/test/sms/callback-example?" + new URLSearchParams({ id, from: "15125550101", to: "15125550100", message: "Add to: house", media, ...extra }));
  const pull = async () => (await (await request("/h/test/inbox/pull", { headers: auth })).json()).messages;
  return { db, request, callback, pull, logs, env };
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
  assert.equal(captured.provider_timestamp, "");
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

test("carrier time survives storage and pull without replacing the Worker's receipt time", async (t) => {
  const { db, callback, pull } = fixture(t);
  // Neither a timezone nor an ISO conversion is inferred for the carrier.
  const timestamp = "2026-10-01 09:15:30";
  const before = Date.now();
  assert.equal((await callback("timed", "", { timestamp })).status, 200);
  const stored = db.prepare("SELECT * FROM messages WHERE id = 'timed'").get();
  assert.equal(stored.provider_timestamp, timestamp);
  assert.ok(Date.parse(stored.received_at) >= before);
  assert.ok(Date.parse(stored.received_at) <= Date.now());
  assert.equal((await pull()).find(m => m.id === "timed").provider_timestamp, timestamp);
  // Retries cannot rewrite either timestamp on an already accepted message.
  assert.equal((await callback("timed", "", { timestamp: "2026-10-02T09:15:30-05:00" })).status, 200);
  assert.deepEqual(db.prepare("SELECT * FROM messages WHERE id = 'timed'").get(), stored);
  const old = (await pull()).find(m => m.id === "old");
  assert.equal(old.provider_timestamp, "");
  assert.equal(old.received_at, "2026-10-04");
});

test("optional carrier metadata cannot block an otherwise valid message", async (t) => {
  const { callback, pull } = fixture(t);
  const values = [undefined, "", "{TIMESTAMP}", "x".repeat(65), "2026-10-05\nprivate", "2026-10-05\x00", "2026-10-05\x7f"];
  for (let i = 0; i < values.length; i++) {
    const extra = values[i] === undefined ? {} : { timestamp: values[i] };
    assert.equal((await callback(`optional-${i}`, "", extra)).status, 200);
  }
  for (const message of await pull()) assert.equal(message.provider_timestamp, "");
  const timestamp = "2026-10-05T09:15:30+05:30";
  assert.equal((await callback("offset", "", { timestamp })).status, 200);
  assert.equal((await pull()).find(m => m.id === "offset").provider_timestamp, timestamp);
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


test("callback diagnostics distinguish missing, empty, unexpanded and captured media", async t => {
  const { request, callback, logs } = fixture(t);
  t.mock.method(globalThis, "fetch", async () => new Response(card));
  const params = new URLSearchParams({ id: "missing", from: "15125550101", message: "Add to: house" });
  await request("/h/test/sms/callback-example?" + params);
  assert.equal(logs.at(-1).media_field, "missing");
  assert.equal(logs.at(-1).media_count, 0);
  await callback("empty", "");
  assert.equal(logs.at(-1).media_field, "empty");
  assert.equal((await callback("unexpanded", "{MEDIA}")).status, 400);
  assert.equal(logs.at(-1).media_field, "unexpanded");
  assert.equal(logs.at(-1).outcome, "invalid-media");
  await callback("good", mediaURL, { timestamp: "{TIMESTAMP}" });
  const accepted = logs.at(-1);
  assert.equal(accepted.outcome, "queued");
  assert.equal(accepted.media_field, "present");
  assert.equal(accepted.timestamp_field, "unexpanded");
  assert.equal(accepted.contact_count, 1);
  assert.match(accepted.message_ref, /^[a-f0-9]{24}$/);
  await callback("good");
  assert.equal(logs.at(-1).outcome, "duplicate");
  assert.equal(logs.at(-1).message_ref, accepted.message_ref);
  assert.notEqual(logs.at(-1).request_ref, accepted.request_ref);
});

test("late media on a duplicate is visible without rewriting an acknowledged message", async t => {
  const { callback, request, logs, db } = fixture(t);
  await callback("late", "");
  await request("/h/test/inbox/ack", { method: "POST", headers: auth, body: JSON.stringify({ ids: ["late"] }) });
  const before = db.prepare("SELECT * FROM messages WHERE id = 'late'").get();
  t.mock.method(globalThis, "fetch", async () => { throw new Error("must not fetch"); });
  assert.equal((await callback("late")).status, 200);
  assert.equal(logs.at(-1).outcome, "duplicate-with-new-media");
  assert.deepEqual(db.prepare("SELECT * FROM messages WHERE id = 'late'").get(), before);
});

test("diagnostics contain no contact data, callback credentials, media URLs or raw errors", async t => {
  const { request, callback, logs, env } = fixture(t);
  const fetched = t.mock.method(globalThis, "fetch", async () => new Response(card));
  await callback("private-message-id", mediaURL, { message: "private-caption", timestamp: "private-timestamp" });
  fetched.mock.mockImplementation(async () => { throw new Error(mediaURL + " private-exception"); });
  assert.equal((await callback("retry-private")).status, 503);
  assert.equal(logs.at(-1).outcome, "media-unavailable");
  fetched.mock.mockImplementation(async () => new Response("invalid card private-value"));
  await callback("bad-private");
  assert.equal(logs.at(-1).contact_error, "invalid-card");
  env.DB.prepare = () => { throw new Error("private-database-error " + mediaURL); };
  const failed = await callback("database-private");
  assert.equal(failed.status, 500);
  assert.equal(await failed.text(), "internal error");
  assert.equal(logs.at(-1).stage, "callback-lookup");
  assert.equal(logs.at(-1).outcome, "internal-error");
  await request("/h/test/inbox/pull?wait=0", { headers: auth });
  assert.equal(logs.at(-1).stage, "inbox-pull");
  await request("/h/test/sms/private-wrong-token?message=private-content");
  assert.equal(logs.at(-1).stage, "authorization");
  assert.equal(logs.at(-1).media_field, undefined);
  assert.doesNotMatch(JSON.stringify(logs), /private|callback-example|inbox-example|voip\.ms|Jane|Smith|1512555|BEGIN:VCARD/);
  assert.ok(logs.every(log => Number.isFinite(log.duration_ms) && log.status >= 200));
});

test("carrier diagnostic is scoped, read-only and exposes only counts", async t => {
  const { env, callback, request, db, logs } = fixture(t);
  env.HOUSE_TEST_DID = "15125550100";
  env.VOIPMS_API_PASSWORD = "private-secret";
  await callback("123", "");
  const before = db.prepare("SELECT * FROM messages").all();
  const calls = [];
  t.mock.method(globalThis, "fetch", async (url, options) => {
    assert.equal(new URL(url).origin + new URL(url).pathname, "https://voip.ms/api/v1/rest.php");
    assert.equal(options.method, "GET");
    assert.equal(options.redirect, "manual");
    const p = new URL(url).searchParams;
    assert.equal(p.get("api_password"), "private-secret");
    calls.push(p.get("method"));
    if (p.get("method") === "getMMS") {
      assert.equal(p.get("mms"), "123");
      assert.equal(p.get("did"), env.HOUSE_TEST_DID);
      return Response.json({status:"success",sms:[{id:"123",type:"1",did:"5125550100",contact:"+15125550101",message:"private-caption",col_media1:mediaURL,media:[mediaURL,"",""]}]});
    }
    assert.equal(p.get("method"), "getMediaMMS");
    return Response.json({status:"success",id:"123",media:{"1":mediaURL}});
  });
  assert.equal((await request("/h/test/inbox/diagnose?id=123")).status,401);
  assert.equal((await request("/h/other/inbox/diagnose?id=123", {headers:{authorization:"Bearer other-example"}})).status,404);
  assert.equal((await request("/h/test/inbox/diagnose?id=999", {headers:auth})).status,404);
  assert.equal((await request("/h/test/inbox/diagnose?id=bad", {headers:auth})).status,400);
  assert.equal(calls.length,0);
  const response = await request("/h/test/inbox/diagnose?id=123",{headers:auth});
  assert.equal(response.headers.get("cache-control"),"no-store");
  const result = await response.json();
  assert.deepEqual(result,{callback_media_count:0,lookup_status:"success",message_found:true,record_media_count:1,media_lookup_status:"success",retrievable_media_count:1});
  assert.deepEqual(calls,["getMMS","getMediaMMS"]);
  assert.deepEqual(db.prepare("SELECT * FROM messages").all(),before);
  assert.doesNotMatch(JSON.stringify([result,logs]),/private|media\.php|1512555|512555/);
});

test("carrier diagnostic rejects mismatches, bounds reads and suppresses raw failures", async t => {
  const {env,callback,request,logs}=fixture(t);
  env.HOUSE_TEST_DID="15125550100";
  await callback("123","");
  let body, calls=0;
  const mocked=t.mock.method(globalThis,"fetch",async()=>{calls++;return Response.json(body);});
  for(const mismatch of [{did:"15125550999"},{contact:"15125550999"},{id:"456"},{type:"0"}]) {
    body={status:"success",sms:[{id:"123",did:"15125550100",contact:"15125550101",type:"1",...mismatch}]};
    assert.equal((await(await request("/h/test/inbox/diagnose?id=123",{headers:auth})).json()).message_found,false);
  }
  assert.equal(calls,4);
  body={status:"private-carrier-error"};
  assert.equal((await(await request("/h/test/inbox/diagnose?id=123",{headers:auth})).json()).lookup_status,"carrier-error");
  for(const response of [new Response("x".repeat(65537))]) {
    mocked.mock.mockImplementation(async()=>response);
    const result=await request("/h/test/inbox/diagnose?id=123",{headers:auth});
    assert.equal(result.status,502);
    assert.equal((await result.json()).error,"carrier diagnostic unavailable");
  }
  mocked.mock.mockImplementation(async()=>new Response("private-http-error",{status:503}));
  const httpFailure=await(await request("/h/test/inbox/diagnose?id=123",{headers:auth})).json();
  assert.equal(httpFailure.lookup_status,"carrier-http-error");
  assert.equal(httpFailure.lookup_http_status,503);
  mocked.mock.mockImplementation(async()=>{throw new Error("private-password");});
  assert.equal((await(await request("/h/test/inbox/diagnose?id=123",{headers:auth})).json()).lookup_status,"carrier-network-error");
  assert.doesNotMatch(JSON.stringify(logs),/private|1512555/);
});

test("carrier diagnostic identifies an MMS with no retained media",async t=>{
  const {env,callback,request}=fixture(t);
  env.HOUSE_TEST_DID="15125550100";
  await callback("123","");
  t.mock.method(globalThis,"fetch",async(url)=>Response.json(new URL(url).searchParams.get("method")==="getMMS"?{status:"success",sms:[{id:"123",type:"1",did:"15125550100",contact:"15125550101",media:["","",""]}]}:{status:"success",id:"123",media:[]}));
  const result=await(await request("/h/test/inbox/diagnose?id=123",{headers:auth})).json();
  assert.equal(result.message_found,true);
  assert.equal(result.record_media_count,0);
  assert.equal(result.retrievable_media_count,0);
});
