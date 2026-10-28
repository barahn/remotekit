# Choosing a relay by measured round trip, not by location

**Status:** Proposal for Phase 3 of [implementation-phases.md](implementation-phases.md). Two parts of it conflict with how `webrtc` and `rendezvous` are built today; §7 names the conflict rather than deciding around it.  
**Companion documents:** [decentralised-signalling.md](decentralised-signalling.md) §6 (Phase 3 fallback), [pluggable-transports.md](pluggable-transports.md)  
**Scope:** `webrtc`, `stream`, `rendezvous`. Everything marked *policy* is a consumer's and is listed in §8; none of it is decided here.

---

## 1. The question this answers

A session is two peers and, when they cannot reach each other directly, one
relay between them. A consumer that grows a volunteered relay fleet — Chirp
lists its public servers, relays among them, with an operator and a two-letter
`region` — only benefits from growth if a session uses a *near* relay. The
registry gives a region per relay and nothing per peer, so something has to
decide which relay is near which peer.

The obvious move is to geolocate each peer's address and match regions. This
document rejects that move, and says what replaces it.

Three forces are in play, and the first thing to settle is that they are not
one problem:

- **Latency.** Interactive remote control is unusable past roughly 80 ms of
  added delay, and the relay sits on the path twice.
- **Privacy.** A consumer may have no accounts, with two strangers meeting by a
  code. A design that derives a peer's city, or that lets whoever holds the
  registry observe who is working on whose machine, hands out something such a
  product otherwise never collects.
- **Cost.** Nothing in the deciding may require per-session infrastructure that
  the vendor runs and pays for, or the growth story is a cost curve.

The forces conflict less than they appear to, because *content* privacy and
*metadata* privacy are separable and have very different prices. Content
privacy is already bought: Phase 0 and Phase 1 terminate authentication and
encryption at the peers, so the carrier of the handshake and the carrier of the
media are both untrusted by construction. Metadata privacy against the chosen
relay, at this latency budget, is not available at any price this design is
willing to pay — and saying so plainly is part of the decision.

## 2. The decision

**Each peer measures candidate relays itself and offers the best few by id.
Nothing geolocates anyone, and no relay or registry holds a peer's address for
longer than a session needs it.**

1. **Candidate gathering.** On session start each peer takes the candidate
   relays its consumer supplies — `remotekit` reads no registry and holds no
   server list; the list is the caller's, as `PeerConfig.ICEServers` already is
   — and probes up to eight of them in parallel over UDP. A probe is a STUN
   binding request; the measure is the median of three round trips, with jitter
   kept as a tie-breaker. Budget: 250 ms, and whatever has answered by then is
   the set.
2. **Candidate exchange.** Each peer sends the other, over the existing
   signalling path (`stream.SignalConn`, carrying a signed
   `webrtc.SignalMessage`), its two or three best relay **ids** with the
   measured RTT — never its own address, and never a region or a coordinate. A
   relay id is a public name from a public list, so the exchange tells the other
   peer nothing it could not have read in that list.
3. **Path choice.** Both peers run the same deterministic rule over the union of
   the two sets: the relay minimising `rtt_a + rtt_b`, breaking ties by lower
   combined jitter, then by lexicographic id, so both sides land on the same
   answer without a round of negotiation. The chosen relay is what each peer
   configures as its TURN server for the session.
4. **Direct first, always.** The relay is a fallback, not the path. ICE already
   gathers host and server-reflexive candidates concurrently and prefers them to
   relayed ones, and it already migrates to a better pair mid-stream; this
   design adds nothing there and must not take anything away. Relay selection
   runs concurrently with ICE gathering so that it costs no wall-clock time when
   hole punching succeeds.
5. **The relay forwards and nothing more.** The peer-to-peer handshake
   terminates at the two peers, so a relay carries opaque datagrams. It learns
   the two addresses, the byte counts and the timing. It learns no key, no code,
   no content and no identity. A relay must not be usable as a general proxy:
   credentials are per-session and time-limited
   (`webrtc.GenerateEphemeralTURNCredentials`), which is what keeps the standing
   shared secret of Phase 3 from turning a volunteered relay into an open one.
6. **Operator diversity is the metadata defence.** Because each peer picks from
   the whole list by its own measurement, sessions spread across operators by
   construction. No single operator sees the network, and the one who serves a
   session sees one session.

## 3. What was rejected

### Geo-IP matching by region

A coordinator resolves each peer's address to a country and returns the relays
whose `region` matches.

| Dimension | Assessment |
|---|---|
| Complexity | Medium — a geo database to carry, update and be wrong with |
| Cost | A coordinator on the session path; a licensed dataset |
| Scalability | Poor — one more thing to run as the relay count grows |
| Accuracy | Poor — CGNAT, VPNs, mobile ASNs, and "same country" is not "near" |
| Privacy | Worst of the three: the coordinator holds address → location → pair |

It is cheap to describe and it uses a field the registry already has. It also
measures the wrong quantity, needs infrastructure this architecture spent three
phases removing, and collects the one thing a code-only product never collects.
A relay 400 km away across a bad peering path loses to one 2 000 km away across
a good one, and region matching cannot see that.

**Not even as a hint.** A hint that is usually right teaches the client to skip
the measurement, and then the failure mode is silent and seasonal. `region`
stays what it is: something for a human reading the list.

### Anycast front for the relay fleet

One address, with the network routing each peer to a near relay.

| Dimension | Assessment |
|---|---|
| Complexity | High |
| Cost | High — coordinated BGP, which volunteers do not have |
| Scalability | Breaks the model outright |
| Privacy | Good |

It needs no selection logic and it is genuinely fast. It is also incompatible
with a fleet of independent volunteers, which is the premise, and a stateful
relay behind anycast needs both peers to land on the *same* node — which anycast
gives only by luck.

### Measured RTT with forwarding-only relays — chosen

| Dimension | Assessment |
|---|---|
| Complexity | Medium — ICE-shaped, and a well-trodden shape |
| Cost | None beyond the relays themselves; no per-session infrastructure |
| Scalability | Improves as the list grows, with no operator action |
| Accuracy | Measures exactly the quantity that matters |
| Privacy | Content: strong. Metadata: distributed, not hidden |

No coordinator, no geo data, no address held anywhere; quality rises by itself
with each relay added; the peer decides, which is also where the trust belongs.
It costs up to 250 ms of probing before a relayed session starts, eight probes
per peer per session as background load on the fleet, and it cannot hide the
peer pair from the relay that was chosen.

## 4. The trade-off, stated plainly

**The real trilemma is latency, metadata privacy and zero per-session
infrastructure — and this design takes two of the three and half of the
remaining one.** Mixnets, cover traffic and constant-rate padding would hide the
pair from the relay, and each costs tens to hundreds of milliseconds. For
interactive control that is not a tuning question, it is a different product. So
what is bought is:

- **Content privacy outright.** The relay cannot read or alter a byte. This is
  the privacy that protects the screen, the keystrokes and the files, and the
  earlier phases already paid for it.
- **Metadata privacy by distribution rather than concealment.** One operator,
  one session, no history: retention is the defence, and it is a property
  operators can be held to and an outside monitor can check.
- **Honesty about the remainder.** The relay a session chose knows that address
  A and address B exchanged 400 MB for 20 minutes. Nothing in this design
  prevents that, and no document may imply otherwise.

**Reputation, not permission.** A relay cannot read traffic, so a hostile one
can only drop, delay or watch. All three are observable from outside, which is
what an external monitor already does — so a relay earns traffic by measured
uptime, and a client may require a minimum observation period before using one
as a fallback. Nothing a server says about itself counts.

## 5. What this changes in this repository

**Easier**

- Adding a relay helps immediately, with no registry surgery and no region map.
- No geo-IP dataset, no licence for one, and no coordinator on the path.
- A peer can refuse a relay, or an operator, for its own reasons.
- Session setup and privacy stop being coupled to anyone running a service.

**Harder**

- A relayed session starts up to 250 ms later than one that was told where to
  go. Mitigated by probing concurrently with signalling and ICE gathering.
- Relays absorb probe traffic from sessions they will not serve. This needs a
  rate limit per address at the relay and a probe that is cheap to answer.
- Two implementations must agree on the deterministic rule exactly, or the peers
  disagree on the path. The rule has to be specified as wire text, not
  described.
- `PeerConfig.ICEServers` is a flat list of URLs with one credential pair for
  all of them. Per-relay credentials and relay ids need either a richer config
  type or selection finishing before the `PeerSession` is built.
- The claim "we cannot see who talks to whom" becomes false for the relay
  operator, and the documentation must stop short of it.

## 6. Relation to the existing design documents

This document does not supersede either neighbour; it fills a gap both leave
open.

- [decentralised-signalling.md](decentralised-signalling.md) §6 decides *that*
  Phase 3 drops the Google STUN default and issues ephemeral HMAC TURN
  credentials, and leaves open *which* relay a session uses. That is the
  question answered here. Its §7 conclusion — that the relay path belongs to
  Chirp first and `remotekit`'s job is to make the path swappable and decline to
  choose for either product — governs §8 below, and is why the candidate list is
  the caller's.
- [pluggable-transports.md](pluggable-transports.md) separates *what to send*
  from *how to send it*, and places ICE negotiation deliberately outside
  `DataTransport`. Relay selection stays outside it too: it produces the
  addresses and credentials a transport is then constructed with. A QUIC or
  WireGuard transport reaching a relay would need the same selection and the
  same probe, with a transport-appropriate probe in place of the STUN binding
  request.

## 7. Where this conflicts with the code as built

Two parts of the design as first drafted do not fit what is in the tree. They
are recorded here rather than quietly reconciled.

**A custom allocation with slot cookies.** The draft described a relay holding
an allocation of two slots, one freshly minted cookie per slot, forwarding
between them and refusing to forward to any address a peer names — which is a
cleaner privacy story than TURN, because no peer ever names an address. The tree
goes the other way: `pion/turn/v5` is already in the graph, Phase 3 commits to
it, and `webrtc.GenerateEphemeralTURNCredentials` implements the TURN REST
convention. In TURN a peer *does* name the other's address, via
`CreatePermission` or `ChannelBind`. The design above is therefore written for
TURN, and the slot-cookie relay is left as an open question (§9), not adopted:
it would be a new relay protocol, a second thing for operators to run, and it
belongs in a document of its own if it is wanted.

**"Fetches the registry it already trusts."** There is no registry of relays in
`remotekit` and there must not be one. Package `rendezvous` is codes and links —
`Registry` there allocates and redeems short codes and has no notion of a
server, a region or an operator — and the server list is a consumer's data file.
Hence §2.1: the candidate list is supplied by the caller, exactly as
`PeerConfig.ICEServers` is today. Selection is mechanism; the list, and what
makes a relay eligible for it, are policy.

Neither conflict changes the decision in §2. If either is resolved differently,
this document is the thing to amend.

## 8. What a consumer must decide

Each of these is policy and none of it is implemented here. For Chirp they are
decisions for its own documentation:

- The defaults: how many relays to probe, the probe budget, and which registry
  entries are eligible (`active`, not `new`, minimum observed uptime).
- Whether the chooser, when offering a backup relay, skips a second relay from
  the same operator.
- An opt-in high-privacy mode that chains two relays from two different
  operators, so that neither sees both peers. It costs 30–60 ms, and it is the
  user's choice to spend rather than the architecture's choice to impose.
- Blinded rendezvous: a rendezvous server seeing a token derived from the code
  rather than the code, holding the pairing only until both peers have exchanged
  candidates, and never learning which relay was chosen.
- The operator commitment: no disk, no per-session logs, aggregate counters
  only.
- Whether `region` stays in the registry as human-facing only, stated where it
  is defined.
- The privacy page's wording, including, in plain words, what the chosen relay
  can see.

## 9. Open questions

- Whether 250 ms and eight probes are the right numbers, once there are enough
  relays for the choice to matter.
- Whether candidate exchange should be padded. It is small and structured, and a
  signalling server watching sizes may learn how many candidates a peer has.
- Whether the two-relay chain earns its latency in practice.
- What happens when the registry is unreachable. A cached list ages, and an aged
  list is a selection bias: peers keep choosing the relays that were listed when
  they last fetched, and a new operator gets no traffic from exactly the clients
  least able to notice.
- Whether a forwarding-only relay protocol with per-slot cookies is worth
  specifying as an alternative to TURN (§7), given that it removes the step
  where a peer names the other's address.
- Whether per-relay credentials belong in `PeerConfig` or in a selection result
  that precedes it.

## 10. Next steps

1. Specify the probe wire format and the deterministic path rule exactly, as the
   normative text both sides implement.
2. Decide how a selected relay's id and per-session credentials reach
   `webrtc.PeerSession`, given §5's constraint on `PeerConfig.ICEServers`.
3. Carry the relay ids and measured RTTs in the signed `SignalMessage`, with a
   signal type of their own, so the exchange is authenticated like the rest.
4. Measure: the added RTT of the chosen relay against the best available one,
   over real sessions, as the honest test of whether the rule works.
