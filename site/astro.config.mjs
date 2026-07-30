import { defineConfig } from 'astro/config';

export default defineConfig({
  site: 'https://callmemaybe.cc',

  // Static output, deliberately. Nothing on this site needs a server: the
  // keypad runs in the browser and the hardware table is build-time data.
  // Static is cheaper to host and cannot be down in a way that matters.
  //
  // Switching to SSR is `output: 'server'` plus an adapter — but which adapter
  // is a deployment decision (Cloudflare Workers vs a Node box), so it stays
  // open rather than guessed.
  output: 'static',

  build: {
    // Inline the stylesheet so a built page is one self-contained file. Useful
    // for previewing a design without a server, and it removes a round trip on
    // a site this small.
    inlineStylesheets: 'always',
  },
});
