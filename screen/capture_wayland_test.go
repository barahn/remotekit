// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

//go:build linux

package screen

import (
	"context"
	"testing"

	"github.com/godbus/dbus/v5"
)

// TestParseStreamsVariant covers the shape xdg-desktop-portal actually returns.
//
// The portal answers Start with `streams` of signature a(ua{sv}). godbus
// represents a struct as []interface{}, so an array of them arrives as
// [][]interface{}. An earlier type switch matched [][2]interface{} and
// []interface{} instead, matched neither, and left nodeID at zero after a
// successful Start - the capture then failed with "Start returned no PipeWire
// node id" while the portal had done its part.
func TestParseStreamsVariant(t *testing.T) {
	cases := []struct {
		name  string
		value interface{}
	}{
		{
			name: "godbus struct array",
			value: [][]interface{}{
				{uint32(42), map[string]dbus.Variant{
					"size": dbus.MakeVariant([]interface{}{int32(1280), int32(800)}),
				}},
			},
		},
		{
			name: "generic interface slice",
			value: []interface{}{
				[]interface{}{uint32(42), map[string]dbus.Variant{}},
			},
		},
		{
			name: "fixed-size pair array",
			value: [][2]interface{}{
				{uint32(42), map[string]dbus.Variant{}},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &waylandCapturer{}
			c.parseStreamsVariant(dbus.MakeVariant(tc.value))
			if c.nodeID != 42 {
				t.Errorf("expected node id 42, got %d", c.nodeID)
			}
		})
	}
}

func TestParseStreamsVariantRejectsGarbage(t *testing.T) {
	// nil is absent on purpose: dbus.MakeVariant(nil) panics inside godbus, so
	// a nil streams value can never reach this parser.
	for _, value := range []interface{}{
		"not a stream list",
		[][]interface{}{},
		[][]interface{}{{"missing the node id"}},
	} {
		c := &waylandCapturer{}
		c.parseStreamsVariant(dbus.MakeVariant(value))
		if c.nodeID != 0 {
			t.Errorf("value %#v should not have produced a node id, got %d", value, c.nodeID)
		}
	}
}

// TestFallbackToX11ReportsFailureUnderWayland pins the contract that let a
// broken capture look healthy.
//
// Under Wayland this fallback refuses to grab the X root window — an Xwayland
// grab returns a blank screen rather than failing — and closes the frame
// channel instead. It used to return nothing, so Start returned nil and handed
// the caller a closed channel that read as a working capture. pkg/tunnel checks
// for a nil channel, not a closed one, so the capture_unavailable notice never
// reached the viewer and the operator watched a spinner forever.
func TestFallbackToX11ReportsFailureUnderWayland(t *testing.T) {
	t.Setenv("XDG_SESSION_TYPE", "wayland")

	c := &waylandCapturer{
		config: DefaultConfig(),
		frames: make(chan *Frame, 1),
	}

	if established := c.fallbackToX11(context.Background()); established {
		t.Fatal("fallbackToX11 reported a working frame source under Wayland; Start would return nil and the caller would never learn capture is unavailable")
	}

	select {
	case _, open := <-c.frames:
		if open {
			t.Fatal("a frame arrived on a channel that should have been closed")
		}
	default:
		t.Fatal("the frame channel was left open, so a consumer would block forever instead of seeing capture end")
	}
}
