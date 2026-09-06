# vp8 — forked VP8 encoder

Vendored from [`github.com/opd-ai/vp8`](https://github.com/opd-ai/vp8) at commit
`49304f9`, MIT licensed. Upstream's licence is kept verbatim in
`LICENSE.upstream`; our own changes from here on are Apache-2.0, per
[ADR-0005](../../../../docs/adr/0005-build-images-without-the-host-podman-socket.md).

## Why a fork rather than a dependency

Recorded in ADR-0005. In short: the changes this project needs are
screen-content specialisations that are not upstreamable as-is, the project has
bus factor 1 and no external users, and a codec on the critical path with an
unvalidated inter-frame path is not something to consume at arm's length.

Generic fixes found here are offered back upstream as pull requests. Forking and
contributing are not alternatives — upstream's merge velocity simply cannot sit
on this project's critical path.

## What came across, and what did not

| | |
|---|---|
| 16 source files | all of them |
| 8 test files | 281 assertions, no debug printing |
| 35 `bpred_*_test.go` | **dropped** — 74 assertions against 203 print statements; this is print-debugging from a B_PRED saga, not a test suite |
| 2 `trace_*_test.go` | **dropped** — 4 assertions, both are tracing harnesses |
| `loop.log`, `test-output.txt` | **dropped** — build residue that was committed |

## Known defects inherited from upstream

Documented by upstream itself in its `GAPS.md`, and confirmed during the audit
that led to the fork:

1. **Inter frames were never decode-validated.** Upstream's tests check the
   frame-type bit and a minimum byte length; no decoder ever parsed the output.
2. **MV prediction deviates from RFC 6386 §18.2.** `findNearestMV` uses a
   weighted candidate count whose tie-breaking may not match the decoder. Since
   `MV_NEW` encodes only a delta from the *decoder-derived* predictor, a
   mismatch reconstructs a different motion vector.
3. **The loop filter is advertised but inert** — `SetLoopFilterLevel` has no
   effect; the bitstream always encodes level 0.
4. **B_PRED is implemented but disabled.**

Defects 1 and 2 are the reason `pkg/screen/codec/vp8check` — a `vpxdec`-backed
conformance oracle — was built *before* this fork landed. Run through libvpx,
the encoder splits cleanly in half:

| | luma PSNR |
|---|---|
| key frame 0 | **39.28 dB** |
| inter frame 1 | 11.55 dB |
| inter frame 2 | 11.20 dB |
| inter frame 3 | 7.84 dB |
| inter frame 4 | 10.43 dB |
| inter frame 5 | 12.13 dB |

Key frames are sound. Inter frames are not slightly off — they are
unrecognisable, and they degrade across the sequence, which is what compounding
reconstruction drift looks like. `TestInterFramesAreBrokenUpstream` asserts that
state on purpose, and fails loudly if it ever stops being true.

`golangci-lint` corroborates three of the four defects without being asked:
`computeFilterLimit` and `filterPlane` are unreachable, which *is* the inert
loop filter; `copyLastToAltRef` is unreachable, which is "only the last
reference is used"; and upstream's own tests carry two tautological assertions
(`SA4003`) that can never fail. The package is excluded from `unused` and
`staticcheck` in `.golangci.yml` while it is kept verbatim, with that exclusion
scoped to this directory and removed when the dead paths are deleted rather than
merely unreachable.

`gosec` is excluded for the same directory and the same reason: 53 `G115`
findings, all deliberate `int` -> `int16` narrowing in the forward DCT and
quantiser, where the VP8 spec bounds the values by construction. Fifty-three
`#nosec` annotations would destroy the verbatim baseline this fork needs.

## The profile this project actually needs

Screen content, not camera video: restrict inter macroblocks to `MV_ZERO` +
skip + intra. That removes motion search and the 6-tap interpolation filter,
and it never enters `MV_NEW` — where defects 1 and 2 both live. Dropping motion
estimation is not a concession here; it designs the defect class out.

Not done yet. This commit is the vendoring only.
