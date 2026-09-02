//go:build linux

package screen

import (
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
