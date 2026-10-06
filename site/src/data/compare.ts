// What each phone can do, side by side. Call Me Maybe against Tin Can,
// because that is the comparison a family shopping for a kid's phone makes.
//
// Every Tin Can cell comes from its product page or FAQ, checked on the date
// below; a feature those pages do not mention is marked absent, which is the
// only honest reading of a product we have not used. The Call Me Maybe cells
// link to the page or document that describes the feature, and say "partly"
// where the stock configuration does less than a reader might assume.
//
// The fun rows come first on purpose. Cost is a tiebreaker; this is the
// argument.

export const tinCanCheckedAt = 'October 6, 2026';

/**
 * `planned` is on the roadmap and not in the binary; the page says so.
 * `zero` is a yes worth celebrating: the row counts something neither
 * phone lets happen.
 */
export type Mark = 'yes' | 'zero' | 'partly' | 'planned' | 'no';

export interface Side {
  mark: Mark;
  note: string;
}

export interface Row {
  feature: string;
  /** A site path or an absolute URL; the feature name links to it. */
  href?: string;
  cmm: Side;
  tinCan: Side;
}

const repo = 'https://github.com/ericdmoore/call-me-maybe';

export const rows: Row[] = [
  {
    feature: 'Spammers',
    cmm: { mark: 'zero', note: 'Only delight when the phone rings, zero dread. The people you list ring the house; everyone else meets the doorman, dials an extension or hears “Good day.”' },
    tinCan: { mark: 'zero', note: 'Only delight when the phone rings, zero dread. Nobody but the contacts approved in the parent app can ring it at all.' },
  },
  {
    feature: 'Room to room',
    href: `${repo}/blob/main/examples/handsets.example.toml`,
    cmm: { mark: 'yes', note: 'Dial 101 for the kitchen. The call never leaves the house and never touches the carrier.' },
    tinCan: { mark: 'partly', note: 'Two Tin Cans with their own numbers can call each other through Tin Can’s service. Linked to one number, they cannot.' },
  },
  {
    feature: 'Home paging',
    href: '/features/paging/',
    cmm: { mark: 'yes', note: 'Dial 500 and every handset opens at once. Dinner is ready without shouting up the stairs.' },
    tinCan: { mark: 'no', note: 'Not mentioned.' },
  },
  {
    feature: 'Book Buddy',
    cmm: { mark: 'planned', note: 'An audiobook read down the phone. Pick up in two rooms and hear the same chapter together; later, a friend calls in for a chapter.' },
    tinCan: { mark: 'no', note: 'Not mentioned.' },
  },
  {
    feature: 'Stories the child steers',
    href: `${repo}/blob/main/docs/STORY-PACKS.md`,
    cmm: { mark: 'planned', note: 'A story the keypad steers. The format is published and public domain, doorman builds the audio and the engine that tells it is written; the extension that puts a call through to it is next.' },
    tinCan: { mark: 'no', note: 'Not mentioned.' },
  },
  {
    feature: 'Open the garage',
    href: `${repo}/blob/main/docs/RUNBOOK.md#actions`,
    cmm: { mark: 'yes', note: 'Text “garage” to the house number. Home Assistant opens it, only for the people you list, and the house texts back that it did.' },
    tinCan: { mark: 'no', note: 'Not mentioned.' },
  },
  {
    feature: 'Ears for Home Assistant',
    href: '/features/home-assistant/',
    cmm: { mark: 'yes', note: 'Dial 555 and tell the house what to do: “lock up the house”. Home Assistant’s Assist hears it through your own speech-to-text, does it, and answers. Nothing is listening until you dial. Optional, and off until you turn it on.' },
    tinCan: { mark: 'no', note: 'Not mentioned.' },
  },
  {
    feature: 'The house announces a caller',
    href: '/features/home-assistant/',
    cmm: { mark: 'yes', note: 'A webhook fires as the house starts ringing. Home Assistant says “call from Grandma” on a speaker, or flashes a lamp.' },
    tinCan: { mark: 'no', note: 'Not mentioned.' },
  },
  {
    feature: 'Ringer ladders',
    href: '/features/ringer-ladders/',
    cmm: { mark: 'yes', note: 'Ring the kids’ room first, the adults after three cycles, voicemail after that. Per extension.' },
    tinCan: { mark: 'partly', note: 'Tin Cans linked to one number all ring at once. There is no order.' },
  },
  {
    feature: 'Call parking',
    href: '/features/call-parking/',
    cmm: { mark: 'yes', note: 'Park a call on 700 in the kitchen, pick it up in the office.' },
    tinCan: { mark: 'no', note: 'Not mentioned.' },
  },
  {
    feature: 'Family conference',
    href: '/features/conference/',
    cmm: { mark: 'yes', note: 'Dial 600 and everyone who joins is in the same room, an outside caller included.' },
    tinCan: { mark: 'no', note: 'Not mentioned.' },
  },
  {
    feature: 'Busy lamps',
    href: '/features/busy-lamps/',
    cmm: { mark: 'yes', note: 'The lamp on your handset says the office is already on a call before you pick up.' },
    tinCan: { mark: 'no', note: 'Not mentioned.' },
  },
  {
    feature: 'The phone calls you back',
    href: `${repo}/blob/main/docs/RUNBOOK.md#scheduled-calls-80-81-82`,
    cmm: { mark: 'yes', note: 'Dial *80, say a time and leave a message. At 7:30 the phone rings and plays it back to you.' },
    tinCan: { mark: 'no', note: 'Not mentioned.' },
  },
  {
    feature: 'A swappable bouncer',
    href: `${repo}/blob/main/docs/PACKS.md`,
    cmm: { mark: 'yes', note: 'The stranger’s greeting is a folder of audio. Swap the pack and a Victorian doorman or a ship’s computer answers instead.' },
    tinCan: { mark: 'no', note: 'Strangers cannot call at all, which is its own kind of answer.' },
  },
  {
    feature: 'Any handset',
    href: '/#hardware',
    cmm: { mark: 'yes', note: 'Any phone that registers over SIP: cordless, desk, or a rotary through an adapter. One in every room.' },
    tinCan: { mark: 'no', note: 'One handset, theirs, on 2.4 GHz Wi-Fi.' },
  },
  {
    feature: 'A bedtime for the phone',
    href: '/features/bedtime/',
    cmm: { mark: 'yes', note: 'A curfew on the handset pauses ordinary calls in both directions. Emergency calls and reminders still go through.' },
    tinCan: { mark: 'yes', note: 'Quiet hours in the parent app disable incoming and outgoing calls.' },
  },
  {
    feature: 'Voicemail',
    href: '/features/voicemail/',
    cmm: { mark: 'yes', note: 'Per mailbox, delivered to email, with a lamp on the handset.' },
    tinCan: { mark: 'yes', note: 'On the phone, with a custom greeting; messages are kept and played back there.' },
  },
  {
    feature: 'Approved contacts only',
    href: '/features/contacts/',
    cmm: { mark: 'partly', note: 'Incoming, yes: the people you list ring through and everyone else meets the lobby. Outgoing, no: the stock dialplan allows ordinary numbers.' },
    tinCan: { mark: 'yes', note: 'Only contacts approved in the parent app can call or be called.' },
  },
  {
    feature: 'Emergency calls',
    href: '/providers/',
    cmm: { mark: 'yes', note: '911 leaves by the trunk whose street address is filed. You configure and verify it with the provider.' },
    tinCan: { mark: 'yes', note: '911 on both plans, with the address set in the app.' },
  },
];

/** Feature slugs the rows link to; the build checks each page exists. */
export const linkedFeatures = rows
  .map((r) => r.href)
  .filter((h): h is string => !!h && h.startsWith('/features/'))
  .map((h) => h.replace(/^\/features\/|\/$/g, ''));
