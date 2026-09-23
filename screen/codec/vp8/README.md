# vp8 — forked VP8 encoder

Vendored from [`github.com/mendsec/vp8`](https://github.com/mendsec/vp8) at
commit `4967b3f`, our fork of
[`github.com/opd-ai/vp8`](https://github.com/opd-ai/vp8), MIT licensed.
Upstream's licence is kept verbatim in `LICENSE.upstream`; our own changes from
here on are Apache-2.0.

## Why the base is our fork and not upstream directly

The vendoring base was originally `opd-ai/vp8@49304f9`. That commit does not
decode correctly: it carries the inter-frame defects catalogued below, and the
fixes for them were offered upstream and have not been acted on.

| | |
|---|---|
| upstream's last commit | 2026-06-20 |
| upstream `main` CI | failing since 2026-06-20 |
| pull requests merged in upstream's history | 7, all by the maintainer's own agent, all same-day |
| pull requests merged from outside contributors | none |
| `opd-ai/vp8#9`, `#10` (the fixes below), `#11` (upstream's own CI) | open, no review, workflows never approved, so upstream CI has never run on them |
| `opd-ai/vp8#8`, the inter-frame bug report | open since 2026-09-06, no maintainer reply |

`mendsec/vp8@4967b3f` is those same two fixes merged into a fork we control, so
the tree this project vendors from descends from a state that decodes. The pull
requests stay open upstream: they cost nothing parked, and nothing here waits on
them.

Re-basing changes no licence obligation. The MIT terms require upstream's
copyright notice to travel with the code whatever the fork chain looks like, and
it does — verbatim in `LICENSE.upstream`, explained in `NOTICE`.

## Why a fork rather than a dependency

The changes this project needs are screen-content specialisations that are not
upstreamable as-is, the project has bus factor 1 and no external users, and a
codec on the critical path with an unvalidated inter-frame path is not something
to consume at arm's length.

Generic fixes found here are offered back upstream as pull requests. Forking and
contributing are not alternatives — upstream's merge velocity simply cannot sit
on this project's critical path. The table above is what that sentence has cost
in practice.

## What came across, and what did not

| | |
|---|---|
| 16 source files | all of them |
| 8 test files | 281 assertions, no debug printing |
| 35 `bpred_*_test.go` | **dropped** — 74 assertions against 203 print statements; this is print-debugging from a B_PRED saga, not a test suite |
| 2 `trace_*_test.go` | **dropped** — 4 assertions, both are tracing harnesses |
| `loop.log`, `test-output.txt` | **dropped** — build residue that was committed |

## Defects inherited from upstream, and what became of them

Upstream documented some of these itself in its `GAPS.md`; the rest surfaced
during the audit that led to the fork and the work that followed.

1. **Inter frames were never decode-validated.** Upstream's tests checked the
   frame-type bit and a minimum byte length; no decoder ever parsed the output.
   Run through libvpx, inter frames reconstructed at 8–13 dB — unrecognisable,
   and degrading across the sequence as the drift compounded.
2. **The mode and motion-vector layer did not implement the format.** This was
   the cause. Detailed below.
3. **The loop filter is advertised but inert** — `SetLoopFilterLevel` has an
   effect on the encoder's own reconstruction but the filter itself is a
   simplified one, and the bitstream signals the normal filter. Still open, and
   still harmless only because the level defaults to 0. Do not raise it.
4. **B_PRED is implemented and used** — key frames code every macroblock with
   it, which is why the key-frame path was exact from the start while the
   16x16-with-Y2 path went unexercised until inter frames arrived.

## Where the inter-frame fault was

Located by diffing the encoder's own reconstruction — the picture it stores as
the `last` reference — against libvpx's reconstruction of the same bitstream,
byte for byte. PSNR against the *source* cannot say which side of a closed loop
is wrong; this can. Key frames matched exactly; inter frames disagreed on
roughly 90% of every plane, which placed the fault in the first partition
(header, modes, motion vectors) rather than in residual coding, since residuals
travel in their own partition behind their own arithmetic decoder.

A word on how not to read that diff. An early run used a flat 128 chroma plane
and reported chroma matching exactly, which looked like proof the fault was
luma-only. It proved nothing: a flat plane reconstructs identically under any
motion vector and any prediction mode. The test source carries structured
chroma for that reason.

### The defects, and their fixes

| | |
|---|---|
| MV probability-update flags written with probability 128 instead of `vp8_mv_update_probs` (§17.2) | fixed |
| `intra_16x16_prob_update_flag` and `intra_chroma_prob_update_flag` omitted from the inter header (§9.11) | fixed |
| `mv_ref` coded from a static 3-entry table, where the format derives the probabilities per macroblock from the neighbouring vote counts (`vp8_mode_contexts`, §16.3) | fixed |
| the mode tree branching on NEARESTMV with 4 symbols, where the format branches on ZEROMV with 5 (SPLITMV is the fifth, and never emitting it does not shorten the tree) | fixed |
| motion-vector components written column-before-row, where the decoder reads row first — and the two components use different probability tables | fixed |
| the predictor derivation counting the above-RIGHT neighbour with a weighted-candidate scheme, where the format reads above, left and above-LEFT in a defined order (`vp8_find_near_mvs`) | fixed |
| a NEWMV delta coded against `nearest` rather than against the derived `best` | fixed |
| intra `y_mode` inside an inter frame coded with the key-frame *contextual* tree, whose shape differs from the flat `vp8_ymode_prob` tree inter frames use | fixed |
| `uv_mode` coded with the key-frame probabilities `{142, 114, 183}` in inter frames, which use `{162, 101, 204}` | fixed |
| B_PRED sub-modes inside an inter frame coded with the key frame's above/left contextual rows instead of the fixed `vp8_bmode_prob` row | fixed |
| the out-of-frame corner pixel answered as a neutral 128, where the row above the picture reads 127 and the column to its left reads 129 | fixed |
| a 16x16 macroblock contributing `B_DC_PRED` to its neighbours' sub-block context instead of its own mode | fixed |
| B_PRED reading its above-right pixels from its own reconstruction, and a context buffer too narrow to hold them at all | fixed |
| the Y2 non-zero context zeroed by macroblocks that have no Y2 block — B_PRED, and skipped macroblocks | fixed |
| `copy_buffer_to_golden` written unconditionally; §9.7 makes it present only when `refresh_golden_frame` is 0 | open, latent — bites once golden refresh is enabled |

### Where it stands

Measured through libvpx, with `TestEncoderReferenceMatchesDecoder` asserting the
first row and `TestInterFramesAreConformant` the second:

| | |
|---|---|
| encoder reference vs decoder reconstruction | **0 bytes differ, every plane, every frame** |
| key frame | 39.28 dB |
| inter frames 1–5 | 41.25, 41.15, 41.89, 41.98, 41.74 dB |

Inter frames landing above the key frame is the ordinary result: at the same
quantiser they code a smaller residual.

Byte equality is the assertion that matters, and PSNR is reported alongside it
rather than in place of it. VP8 prediction is closed-loop, so a single byte of
disagreement compounds into every frame that follows — a stream can measure well
on the first inter frame and still be diverging.

## The screen-content profile, and why it is off

The profile restricts inter macroblocks to `MV_ZERO` + skip + intra, removing
the motion search and the 6-tap interpolation filter. It was introduced on the
theory that the inter-frame corruption lived in `MV_NEW`, so designing the
defect class out looked cheaper than fixing it.

The theory was wrong. The corruption was in the mode and motion-vector layer,
catalogued above, and it is fixed. What remained was the assumption that
skipping the search is faster. Measured on a real 1080p desktop:

| | profile on | profile off |
|---|---|---|
| static desktop, few dirty macroblocks | 4.0 ms/frame | 3.2 ms/frame |
| full-screen scrolling | 99.7 ms/frame | 120 ms/frame |

In the common case it costs 25% more time and saves nothing, because the dirty
map has already removed the macroblocks a motion search would waste time on. In
the hard case it trades 19% more bits for 20% less time, and bits are what
crosses a network. `screen` therefore calls `SetScreenContentProfile(false)`.

The profile stays available. It is a reasonable switch for a link where the CPU
is the scarce resource, which is not the case this encoder was tuned for.

## Rate control

`SetRateControl(true)` makes the bitrate parameter mean what it says. Without
it the quantiser is fixed at whatever the target maps to and the output is
whatever the content makes it — a scrolling 1080p desktop asked for 8 Mbps
produced 43. See `ratecontrol.go`.

The quantiser range this operates over is the format's full `[4, 127]`. It used
to stop at 63, half of what VP8 allows, so the coarse end — the end a
constrained link needs — did not exist.


## Prediction is closed-loop

Each macroblock is analysed against the reconstruction of its neighbours and
reconstructed immediately, before the next one is analysed. Residuals are
therefore computed against the same prediction the decoder will add them to.

It was not always so, and the difference is not subtle. Predicting from the
SOURCE frame's neighbours means a residual cannot correct an error it was never
told about, so each macroblock passes a little more of it along. Following one
macroblock row of a real screen capture, on a source that is a constant 34, the
drift was one level per macroblock and monotonic: -14 at macroblock 40, -22 at
48, -30 at 56. H_PRED showed it worst, replicating the left column across
sixteen pixels; those macroblocks were 3% of the frame and 38% of its error.

Measured on a 1920x1080 capture, encoder reconstruction against the source:

| quantiser | open loop | closed loop | bytes (closed) |
|---|---|---|---|
| qi=0 | 39.0 dB | **60.3 dB** | 396160 |
| qi=8 | 38.9 dB | **53.1 dB** | 216294 |
| qi=24 | 32.8 dB | **45.9 dB** | 130604 |
| qi=48 | 30.2 dB | **40.9 dB** | 91854 |

Thirteen decibels at qi=24 for 3% more bytes. Open-loop quality barely responded
to the quantiser at all — 0.1 dB from qi=0 to qi=8, where the step doubles —
because quantisation was never what limited it.

### What it costs

A macroblock cannot be analysed until its neighbours are reconstructed, so the
intra analysis pass is serial. The row-parallel key-frame pass that gave a 2.6x
speedup depended on the open loop, and is gone with it:

| | parallel, open loop | serial, closed loop |
|---|---|---|
| key frame, 1080p, end to end | 55 ms | 127 ms |
| inter frame, steady state | 8.0 ms | 8.6 ms |

The steady state barely moves, which is the number that matters for a 30fps
stream: 8.6 ms against a 33.3 ms budget. Key frames cost what they cost, and a
periodic full refresh is a visible hitch again — roughly four dropped frames
every ten seconds at a 300-frame interval.

Getting the speed back means wavefront parallelism, where a macroblock starts as
soon as its above-right neighbour is done rather than waiting for the whole row.
That is a real piece of work and nobody has done it.

## Where it stands, measured on a real screen

A 1920x1080 X11 desktop, captured and encoded through the production path, then
decoded by libvpx:

| | |
|---|---|
| encoder reference vs decoder reconstruction | **0 macroblocks diverge** |
| decoder reconstruction vs source | **43.29 dB**, max \|delta\| 24 |

Before the last three fixes the same capture measured 12.41 dB with 1.6 million
of 2.1 million luma bytes wrong. The remaining difference from the source is
quantisation, which is what it is supposed to be.

Every case in `TestConformanceAcrossContentAndSize` passes: flat and textured
content, 64x64 through 1280x720, key frames and inter frames. Nothing in this
package is gated behind a known-defect flag any more.
