export const MAX_MEDIA_BYTES = 1300 * 1024;

// Never turn a carrier callback into an arbitrary URL fetch. Only allow
// the carrier's own domain; another CDN would need explicit verification.
export function carrierMediaURL(raw: string): URL | null {
  try {
    const u = new URL(raw);
    if (u.protocol !== "https:" || u.username || u.password || u.port || u.hash) return null;
    if (u.hostname !== "voip.ms" && !u.hostname.endsWith(".voip.ms")) return null;
    return u;
  } catch { return null; }
}

export function mediaList(value: string | null): string[] {
  if (!value?.trim()) return [];
  // The carrier's own records show three fixed slots, the unused ones empty;
  // an empty slot in the callback is not a bad URL.
  const urls = value.split(",").map((v) => v.trim()).filter(Boolean);
  if (urls.length > 3 || urls.some((u) => u.length > 4096 || !carrierMediaURL(u))) {
    throw new Error("invalid media list");
  }
  return urls;
}

// Bounded, even when a server omits Content-Length or compresses its body.
export async function readMedia(raw: string): Promise<Response> {
  let u = carrierMediaURL(raw);
  const signal = AbortSignal.timeout(15000);
  try {
    for (let redirects = 0; redirects <= 3; redirects++) {
      if (!u) return new Response("unsupported media host", { status: 422 });
      const response = await fetch(u, { redirect: "manual", signal });
      if ([301, 302, 303, 307, 308].includes(response.status)) {
        const next = response.headers.get("location");
        await response.body?.cancel();
        u = next ? carrierMediaURL(new URL(next, u).toString()) : null;
        continue;
      }
      if (!response.ok || !response.body) {
        await response.body?.cancel();
        // A 4xx from the carrier's media host is final: the signed URL has
        // expired or the file is gone, and no retry of this callback changes
        // that. 408 and 429 are the two 4xx that mean "try again later".
        const gone = response.status >= 400 && response.status < 500 && response.status !== 408 && response.status !== 429;
        return new Response(gone ? "media gone" : "media unavailable", { status: gone ? 410 : 502 });
      }
      const reader = response.body.getReader();
      const chunks: Uint8Array[] = [];
      let size = 0;
      for (;;) {
        const { value, done } = await reader.read();
        if (done) break;
        size += value.byteLength;
        if (size > MAX_MEDIA_BYTES) {
          await reader.cancel();
          return new Response("media too large", { status: 413 });
        }
        chunks.push(value);
      }
      const bytes = new Uint8Array(size);
      let at = 0;
      for (const chunk of chunks) { bytes.set(chunk, at); at += chunk.byteLength; }
      return new Response(bytes, { headers: { "content-type": "application/octet-stream", "cache-control": "no-store" } });
    }
    // A redirect chain this long is the host's configuration, not a blip.
    return new Response("too many redirects", { status: 422 });
  } catch { /* Never expose a signed media URL in an error. */ }
  return new Response("media unavailable", { status: 502 });
}
