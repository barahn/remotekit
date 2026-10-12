// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package stream

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"image"
	"strconv"
	"sync"
	"time"

	"github.com/barahn/remotekit/bark"
	"github.com/barahn/remotekit/screen/codec/vp8"
	"github.com/barahn/remotekit/webrtc"
)

// Viewer is the other end of a Runner: it asks for the agent's screen,
// decodes it, and sends input back (barahn/remotekit#87). Every viewer before
// it was a browser; this one is Go, for a consumer whose controlling side is a
// native program rather than a web page.
//
// It takes the screen over WebRTC only. A Runner falls back to JPEG frames over
// the signalling socket for a viewer that has not confirmed it decodes the
// video; this viewer ignores them, since the transport in the middle can see
// them, and confirms as soon as its first frame decodes.
//
//	v := stream.NewViewer(stream.ViewerConfig{ID: "me", AgentID: sessionID, PeerConfig: &ice})
//	v.OnFrame(func(img *image.YCbCr) { /* draw it */ })
//	go v.Serve(ctx, conn)
//	_ = v.SendInput(bark.InputEvent{Type: "mouse_move", X: 0.5, Y: 0.5})
type Viewer struct {
	cfg  ViewerConfig
	conf webrtc.PeerConfig

	onFrame  func(img *image.YCbCr)
	onNotice func(Notice)

	writeMu sync.Mutex
	conn    SignalConn

	// mu guards the peer, which a re-offer replaces, and the candidates
	// that arrive before its answer.
	mu        sync.Mutex
	peer      *webrtc.PeerSession
	answered  bool
	offerID   string
	offers    int
	pending   []string
	confirmed bool
	nonces    *webrtc.NonceCache
}

// ViewerConfig is who a Viewer is, and whom it views.
type ViewerConfig struct {
	// ID is this viewer's viewer_id: the agent answers it by this ID, and the
	// transport routes on it.
	ID string

	// AgentID is the Runner's session ID (its Config.ID). Signed offers
	// target it.
	AgentID string

	// SigningKey, when set, signs the offer and ICE candidates, for a Runner
	// with RequireSignedViewers. It is held in memory only.
	SigningKey ed25519.PrivateKey

	// AgentKey, when set, is the only key the agent's answer and candidates
	// are accepted from; unsigned ones are dropped. It is how a viewer keeps
	// the transport from answering in the agent's place, and it must reach
	// the viewer some way the transport does not control.
	AgentKey ed25519.PublicKey

	// PeerConfig is the ICE configuration, as in Config. Nil means
	// webrtc.DefaultPeerConfig().
	PeerConfig *webrtc.PeerConfig
}

// Notice is something the agent told the viewer instead of streaming: that
// the person at the machine has not consented, that the machine cannot
// capture its screen, or that input must go over a data channel.
type Notice struct {
	// Type is the agent's message type: "consent_required",
	// "capture_unavailable" or "data_channel_required".
	Type string
	// Detail is the permission, the reason or the message type concerned.
	Detail string
}

// ErrNotConnected is what SendInput returns while the control channel to the
// agent is not open.
var ErrNotConnected = errors.New("stream: the viewer is not connected to the agent")

// NewViewer returns a Viewer for cfg.
func NewViewer(cfg ViewerConfig) *Viewer {
	conf := webrtc.DefaultPeerConfig()
	if cfg.PeerConfig != nil {
		conf = *cfg.PeerConfig
		conf.ICEServers = append([]string(nil), cfg.PeerConfig.ICEServers...)
	}
	return &Viewer{cfg: cfg, conf: conf, nonces: webrtc.NewNonceCache()}
}

// OnFrame registers fn for every decoded picture of the agent's screen. It
// runs on the video goroutine, one frame at a time; img is valid only until fn
// returns, so fn copies what it keeps. Register it before Serve.
func (v *Viewer) OnFrame(fn func(img *image.YCbCr)) { v.onFrame = fn }

// OnNotice registers fn for the agent's notices. Register it before Serve.
func (v *Viewer) OnNotice(fn func(Notice)) { v.onNotice = fn }

// SendInput sends one input event to the agent over the control data channel.
// X and Y are fractions of the screen, from 0 to 1. The agent applies it only
// if the person at the machine granted remote control.
func (v *Viewer) SendInput(ev bark.InputEvent) error {
	v.mu.Lock()
	peer := v.peer
	v.mu.Unlock()
	if peer == nil || !peer.DataChannelOpen(webrtc.DataChannelControl) {
		return ErrNotConnected
	}
	data, err := bark.EncodeEnvelope(bark.TypeInputEvent, v.cfg.AgentID, ev)
	if err != nil {
		return err
	}
	return peer.SendData(webrtc.DataChannelControl, data)
}

// Serve runs the viewer's side of one session over conn until conn fails or
// ctx is cancelled, and closes conn either way. Cancelling ctx tells the agent
// the session is over first, which revokes what was granted.
//
// It offers at once, and again whenever the agent announces itself: an agent
// that starts after the viewer missed the first offer.
func (v *Viewer) Serve(ctx context.Context, conn SignalConn) error {
	if v.cfg.ID == "" || v.cfg.AgentID == "" {
		return errors.New("stream: viewer needs its own ID and the agent's")
	}
	v.writeMu.Lock()
	v.conn = conn
	v.writeMu.Unlock()
	stop := context.AfterFunc(ctx, func() {
		_ = v.write(map[string]interface{}{"type": string(webrtc.SignalClose), "viewer_id": v.cfg.ID})
		_ = conn.Close()
	})
	defer stop()
	defer v.closePeer()

	if err := v.offer(); err != nil {
		_ = conn.Close()
		return err
	}
	for {
		data, err := conn.ReadMessage()
		if err != nil {
			_ = conn.Close()
			return ctx.Err()
		}
		v.handle(data)
	}
}

// handle acts on one message from the agent.
func (v *Viewer) handle(data []byte) {
	var head struct {
		Type       string `json:"type"`
		ViewerID   string `json:"viewer_id"`
		Permission string `json:"permission"`
		Reason     string `json:"reason"`
		MsgType    string `json:"message_type"`
		OfferID    string `json:"offer_id"`
	}
	if json.Unmarshal(data, &head) != nil {
		return
	}
	switch head.Type {
	case "agent_ready":
		_ = v.offer()
	case string(webrtc.SignalAnswer), string(webrtc.SignalCandidate):
		if head.ViewerID != "" && head.ViewerID != v.cfg.ID {
			return // another viewer's
		}
		m, ok := v.accept(data)
		if !ok {
			return
		}
		if m.Type == webrtc.SignalAnswer {
			v.applyAnswer(m.SDP, head.OfferID)
		} else {
			v.addCandidate(m.Candidate)
		}
	case "consent_required":
		v.notify(Notice{Type: head.Type, Detail: head.Permission})
	case "capture_unavailable":
		v.notify(Notice{Type: head.Type, Detail: head.Reason})
	case "data_channel_required":
		v.notify(Notice{Type: head.Type, Detail: head.MsgType})
	}
}

// accept decodes an answer or candidate, and checks its signature when the
// agent's key is known.
func (v *Viewer) accept(data []byte) (*webrtc.SignalMessage, bool) {
	m, err := webrtc.DecodeSignalMessage(data)
	if err != nil {
		return nil, false
	}
	if v.cfg.AgentKey == nil {
		return m, true
	}
	now := time.Now()
	if m.VerifyFrom(v.cfg.AgentKey, now) != nil || m.TargetID != v.cfg.ID || m.SessionID != v.cfg.AgentID {
		return nil, false
	}
	return m, v.nonces.CheckAndRecord(m, now)
}

func (v *Viewer) notify(n Notice) {
	if v.onNotice != nil {
		v.onNotice(n)
	}
}

// offer starts a negotiation from scratch: a new peer that receives video and
// opens the two data channels, and an offer for it.
func (v *Viewer) offer() error {
	peer, err := webrtc.NewPeerSession(v.conf)
	if err != nil {
		return err
	}
	if err := peer.ReceiveVideo(); err != nil {
		_ = peer.Close()
		return err
	}
	for _, label := range []string{webrtc.DataChannelControl, webrtc.DataChannelTransfer} {
		if err := peer.OpenDataChannel(label); err != nil {
			_ = peer.Close()
			return err
		}
	}
	dec := vp8.NewDecoder()
	peer.OnVideoFrame(func(frame []byte, _ bool) { v.decode(peer, dec, frame) })

	// pion gathers from inside CreateOffer, so candidates can be ready before
	// the offer is sent. The agent drops a candidate for a viewer it has no
	// peer for yet, or gives it to the peer this offer replaces, so they wait
	// for the offer; a replaced peer's are not sent at all.
	var (
		gateMu    sync.Mutex
		offerSent bool
		held      []string
	)
	sendCandidate := func(c string) {
		v.mu.Lock()
		current := v.peer == peer
		v.mu.Unlock()
		if current {
			_ = v.sendSignal(webrtc.SignalMessage{Type: webrtc.SignalCandidate, Candidate: c}, "")
		}
	}
	peer.OnICECandidate(func(c string) {
		gateMu.Lock()
		if !offerSent {
			held = append(held, c)
			gateMu.Unlock()
			return
		}
		gateMu.Unlock()
		sendCandidate(c)
	})

	v.mu.Lock()
	old := v.peer
	v.offers++
	id := strconv.Itoa(v.offers)
	v.peer, v.offerID, v.answered, v.pending, v.confirmed = peer, id, false, nil, false
	v.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}

	sdp, err := peer.CreateOffer()
	if err != nil {
		return err
	}
	if err := v.sendSignal(webrtc.SignalMessage{Type: webrtc.SignalOffer, SDP: sdp}, id); err != nil {
		return err
	}
	gateMu.Lock()
	offerSent = true
	ready := held
	held = nil
	gateMu.Unlock()
	for _, c := range ready {
		sendCandidate(c)
	}
	return nil
}

// decode decodes one frame from peer and hands it on. A frame that does not
// decode costs a key frame.
func (v *Viewer) decode(peer *webrtc.PeerSession, dec *vp8.Decoder, frame []byte) {
	img, err := dec.Decode(frame)
	if err != nil {
		_ = peer.RequestKeyFrame()
		return
	}
	if img == nil {
		return
	}
	v.mu.Lock()
	current := v.peer == peer
	first := current && !v.confirmed
	if first {
		v.confirmed = true
	}
	v.mu.Unlock()
	if !current {
		return
	}
	if first {
		// The agent keeps sending JPEG over the signalling socket until
		// the viewer says it can read the video.
		_ = v.write(map[string]interface{}{"type": "video_ok", "viewer_id": v.cfg.ID})
	}
	if v.onFrame != nil {
		v.onFrame(img)
	}
}

// applyAnswer applies the agent's answer to the current offer. An answer to
// an earlier one -- the viewer re-offers when the agent announces itself, and
// an agent that was already running answers both -- is dropped: applied to the
// new peer, it would name ICE credentials that peer never offered. An answer
// without an offer_id comes from an agent that predates them, and is taken.
func (v *Viewer) applyAnswer(sdp, offerID string) {
	v.mu.Lock()
	peer := v.peer
	if peer == nil || v.answered || offerID != "" && offerID != v.offerID {
		v.mu.Unlock()
		return
	}
	v.mu.Unlock()
	// Messages are handled one at a time, so no candidate is queued while
	// the answer is applied. One that does not apply leaves the offer open
	// for one that does.
	if peer.SetRemoteAnswer(sdp) != nil {
		return
	}
	v.mu.Lock()
	if v.peer != peer {
		v.mu.Unlock()
		return
	}
	v.answered = true
	pending := v.pending
	v.pending = nil
	v.mu.Unlock()
	for _, c := range pending {
		_ = peer.AddICECandidate(c)
	}
}

// maxPendingCandidates bounds the remote candidates held for an answer. An
// agent gathers a handful; past this, candidates are dropped rather than
// letting whoever writes the socket grow the queue without end.
const maxPendingCandidates = 64

// addCandidate adds a remote candidate, or holds it until the answer: pion
// refuses candidates before the remote description.
func (v *Viewer) addCandidate(c string) {
	v.mu.Lock()
	peer := v.peer
	if peer == nil {
		v.mu.Unlock()
		return
	}
	if !v.answered {
		if len(v.pending) < maxPendingCandidates {
			v.pending = append(v.pending, c)
		}
		v.mu.Unlock()
		return
	}
	v.mu.Unlock()
	_ = peer.AddICECandidate(c)
}

// sendSignal sends an offer or candidate, signed when the viewer has a key,
// with viewer_id alongside for the transport to route on, and an offer's ID
// for the agent to echo.
func (v *Viewer) sendSignal(m webrtc.SignalMessage, offerID string) error {
	m.SessionID = v.cfg.AgentID
	if v.cfg.SigningKey != nil {
		m.TargetID = v.cfg.AgentID
		if err := m.Sign(v.cfg.SigningKey, time.Now(), signalTTL); err != nil {
			return err
		}
	}
	data, err := m.Encode()
	if err != nil {
		return err
	}
	var fields map[string]interface{}
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	fields["viewer_id"] = v.cfg.ID
	if offerID != "" {
		fields["offer_id"] = offerID
	}
	return v.write(fields)
}

func (v *Viewer) write(fields map[string]interface{}) error {
	data, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	v.writeMu.Lock()
	defer v.writeMu.Unlock()
	if v.conn == nil {
		return ErrNotConnected
	}
	return v.conn.WriteMessage(data)
}

func (v *Viewer) closePeer() {
	v.mu.Lock()
	peer := v.peer
	v.peer = nil
	v.mu.Unlock()
	if peer != nil {
		_ = peer.Close()
	}
}
