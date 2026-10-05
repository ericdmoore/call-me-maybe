# Positioning articles — editorial drafts

Research checked October 5, 2026. These compare intended use and responsibility,
not measured reliability or security. No competitor devices were tested.
Recheck current plans before publication. Each article should stand on its own.

## Draft 1: Tin Can and Call Me Maybe: who looks after the phone?

A child wants to call a friend. A parent wants to make that possible without
handing over a smartphone. Tin Can and Call Me Maybe both make sense in that
conversation. The useful question is how much of the phone system you want
to look after yourself.

Tin Can packages the device and service together. Its parent app manages
approved contacts in both directions and call hours. The US storefront lists
the phone at $100, free calls between Tin Cans, and a $9.99/month Party Line
plan for approved ordinary phone numbers. Both plans advertise 911 calling.
Those are meaningful conveniences for a family that wants a phone to set up
and use. [Tin Can product and plans](https://tincan.kids/products/tin-can),
[parent app](https://apps.apple.com/us/app/tin-can-companion-app/id6741686691).

Call Me Maybe is software you run on your own Asterisk host, often a Raspberry
Pi. You choose the handsets and carrier. An incoming call can ring a room,
move through a sequence of rooms, or reach voicemail. Handset curfews set a
bedtime; a Home Assistant integration can announce that someone is calling.
The appeal is being able to shape a household phone system around the way
that household actually works.

That flexibility comes with work. Someone owns the updates, configuration,
backups and carrier account. The software is free, but hardware, calling,
emergency service and upkeep are real costs. Your carrier still carries
outside calls; self-hosted does not mean carrier-free.

There is also a substantive parental-control difference. Call Me Maybe's
incoming people list is not an outgoing contact whitelist. Outside a curfew,
the supplied dialplan permits ordinary US/Canada-format numbers. A family
requiring approved-only dialing should give that requirement more weight
than an appealing feature list.

Choose Tin Can when the integrated family experience is what you want.
Consider Call Me Maybe when someone enjoys maintaining a small home system
and wants control over rooms, phones and routing. Both choices can begin
with the same good intention: giving a child more people to talk to.

## Draft 2: A neighborhood phone circle, or a household switchboard?

Sometimes the missing piece is not a phone. It is three other families whose
children can pick up when it rings.

Switchboard focuses on that neighborhood problem. Its site describes a
connector for an ordinary telephone, parent-approved contacts, quiet hours
and three-digit dialing within a private community. That can be an attractive
shape for a street or school group making a decision together. Verify current
availability, recurring charges and emergency-call behavior directly before
buying; this review did not establish those terms.
[Switchboard's own description](https://www.switchboard.kids/).

Call Me Maybe starts with a different unit: the household. You run a phone
system at home and connect it to a carrier for ordinary phone calls. Friends
do not need the same software. A number can ring the kitchen, a child's room
or an adult's handset according to rules you configure.

These are different kinds of coordination. A private circle makes a shared
community part of the product. A household system lets you choose your own
phones and routing, but leaves you responsible for setup and deciding how
friends will reach each other. Call Me Maybe's six-digit incoming extensions
are access credentials, not the same thing as a public three-digit friend
number. Its default outgoing dialplan is not a closed contact circle.

If your main task is getting several families onto one simple calling
network, investigate the community service first. If you want a phone system
that also serves the office, the kitchen and the grandparents calling an
ordinary number, Call Me Maybe is worth exploring. Neither goal needs to be
a criticism of the other.

## Draft 3: Ooma and Call Me Maybe: service convenience or local control?

There is a perfectly reasonable answer to “we miss having a home phone”:
buy a home phone service.

Ooma Telo connects a home telephone to an internet-based service. Ooma's Basic
plan advertises nationwide calling with monthly taxes and fees, after the
hardware purchase; additional features are available through other plans.
Use its location-specific tax calculator before treating “free service” as
a zero monthly bill. [Ooma Telo](https://www.ooma.com/home-phone-service/telo-base-station/),
[monthly charges](https://www.ooma.com/home-phone-service/faqs/monthly-fees-ooma-telo/).

That is useful if the job is simply having a familiar phone in the house,
with the service supplied by one company. A household does not need to
become a telecom hobby to deserve a working kitchen phone.

Call Me Maybe is for the household that wants to decide more: which carrier
to use, which handset rings first, what happens at bedtime, and whether the
lights or speakers announce an incoming call. Its software runs locally;
configuration files describe the rules. You can combine that flexibility
with an analog phone through a compatible adapter, or choose SIP handsets.

The price of that control includes your time. An open-source license does
not include a carrier subscription, installed hardware or someone on call
to maintain the system. Both internet-based approaches depend on their
power and network arrangements; Call Me Maybe should remain supplementary
to another route to emergency help.

Choose a managed home-phone service when the ordinary experience is the
point. Consider Call Me Maybe when making the phone fit your household is
part of the value. Familiarity and configurability are both legitimate things
to want.
