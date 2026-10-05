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
  const urls = value.split(",").map((v) => v.trim());
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
        return new Response("media unavailable", { status: 502 });
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
  } catch { /* Never expose a signed media URL in an error. */ }
  return new Response("media unavailable", { status: 502 });
}
