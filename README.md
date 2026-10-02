# remotekit

Go building blocks for remote desktop sessions: screen capture and VP8 encoding,
input injection, clipboard sync, chunked file transfer, a WebRTC wrapper, a
WSS/Yamux reverse tunnel, a system tray and OS service installation — each
usable on its own.

[![License](https://img.shields.io/badge/License-Apache--2.0-green.svg?style=flat-square)](LICENSE)

```
go get github.com/barahn/remotekit
```

## Why this exists

This is the shared core behind two products with incompatible licences —
[Chirp](https://github.com/barahn/chirp) (AGPL-3.0, on-demand remote support)
and Barahn (proprietary fleet management). Neither could consume the other's
code, so the mechanism both need lives here under a permissive licence.

That constraint shaped the API: these packages carry no product opinions. There
is no notion of a user, a tenant, a role, or a session policy anywhere in this
module, and nothing here shells out to run commands on your behalf. Features
layered on top register through extension points such as
`tunnel.AgentStreamRunner.Handle` rather than being compiled in.

## Packages

| Package | What it does |
|---|---|
| `screen` | Screen capture (X11, Wayland, Windows, macOS), damage detection, YUV conversion, VP8 encoding |
| `screen/codec/vp8` | Pure-Go VP8 encoder — real bitstream, verified byte-exact against libvpx |
| `screen/codec/ivf` | IVF container writer, for dumping streams to disk |
| `input` | Keyboard and mouse injection |
| `clipboard` | Cross-platform clipboard read/write with a change watcher |
| `transfer` | Chunked file transfer with resume |
| `webrtc` | Peer session, video track and signalling helpers over pion |
| `tunnel` | WSS + Yamux reverse tunnel, agent enrolment, multiplexed streams |
| `bark` | The wire envelope shared by everything above |
| `heartbeat` | Generic keepalive with server-side staleness monitoring |
| `rendezvous` | Two peers that have never met find each other by a short single-use code, or a link that mints one; failed attempts throttled per origin |
| `tray` | System tray icon and menu |
| `service` | Install and manage an OS service (systemd, launchd, SCM) |
| `osinfo` | Host details |

## Platform support

Linux (X11 and Wayland), Windows and macOS. Capture, input and tray are
per-platform implementations behind one interface; macOS support is the least
exercised of the three.

## The VP8 encoder

`screen/codec/vp8` is a pure-Go VP8 encoder vendored from
[mendsec/vp8](https://github.com/mendsec/vp8), our fork of
[opd-ai/vp8](https://github.com/opd-ai/vp8). Its output decodes in libvpx, and
its internal reconstruction matches libvpx's byte for byte on every frame of the
conformance suite. A headless Firefox also decodes it over a real WebRTC
connection — `TestFirefoxDecodesTheStream` in `tunnel`, which CI runs with
`BARAHN_BROWSER_TEST=1` — which is the only check that covers the RTP
depacketizer and jitter buffer sitting between the wire and the decoder. The
verdict there is `getStats().framesDecoded`; what a browser paints from those
frames is still nobody's measurement.

The fork is Apache-2.0; upstream's MIT terms are preserved in
`screen/codec/vp8/LICENSE.upstream` and explained in `screen/codec/vp8/NOTICE`.
Generic fixes should be offered back to upstream under its own terms.

## Security

Report vulnerabilities privately to
[security@mendsec.com](mailto:security@mendsec.com), not in a public issue — see
[SECURITY.md](SECURITY.md). Every push and pull request runs gosec,
govulncheck, gitleaks, a dependency licence gate and the NOTICE check, with
every action pinned to a commit SHA; CodeQL and dependency review are wired in
and turn on when the repository has code scanning available, and Aikido Security
provides unified SAST, dependency and secret scanning. What each gate is
for is in [docs/devsecops.md](docs/devsecops.md).

## Licence

Apache License 2.0 — see [LICENSE](LICENSE). Vendored third-party code retains
its upstream licence, recorded alongside it.
