# The four repositories

Fabrintek's remote-access work is split across four repositories with three
different licences. The split is not organisational tidiness: it is what lets
an AGPL product and a proprietary product share one implementation of the hard
parts without either one's licence reaching the other.

This page is the same in all four repositories, so that each one states its own
role and acknowledges the others. If you are reading it in one repository and
acting on another, read that one's copy too — the roles are reciprocal, and a
change that looks right from one side is often the arrow pointing the wrong way.

## The repositories

| Repository | Licence | Role |
|---|---|---|
| [remotekit](https://github.com/barahn/remotekit) | Apache-2.0 | The mechanism. Screen capture and VP8 encoding, input injection, clipboard, chunked file transfer, a WebRTC wrapper, a reverse tunnel, tray and OS-service installation. |
| [chirp](https://github.com/barahn/chirp) | AGPL-3.0-only | On-demand remote support. One binary, the same on both sides, with nothing to install and no account: it shows its own code and share link; whoever types someone's code controls that machine once its owner consents there. |
| [chirp-status](https://github.com/barahn/chirp-status) | AGPL-3.0-only | A hosted monitor of Chirp's public network: a Cloudflare Worker that probes the servers Chirp lists and serves the figures `status.usechirp.app` shows. Chirp keeps its own monitor, `cmd/chirp-status`, which anyone can run. |
| barahn (private) | Proprietary | Fabrintek's commercial platform: fleet management over standing agents, with tenancy, roles and persistence. |

**This repository is remotekit** — the Apache-2.0 mechanism beneath the other two.

## Dependencies point inwards only

```
        barahn  (proprietary)
          │  may depend on
          ├──────────────► chirp  (AGPL-3.0-only)
          │                  │  may depend on
          └──────────────────┴──► remotekit  (Apache-2.0)
```

- **remotekit depends on neither of the others.** It is the innermost layer.
- **Chirp may depend on remotekit.** Never on the platform.
- **The platform may depend on both.**
- **chirp-status depends on no other repository's code.** It reads Chirp's list
  of servers (`network/servers.json`) as data, over HTTPS, and Chirp's status
  page reads its API at run time. Neither imports the other.

A change that needs one of those arrows reversed is a design problem, not a
licensing exception. There is no approval that makes it acceptable, because the
constraint is not a policy — reversing an arrow either breaks a licence or
breaks the dual-licensing arrangement described in Chirp's
[LICENSING.md](https://github.com/barahn/chirp/blob/main/LICENSING.md).

## Why each licence is the one it is

**remotekit is permissive** so that both an AGPL project and a proprietary one
can build on it. That is its entire reason for existing as a separate
repository: Chirp and the platform have incompatible licences and could not
consume each other's code, so the mechanism both need lives where both can
reach it.

**Chirp is AGPL** because it is network-delivered software, and the AGPL is what
stops a hosted fork from taking the code without returning changes.

**chirp-status is AGPL** for the reasons Chirp is, and because a monitor's
figures are only as believable as its code is open: anyone can read how a
number is made, and run a monitor of their own to check it.

**The platform is proprietary**, and Fabrintek's ability to keep it that way
while using Chirp rests on Fabrintek owning all of Chirp — which is why Chirp
requires a CLA and why its contributions are currently closed. See Chirp's
[CONTRIBUTING.md](https://github.com/barahn/chirp/blob/main/CONTRIBUTING.md).

## Where does this change belong?

Work outwards from the innermost repository that can hold it.

**It belongs in remotekit** if it is mechanism: something that manipulates a
screen, a device, a codec, a socket or a file, and that would be just as useful
to a product with entirely different rules. remotekit carries no product
opinions — there is no user, tenant, role or session policy anywhere in it, and
nothing in it shells out to run commands on a caller's behalf. Features layer on
through extension points rather than being compiled in.

**It belongs in Chirp** if it is the ad-hoc support product: how a PIN is
generated and redeemed, how consent is asked for and recorded, what a session
may do and for how long, what the join page shows. Chirp's own commitments
constrain this — no feature gates, consent that cannot be bypassed, and
self-hosting with no restrictions.

**It belongs in chirp-status** if it is how the hosted monitor runs on
Cloudflare: its Worker, its Durable Object, and what the platform's limits ask
of them. How a figure is made, who is in the network and the rules for joining
it are Chirp's ([docs/status-page-proposal.md](https://github.com/barahn/chirp/blob/main/docs/status-page-proposal.md)):
Chirp's own monitor and chirp-status make each figure the same way, so a change
to how one is made goes into both.

**It belongs in the platform** if it presumes accounts, tenants, roles, fleets
or standing agents. Chirp has none of those and is not the place to introduce
them.

If a change seems to belong in two of them, it is usually one mechanism plus one
policy that have not been separated yet. Split it and the arrows resolve.

**A worked example: two strangers meeting by a code.** Allocating a short code,
minting a fresh one each time a share link is used, redeeming it, throttling
failed guesses and relaying the two peers' signalling would serve Barahn as well
as Chirp — so that is remotekit's. That each Chirp shows its code at start, that
the one who types it controls, how long a link lives, the limits, and the
consent prompt's wording are Chirp's. The full split is proposed in
Chirp's [docs/rendezvous-proposal.md](https://github.com/barahn/chirp/blob/main/docs/rendezvous-proposal.md).

## What this repository must not acquire

- A product opinion: a user, tenant, role, account, or session policy. A
  consumer's policy belongs behind an extension point, in the consumer.
- An import of Chirp or of the Barahn platform, at any depth: not a type, not
  a test helper.
- Code that shells out to run commands on a caller's behalf.
- A dependency whose licence is not on the list in
  [CONTRIBUTING.md](CONTRIBUTING.md), and vendored code without its
  attribution in [NOTICE](NOTICE).

---

`SPDX-License-Identifier: Apache-2.0`
Copyright (C) 2026 Fabrintek Engenharia Digital Ltda
