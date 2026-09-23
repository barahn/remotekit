// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package vp8

// Rate control.
//
// Before this existed, SetBitrate mapped a target rate onto a fixed quantiser
// and nothing ever looked at what came out. Asking for 8 Mbps on a 4K desktop
// produced 63 Mbps, and asking for less produced a different wrong number: the
// parameter named a bitrate and delivered a quality setting.
//
// What is here is a frame-level controller — one quantiser per frame, chosen
// from how far the stream has drifted from its budget. It is not libvpx's rate
// control: there is no macroblock-level adjustment, no two-pass analysis, and
// no lookahead. It does make the bitrate parameter mean what it says over a
// window of a second or so, which is the difference between a stream that can
// be sent over a link and one that cannot.

const (
	// rcBufferSeconds is how much the stream may run ahead of or behind its
	// budget before the controller is at full authority. One second is short
	// enough to react within a noticeable moment and long enough to absorb a
	// key frame without pinning the quantiser at its coarsest.
	rcBufferSeconds = 1.0

	// rcMaxStep bounds how far the quantiser may move between frames. Letting
	// it jump freely converges faster and pumps visibly, which on a screen
	// share reads as the picture breathing.
	rcMaxStep = 4

	// rcKeyFrameBudget is how many frame budgets a key frame is allowed to
	// spend. A full refresh is inherently several times an inter frame, and
	// charging it a single frame's worth would slam the quantiser coarse for
	// the second that follows.
	rcKeyFrameBudget = 10.0

	// rcMinQI is the finest quantiser the controller will choose. Below this
	// the bitrate climbs steeply for quality nobody sees on a desktop.
	rcMinQI = 4

	// rcMaxQI is the coarsest. VP8's quantiser index runs 0..127 — the header
	// field is seven bits and both lookup tables carry 128 entries — and this
	// encoder used only the finer half of it. At 63 the AC step is about 155;
	// at 127 it is 284, so half the compression range was simply unreachable,
	// which is why the controller could saturate and still miss its target by
	// an order of magnitude.
	rcMaxQI = 127
)

// updateRateControl records what a frame cost and picks the quantiser for the
// next one.
//
// The model is a leaky bucket: each frame adds what it spent and removes its
// budget, so the bucket holds the accumulated overshoot in bits. The quantiser
// is then set from how full that bucket is, proportionally, rather than from
// the last frame alone — a single large frame should not swing the quantiser if
// the stream as a whole is on budget.
func (e *Encoder) updateRateControl(frameBytes int, wasKeyFrame bool) {
	if !e.rateControl || e.fps <= 0 || e.bitrate <= 0 {
		return
	}

	budget := float64(e.bitrate) / float64(e.fps)
	if wasKeyFrame {
		budget *= rcKeyFrameBudget
	}

	e.rcBucket += float64(frameBytes*8) - budget

	capacity := float64(e.bitrate) * rcBufferSeconds
	if e.rcBucket > capacity {
		e.rcBucket = capacity
	}
	if e.rcBucket < -capacity {
		e.rcBucket = -capacity
	}

	// Fullness in [-1, 1]: positive means the stream owes bits and the
	// quantiser must coarsen.
	fullness := e.rcBucket / capacity
	want := e.rcBaseQI + int(fullness*float64(rcMaxQI-rcMinQI))

	if want > e.qi+rcMaxStep {
		want = e.qi + rcMaxStep
	}
	if want < e.qi-rcMaxStep {
		want = e.qi - rcMaxStep
	}
	if want < rcMinQI {
		want = rcMinQI
	}
	if want > rcMaxQI {
		want = rcMaxQI
	}
	e.qi = want
}

// SetRateControl turns the controller on or off.
//
// Off, the quantiser stays wherever SetBitrate or SetQuantizerIndex left it and
// the output size is whatever the content makes it — which is the right choice
// when something upstream is already choosing quantisers, and the wrong one for
// anything that has to fit down a link.
func (e *Encoder) SetRateControl(enabled bool) {
	e.rateControl = enabled
	e.rcBucket = 0
}
