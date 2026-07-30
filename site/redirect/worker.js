/**
 * dialdoorman.cc → callmemaybe.cc
 *
 * A Cloudflare Redirect Rule would be the better tool for this: it runs in the
 * edge proxy before any compute, and costs nothing. It needs the `Zone →
 * Dynamic Redirect → Edit` API permission, which the deploy credentials do not
 * carry — so this is the same behaviour implemented with the scopes we have
 * (Workers Scripts → Edit, plus DNS → Edit for the custom domain attach).
 *
 * It is a *separate* Worker from the site on purpose. The site is
 * assets-only, and Workers static assets serve a matching file before invoking
 * any script — so putting this redirect in the site's Worker would have meant
 * setting `run_worker_first`, paying a JS invocation on every request to
 * callmemaybe.cc in order to redirect a spare domain. Two Workers, each doing
 * one thing, is the cheaper shape.
 *
 * If the Dynamic Redirect permission is ever added, replace this with a
 * Redirect Rule and delete the Worker — see site/readme.md for the rule.
 */
export default {
  /**
   * @param {Request} request
   * @returns {Response}
   */
  fetch(request) {
    const url = new URL(request.url);

    // Keep the path and query, drop the host. A visitor who bookmarked
    // dialdoorman.cc/#hardware lands in the right place — the fragment never
    // reaches the server, so the browser reapplies it after the redirect.
    url.protocol = 'https:';
    url.hostname = 'callmemaybe.cc';
    url.port = '';

    // 301 rather than 302: this is permanent, and the whole point of
    // redirecting instead of mirroring is to consolidate the canonical signal
    // onto one hostname. Matches the <link rel="canonical"> the site emits.
    return Response.redirect(url.toString(), 301);
  },
};
