import test from "node:test";
import assert from "node:assert/strict";
import { carrierMediaURL, mediaList, readMedia, MAX_MEDIA_BYTES } from "../src/media.ts";

test("only bounded carrier media lists are accepted", () => {
  assert.equal(mediaList("").length, 0);
  assert.equal(mediaList("https://voip.ms/media.php?id=example").length, 1);
  for (const u of ["http://voip.ms/a", "https://voip.ms.evil.invalid/a", "https://127.0.0.1/a", "https://user:secret@voip.ms/a", "https://voip.ms:8443/a"]) {
    assert.equal(carrierMediaURL(u), null);
    assert.throws(() => mediaList(u));
  }
  assert.throws(() => mediaList(Array(4).fill("https://voip.ms/a").join(",")));
});

test("a media URL longer than 4096 characters is refused at the boundary", () => {
  const atLimit = "https://voip.ms/media.php?" + "a".repeat(4070);
  assert.equal(atLimit.length, 4096);
  assert.equal(mediaList(atLimit).length, 1);
  assert.throws(() => mediaList(atLimit + "a"), /invalid media list/);
});

test("media redirects cannot escape the carrier and do not carry inbox credentials", async (t) => {
  const calls = [];
  t.mock.method(globalThis, "fetch", async (url, opts) => {
    calls.push([url.toString(), opts]);
    return new Response(null, { status: 302, headers: { location: "https://127.0.0.1/private" } });
  });
  assert.equal((await readMedia("https://voip.ms/media.php?id=secret")).status, 422);
  assert.equal(calls.length, 1);
  assert.equal(calls[0][1].redirect, "manual");
  assert.equal(calls[0][1].headers, undefined);
});

test("media is bounded even without a Content-Length header", async (t) => {
  t.mock.method(globalThis, "fetch", async () => new Response(new Uint8Array(MAX_MEDIA_BYTES + 1)));
  assert.equal((await readMedia("https://voip.ms/a")).status, 413);
});

test("vCard bytes are preserved and URL errors are hidden", async (t) => {
  const card = "BEGIN:VCARD\r\nFN:Jane\r\nTEL:+15125550123\r\nEND:VCARD\r\n";
  const mock = t.mock.method(globalThis, "fetch", async () => new Response(card));
  const response = await readMedia("https://voip.ms/a");
  assert.equal(await response.text(), card);
  assert.equal(response.headers.get("cache-control"), "no-store");
  mock.mock.mockImplementation(async () => { throw new Error("signed-url-secret"); });
  const failed = await readMedia("https://voip.ms/a");
  assert.equal(failed.status, 502);
  assert.ok(!(await failed.text()).includes("secret"));
});
