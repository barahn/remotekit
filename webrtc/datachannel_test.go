// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package webrtc

import (
	"errors"
	"sync"
	"testing"
	"time"

	pion "github.com/pion/webrtc/v4"
)

// dataPair is a viewer and an agent negotiated over loopback, the viewer
// offering and the agent answering, exactly as in production.
type dataPair struct {
	viewer, agent *PeerSession
}

// connectDataPair brings up the pair after setup has had its chance to open
// channels on the viewer and register callbacks on the agent, both of which
// have to happen before the offer is made.
func connectDataPair(t *testing.T, setup func(viewer, agent *PeerSession)) dataPair {
	t.Helper()
	viewer, err := NewPeerSession(loopbackConfig())
	if err != nil {
		t.Fatalf("viewer: %v", err)
	}
	t.Cleanup(func() { _ = viewer.Close() })
	agent, err := NewPeerSession(loopbackConfig())
	if err != nil {
		t.Fatalf("agent: %v", err)
	}
	t.Cleanup(func() { _ = agent.Close() })

	setup(viewer, agent)

	agentCands := newCandidateRelay(agent)
	viewerCands := newCandidateRelay(viewer)
	agent.OnICECandidate(viewerCands.accept)
	viewer.OnICECandidate(agentCands.accept)

	offer, err := viewer.CreateOffer()
	if err != nil {
		t.Fatalf("CreateOffer: %v", err)
	}
	answer, err := agent.CreateAnswer(offer)
	if err != nil {
		t.Fatalf("CreateAnswer: %v", err)
	}
	agentCands.ready()
	if err := viewer.SetRemoteAnswer(answer); err != nil {
		t.Fatalf("SetRemoteAnswer: %v", err)
	}
	viewerCands.ready()
	return dataPair{viewer: viewer, agent: agent}
}

// inbox collects data channel messages for a test to wait on.
type inbox struct {
	ch chan string
}

func newInbox() *inbox { return &inbox{ch: make(chan string, 16)} }

func (b *inbox) accept(label string, msg []byte) { b.ch <- label + ":" + string(msg) }

func (b *inbox) want(t *testing.T, expected string) {
	t.Helper()
	select {
	case got := <-b.ch:
		if got != expected {
			t.Fatalf("got message %q, want %q", got, expected)
		}
	case <-time.After(20 * time.Second):
		t.Fatalf("no message within 20s, want %q", expected)
	}
}

// waitOpen waits until the session reports the labelled channel open.
func waitOpen(t *testing.T, ps *PeerSession, label string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !ps.DataChannelOpen(label) {
		if time.Now().After(deadline) {
			t.Fatalf("channel %q not open within 20s (state %v)", label, ps.ConnectionState())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestDataChannelBothWays proves the agent accepts the channels the viewer
// creates and that messages cross in both directions on each, kept apart by
// label.
func TestDataChannelBothWays(t *testing.T) {
	agentIn, viewerIn := newInbox(), newInbox()
	var opened sync.Map
	p := connectDataPair(t, func(viewer, agent *PeerSession) {
		for _, label := range []string{DataChannelControl, DataChannelTransfer} {
			if err := viewer.OpenDataChannel(label); err != nil {
				t.Fatalf("OpenDataChannel(%s): %v", label, err)
			}
		}
		viewer.OnDataMessage(viewerIn.accept)
		agent.OnDataMessage(agentIn.accept)
		agent.OnDataChannelOpen(func(label string) { opened.Store(label, true) })
	})

	for _, label := range []string{DataChannelControl, DataChannelTransfer} {
		waitOpen(t, p.viewer, label)
		waitOpen(t, p.agent, label)
		if _, ok := opened.Load(label); !ok {
			t.Errorf("OnDataChannelOpen was not told about %q", label)
		}
	}

	if err := p.viewer.SendData(DataChannelControl, []byte(`{"type":"input"}`)); err != nil {
		t.Fatalf("viewer SendData: %v", err)
	}
	agentIn.want(t, DataChannelControl+`:{"type":"input"}`)

	if err := p.viewer.SendData(DataChannelTransfer, []byte(`{"type":"file_chunk"}`)); err != nil {
		t.Fatalf("viewer SendData: %v", err)
	}
	agentIn.want(t, DataChannelTransfer+`:{"type":"file_chunk"}`)

	if err := p.agent.SendData(DataChannelControl, []byte(`{"type":"clipboard"}`)); err != nil {
		t.Fatalf("agent SendData: %v", err)
	}
	viewerIn.want(t, DataChannelControl+`:{"type":"clipboard"}`)
}

// TestDataChannelRefusesUnreliable proves a channel that may drop or reorder
// messages is closed rather than accepted: input and file chunks depend on
// both guarantees.
func TestDataChannelRefusesUnreliable(t *testing.T) {
	agentIn := newInbox()
	var unreliable *pion.DataChannel
	p := connectDataPair(t, func(viewer, agent *PeerSession) {
		if err := viewer.OpenDataChannel(DataChannelControl); err != nil {
			t.Fatalf("OpenDataChannel: %v", err)
		}
		ordered := false
		var err error
		unreliable, err = viewer.pc.CreateDataChannel(DataChannelTransfer, &pion.DataChannelInit{Ordered: &ordered})
		if err != nil {
			t.Fatalf("CreateDataChannel: %v", err)
		}
		agent.OnDataMessage(agentIn.accept)
	})

	// The reliable channel coming up shows negotiation finished, so the
	// agent has had the unreliable one handed to it as well.
	waitOpen(t, p.agent, DataChannelControl)

	deadline := time.Now().Add(20 * time.Second)
	for unreliable.ReadyState() != pion.DataChannelStateClosed {
		if time.Now().After(deadline) {
			t.Fatalf("unreliable channel still %v after 20s; the agent should have closed it", unreliable.ReadyState())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if p.agent.DataChannelOpen(DataChannelTransfer) {
		t.Fatal("agent reports the unreliable channel as open")
	}
	if err := p.agent.SendData(DataChannelTransfer, []byte("x")); !errors.Is(err, ErrDataChannelNotOpen) {
		t.Fatalf("SendData on the refused channel: got %v, want ErrDataChannelNotOpen", err)
	}
}

// TestSendDataWithoutChannel covers the state every session starts in, and
// the one a viewer that predates data channels never leaves.
func TestSendDataWithoutChannel(t *testing.T) {
	ps, err := NewPeerSession(loopbackConfig())
	if err != nil {
		t.Fatalf("NewPeerSession: %v", err)
	}
	defer func() { _ = ps.Close() }()

	if ps.DataChannelOpen(DataChannelControl) {
		t.Fatal("a fresh session reports a data channel open")
	}
	if err := ps.SendData(DataChannelControl, []byte("x")); !errors.Is(err, ErrDataChannelNotOpen) {
		t.Fatalf("got %v, want ErrDataChannelNotOpen", err)
	}

	_ = ps.Close()
	if err := ps.OpenDataChannel(DataChannelControl); err == nil {
		t.Fatal("OpenDataChannel succeeded on a closed session")
	}
}

// TestDataChannelClosedIsForgotten proves a channel the viewer closes stops
// being reachable, so a sender learns to fall back instead of writing into a
// channel that is gone.
func TestDataChannelClosedIsForgotten(t *testing.T) {
	var viewerDC *pion.DataChannel
	p := connectDataPair(t, func(viewer, _ *PeerSession) {
		var err error
		viewerDC, err = viewer.pc.CreateDataChannel(DataChannelControl, nil)
		if err != nil {
			t.Fatalf("CreateDataChannel: %v", err)
		}
	})
	waitOpen(t, p.agent, DataChannelControl)

	if err := viewerDC.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for p.agent.DataChannelOpen(DataChannelControl) {
		if time.Now().After(deadline) {
			t.Fatal("agent still reports the channel open 20s after the viewer closed it")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
