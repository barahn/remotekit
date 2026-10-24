// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package stream

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/barahn/remotekit/bark"
	"github.com/barahn/remotekit/clipboard"
	"github.com/barahn/remotekit/input"
	"github.com/barahn/remotekit/permission"
	"github.com/barahn/remotekit/screen"
	"github.com/barahn/remotekit/transfer"
	"github.com/barahn/remotekit/webrtc"
)

// Runner serves the agent side of a remote session: it answers a viewer's
// WebRTC negotiation, streams the screen, and applies input, clipboard and
// file transfer -- each only as far as the person at the machine has consented
// (see Grant) and the consumer's opt-in checks allow.
//
// A Runner knows nothing of how its signalling arrives. Serve runs it over any
// SignalConn; package tunnel's AgentStreamRunner wraps one to run it over the
// control plane's WebSocket with an enrolled agent's credentials. Configure it
// before the first Serve.
type Runner struct {
	// id is the session ID viewers target; signKey, when set, signs the
	// answers and candidates sent to them. See Config.
	id      string
	signKey ed25519.PrivateKey
	// peerConfig is what each viewer's WebRTC connection is built with; see
	// Config.PeerConfig.
	peerConfig webrtc.PeerConfig

	injector input.Injector
	// clip replaces the system clipboard when set; tests use it so they do
	// not touch the clipboard of whoever runs them.
	clip clipboard.Manager
	// newCapturer replaces screen.NewCapturer when set, for the same reason.
	newCapturer func(screen.CaptureConfig) (screen.Capturer, error)
	// handlers holds message types layered on top of the core session by a
	// consumer; see Handle.
	handlers map[string]MessageHandler

	// granted is what the person at the machine has consented to; see Grant.
	// warned remembers which refusals have been logged.
	// standing is what the consumer's own policy allows with nobody asked,
	// and what a close does not revoke; see SetStandingPermissions.
	consentMu sync.RWMutex
	granted   map[string]bool
	standing  map[string]bool
	warned    map[string]bool
	// permGen counts changes to what viewers may do, under consentMu; each
	// list sent to a viewer carries it, so a viewer can drop a list that
	// arrives after a newer one. See permissions.go.
	permGen uint64

	// viewerAuth, when set, restricts negotiation to signed viewers; see
	// RequireSignedViewers.
	viewerAuth *viewerAuth

	// requireScreenView, when set, withholds the screen until screen_view is
	// granted; see RequireScreenViewConsent.
	requireScreenView bool

	// requireDataChannel, when set, refuses user data over the signalling
	// socket; see RequireDataChannel.
	requireDataChannel bool

	// omitFilePathInReply, when set, returns only the base filename in file_saved
	// replies across the network; see OmitFilePathInReply.
	omitFilePathInReply bool

	// downloadDir, onFileOffer and onFileSaved govern received files; see
	// Config.DownloadDir and files.go.
	downloadDir string
	onFileOffer func(FileOffer) bool
	onFileSaved func(ReceivedFile)
	// onClipboardEvent hears every clipboard sync; see OnClipboardEvent.
	onClipboardEvent func(ClipboardEvent)
	// outgoing holds SendFile's waiters by transfer ID, with the viewer
	// each file goes to; see agentfiles.go.
	outgoingMu sync.Mutex
	outgoing   map[string]outgoingFile

	// onViewer and sessions serve ViewerEvents and Disconnect; see
	// viewers.go.
	onViewer   func(ViewerEvent)
	sessionsMu sync.Mutex
	sessions   map[*sessionHook]struct{}

	// disableRelay and onRelayMode govern the JPEG fallback; see
	// DisableRelayFallback and OnRelayMode.
	disableRelay bool
	onRelayMode  func(active bool)
}

// Config is who a Runner is to the viewers it serves.
type Config struct {
	// ID is the session ID viewers address and sign their offers for
	// (webrtc.SignalMessage.TargetID). An enrolled agent uses its agent ID;
	// an on-demand session can use any ID the two ends agree on. Serve
	// refuses to run without one.
	ID string

	// SigningKey, when set, signs every answer and ICE candidate sent to a
	// viewer, so a viewer that knows the matching public key can tell they
	// came from this end and not from the transport in between. It is held
	// in memory only; where it comes from -- a device key file, a key
	// generated for one session -- is the caller's. Nil sends them unsigned.
	SigningKey ed25519.PrivateKey

	// PeerConfig is the ICE configuration -- STUN and TURN servers -- every
	// viewer's WebRTC connection is built with. Nil means
	// webrtc.DefaultPeerConfig(), which configures no ICE servers (host candidates
	// only, working on a LAN and failing behind most NATs). A deployment operating
	// across NATs or firewalls must provide explicit STUN or TURN servers.
	PeerConfig *webrtc.PeerConfig

	// DownloadDir is where files a viewer sends are saved; it is created if
	// it does not exist. Empty means the system's temporary directory. A
	// received file never replaces one already there: it gets a numbered
	// name instead. See also OnFileOffer and OnFileSaved.
	DownloadDir string

	// RequireDataChannel, when set, refuses user data (input, clipboard, files)
	// arriving over the signalling socket, enforcing that all user interactions
	// travel strictly over end-to-end encrypted WebRTC DataChannels.
	RequireDataChannel bool

	// OmitFilePathInReply, when set, sends only the base filename in file_saved
	// replies to remote viewers, preventing disclosure of internal host filesystem
	// paths (CWE-200).
	OmitFilePathInReply bool
}

// New returns a Runner for cfg, with input injection for this platform when
// there is one. Everything the Runner gates is denied until granted.
func New(cfg Config) *Runner {
	r := &Runner{}
	r.Init(cfg)
	return r
}

// Init configures r as New does. It is for a type that embeds a Runner by
// value, so that the type's zero value stays usable for consent; anyone else
// should call New. Call it once, before the first Serve.
//
// A zero Runner that is never initialised can still Grant, Revoke and report
// Granted, but has no ID, so Serve refuses it, and no input injector.
func (r *Runner) Init(cfg Config) {
	r.id = cfg.ID
	r.signKey = cfg.SigningKey
	r.downloadDir = cfg.DownloadDir
	r.requireDataChannel = cfg.RequireDataChannel
	r.omitFilePathInReply = cfg.OmitFilePathInReply
	r.peerConfig = webrtc.DefaultPeerConfig()
	if cfg.PeerConfig != nil {
		r.peerConfig = *cfg.PeerConfig
		r.peerConfig.ICEServers = append([]string(nil), cfg.PeerConfig.ICEServers...)
	}
	r.injector, _ = input.NewInjector()
}

// ID returns the session ID the runner was configured with.
func (r *Runner) ID() string { return r.id }

// logSafe strips line breaks from a value that came off the wire before it
// is logged. viewer_id is chosen by whoever is on the other end of the
// signalling socket; logged as is, a "\n" in it would forge log lines.
// Numbers and booleans decoded from a message go through it too, formatted
// first: they cannot carry a line break, but CodeQL's log-injection query
// cannot tell, and one rule for every peer-supplied value is easier to keep.
func logSafe(s string) string {
	s = strings.ReplaceAll(s, "\n", "")
	return strings.ReplaceAll(s, "\r", "")
}

// signalTTL is how long a signed answer or candidate stays valid. Negotiation
// finishes in seconds; the margin covers a slow relay and clock skew without
// leaving a long replay window.
const signalTTL = 2 * time.Minute

// encodeSignal encodes an outgoing answer or candidate for one viewer.
//
// With a key, the message is signed with webrtc.SignalMessage.Sign, so a
// viewer that knows the agent's public key can tell the SDP and candidates
// came from the agent and not from the relay in between. The viewer is put in
// TargetID, which the signature covers, so a message signed for one viewer
// cannot be replayed to another. viewer_id is still set alongside it, since
// that is what the server routes on.
//
// offerID, when the viewer's offer carried one, is echoed on its answer and
// on each of its candidates, so a viewer that offered more than once can tell
// which offer an answer or a candidate is for.
// It is not covered by the signature: whoever can rewrite it could only make
// the viewer drop a genuine answer, which dropping the message achieves too.
func encodeSignal(m webrtc.SignalMessage, viewerID, offerID string, key ed25519.PrivateKey, now time.Time) ([]byte, error) {
	if key != nil {
		m.TargetID = viewerID
		if err := m.Sign(key, now, signalTTL); err != nil {
			return nil, err
		}
	}
	data, err := m.Encode()
	if err != nil {
		return nil, err
	}
	var fields map[string]interface{}
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	fields["viewer_id"] = viewerID
	if offerID != "" {
		fields["offer_id"] = offerID
	}
	return json.Marshal(fields)
}

func (r *Runner) runSignalingLoop(ctx context.Context, conn SignalConn) {
	signKey := r.signKey
	var stateMu sync.RWMutex
	// One WebRTC negotiation per connected viewer (keyed by the server-assigned viewer_id,
	// or "" for legacy/unattributed messages), so multiple technicians can view the same
	// session concurrently without one viewer's offer tearing down another's in-flight
	// negotiation. Actual frame delivery is a separate JSON-over-WebSocket broadcast (see
	// safeWrite calls below) that already reaches every connection in the room; this map
	// only tracks the SDP offer/answer/ICE bookkeeping.
	peers := make(map[string]*webrtc.PeerSession)
	// One encoder for the session, not one per viewer. VP8 is a chain of
	// predictions, so every viewer has to receive the same bitstream from the
	// same reference frames; encoding separately per viewer would cost N times
	// the CPU to produce N identical streams. A viewer joining mid-session is
	// served by forcing a key frame, which is what videoEncoder.Reset does.
	videoEncoder := screen.NewVP8Encoder(30, 70)
	// Viewers that have reported decoding the WebRTC stream. A peer being
	// connected only means the transport came up; it says nothing about whether
	// the browser can read what is being sent. Until a viewer confirms, the
	// JPEG fallback keeps feeding it -- see the video_ok case below.
	videoConfirmed := make(map[string]bool)
	var capturer screen.Capturer
	var activeCapCancel context.CancelFunc
	var writeMu sync.Mutex

	clipMgr := r.clip
	if clipMgr == nil {
		clipMgr = clipboard.NewManager()
	}
	clipWatcher := clipboard.NewWatcher(clipMgr, 500*time.Millisecond)
	transferMgr, err := transfer.NewManager(r.downloadDir)
	if err != nil {
		// Files are refused, with this reason, rather than the session.
		log.Printf("[AgentStream] cannot receive files: %v\n", err)
	}
	dp := &dataPlane{r: r, clipMgr: clipMgr, clipWatcher: clipWatcher, files: fileInbox{transfer: transferMgr}}

	// The monitor being streamed, as the frames report it; -1 until the
	// first one of the current capture. See displays.go.
	activeDisplay := -1
	currentCapturer := func() screen.Capturer {
		stateMu.RLock()
		defer stateMu.RUnlock()
		return capturer
	}
	dp.displays = func() (bark.DisplaysPayload, bool) {
		c := currentCapturer()
		if c == nil {
			return bark.DisplaysPayload{}, false
		}
		stateMu.RLock()
		active := activeDisplay
		stateMu.RUnlock()
		return displaysPayload(c, active), true
	}
	var lastSelect time.Time
	// requested is the monitor last switched to, -1 until a switch: the
	// frames lag a switch, so they cannot tell whether one is pending.
	requested := -1
	dp.selectDisplay = func(index int) error {
		c := currentCapturer()
		if c == nil {
			return errNoCapture
		}
		// Every switch costs every viewer a key frame, so a viewer may not
		// switch faster than minDisplaySwitch, and asking for the monitor
		// already streamed costs nothing.
		stateMu.Lock()
		current := requested
		if current < 0 {
			current = activeDisplay
		}
		if index == current {
			stateMu.Unlock()
			return nil
		}
		now := time.Now()
		if now.Sub(lastSelect) < minDisplaySwitch {
			stateMu.Unlock()
			return errDisplaySwitchTooSoon
		}
		// Only a switch that happens uses up the window: a bad index must
		// not keep another viewer from switching to a good one.
		if err := c.SetDisplay(index); err != nil {
			stateMu.Unlock()
			return err
		}
		lastSelect = now
		requested = index
		stateMu.Unlock()
		// The new monitor's first frame has to be a key frame for every
		// viewer, and the differ must not compare it with the old one.
		videoEncoder.Reset()
		return nil
	}
	// announceDisplays sends the monitor list to every viewer with a
	// control channel, when the monitor being streamed changes.
	announceDisplays := func() {
		payload, ok := dp.displays()
		if !ok {
			return
		}
		env, err := bark.EncodeEnvelope(bark.TypeDisplays, r.id, payload)
		if err != nil {
			return
		}
		stateMu.RLock()
		live := make([]*webrtc.PeerSession, 0, len(peers))
		for _, p := range peers {
			if p.DataChannelOpen(webrtc.DataChannelControl) {
				live = append(live, p)
			}
		}
		stateMu.RUnlock()
		for _, p := range live {
			_ = p.SendData(webrtc.DataChannelControl, env)
		}
	}
	// announcePermissions tells every viewer with a control channel what is
	// granted now; Grant, Revoke and SetStandingPermissions call it through
	// registerSession.
	announcePermissions := func() {
		granted, gen := r.effectivePermissions()
		stateMu.RLock()
		live := make([]*webrtc.PeerSession, 0, len(peers))
		for _, p := range peers {
			if p.DataChannelOpen(webrtc.DataChannelControl) {
				live = append(live, p)
			}
		}
		stateMu.RUnlock()
		for _, p := range live {
			sendPermissions(p, webrtc.DataChannelControl, r.id, granted, gen)
		}
	}
	var refusal relayRefusal
	relay := &relayGate{disabled: r.disableRelay, onChange: r.onRelayMode}

	safeWrite := func(data []byte) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return conn.WriteMessage(data)
	}

	// socketReply answers a data plane message that came over the socket.
	socketReply := func(msgType string, fields map[string]interface{}) {
		fields["type"] = msgType
		if data, err := json.Marshal(fields); err == nil {
			_ = safeWrite(data)
		}
	}

	// Start clipboard watcher for continuous sync from host to technician
	clipCtx, clipCancel := context.WithCancel(ctx)
	defer clipCancel()

	// A copy a password manager marked never leaves; the consumer hears
	// that one was held back.
	clipWatcher.OnConcealed(func() {
		if r.allow(PermissionClipboard, "host clipboard sync") {
			log.Printf("[AgentStream] Held back a concealed host clipboard change\n")
			r.clipboardEvent(ClipboardEvent{Direction: ClipboardFromHost, Concealed: true})
		}
	})
	go clipWatcher.Start(clipCtx, func(newText string) {
		if !r.allow(PermissionClipboard, "host clipboard sync") {
			return
		}

		// Every viewer with a control channel gets the clipboard there.
		// The socket reaches every viewer at once, the server included,
		// so it is used only while some viewer has no channel to take it.
		stateMu.RLock()
		withChannel := make([]*webrtc.PeerSession, 0, len(peers))
		viaSocket := len(peers) == 0
		for _, p := range peers {
			if p.DataChannelOpen(webrtc.DataChannelControl) {
				withChannel = append(withChannel, p)
			} else {
				viaSocket = true
			}
		}
		stateMu.RUnlock()

		if env, err := bark.EncodeEnvelope(bark.TypeClipboard, r.id, map[string]string{"text": newText}); err == nil {
			for _, p := range withChannel {
				if sErr := p.SendData(webrtc.DataChannelControl, env); sErr != nil {
					viaSocket = true // the channel closed under us
				}
			}
		}

		if viaSocket && !r.requireDataChannel {
			clipMsg, err := json.Marshal(map[string]interface{}{
				"type":       "clipboard",
				"session_id": r.id,
				"text":       newText,
			})
			if err == nil {
				_ = safeWrite(clipMsg)
			}
		}
		log.Printf("[AgentStream] Dispatched host clipboard change to remote (%d bytes)\n", len(newText))
		r.clipboardEvent(ClipboardEvent{Direction: ClipboardFromHost, Size: len(newText)})
	})

	// Send agent_ready ping so any waiting browser viewer immediately initiates the WebRTC offer
	_ = safeWrite([]byte(fmt.Sprintf(`{"type":"agent_ready","session_id":"%s"}`, r.id)))

	// viewerKeys holds the key each connected viewer's offer was signed
	// with, under RequireSignedViewers, for its ViewerEvents.
	viewerKeys := make(map[string]ed25519.PublicKey)
	// present and streamingSent make a viewer's events once per visit: a
	// viewer that re-offers -- the Go viewer does on agent_ready -- gets a
	// new peer but has not left.
	present := make(map[string]bool)
	streamingSent := make(map[string]bool)
	// peerOffers is the offer_id each viewer's current peer answered, so a
	// disconnected notice names the peer it ended.
	peerOffers := make(map[string]string)
	// events delivers ViewerEvents in the order the state changed: they are
	// queued under stateMu and delivered outside it. See viewers.go.
	events := newEventQueue(r.emitViewer)
	// dropPeer forgets one viewer's peer, reporting whether peer was still
	// that viewer's, and queues its left if it had joined. Callers hold
	// stateMu.
	dropPeer := func(id string, peer *webrtc.PeerSession) bool {
		if peer == nil || peers[id] != peer {
			return false
		}
		if present[id] {
			events.push(ViewerEvent{ViewerID: id, Kind: ViewerLeft, At: time.Now(), Key: viewerKeys[id]})
		}
		delete(peers, id)
		delete(videoConfirmed, id)
		delete(viewerKeys, id)
		delete(peerOffers, id)
		delete(present, id)
		delete(streamingSent, id)
		return true
	}
	// dropAll forgets every viewer, under stateMu, closing their peers.
	dropAll := func() {
		for id, peer := range peers {
			_ = peer.Close()
			dropPeer(id, peer)
		}
	}

	unregister := r.registerSession(func(id string) bool {
		stateMu.Lock()
		peer := peers[id]
		offerID := peerOffers[id]
		ok := dropPeer(id, peer)
		stateMu.Unlock()
		if !ok {
			return false
		}
		_ = peer.Close()
		notice, _ := json.Marshal(map[string]string{"type": "disconnected", "viewer_id": id, "offer_id": offerID})
		_ = safeWrite(notice)
		return true
	}, announcePermissions, func(id string) *webrtc.PeerSession {
		stateMu.RLock()
		defer stateMu.RUnlock()
		return peers[id]
	})
	defer unregister()

	defer func() {
		if r := recover(); r != nil {
			log.Printf("[AgentStream] Recovered panic in signaling loop: %v\n", r)
		}
		stateMu.Lock()
		if activeCapCancel != nil {
			activeCapCancel()
			activeCapCancel = nil
		}
		dropAll()
		if capturer != nil {
			capturer.Stop()
			capturer = nil
		}
		stateMu.Unlock()
		relay.stop()
		events.close()
	}()

	for {
		msgBytes, err := conn.ReadMessage()
		if err != nil {
			break
		}

		var signal map[string]interface{}
		if err := json.Unmarshal(msgBytes, &signal); err != nil {
			continue
		}

		msgType, _ := signal["type"].(string)

		// msgKey is the key this message was signed with, when signatures
		// are required.
		var msgKey ed25519.PublicKey
		if r.viewerAuth != nil {
			switch msgType {
			case "offer", "session_start", "candidate":
				key, err := r.viewerAuth.check(msgBytes, r.id, time.Now())
				if err != nil {
					viewerID, _ := signal["viewer_id"].(string)
					// The error can carry peer-supplied text, such as the
					// TargetID a message was signed for.
					log.Printf("[AgentStream] refusing %s from viewer %s: %s\n", logSafe(msgType), logSafe(viewerID), logSafe(err.Error()))
					continue
				}
				msgKey = key
			}
		}

		switch msgType {
		case "offer", "session_start":
			if r.requireScreenView && !r.allow(PermissionScreenView, "screen view") {
				// Tell the viewer why nothing arrives, instead of leaving it
				// waiting on a negotiation the agent will not answer.
				notice, _ := json.Marshal(map[string]string{
					"type":       "consent_required",
					"permission": PermissionScreenView,
				})
				_ = safeWrite(notice)
				continue
			}
			sdp, _ := signal["sdp"].(string)
			viewerID, _ := signal["viewer_id"].(string)
			offerID, _ := signal["offer_id"].(string)
			relay.viewerJoined()

			// WebRTC negotiation (if SDP offer is provided)
			if sdp != "" {
				stateMu.Lock()
				if old, ok := peers[viewerID]; ok {
					// The same viewer negotiating again: a new peer, not a
					// new visit.
					_ = old.Close()
					delete(peers, viewerID)
				}

				peer, err := webrtc.NewPeerSession(r.peerConfig)
				if err == nil {
					peers[viewerID] = peer
					viewerKeys[viewerID] = msgKey
					peerOffers[viewerID] = offerID

					// A viewer that vanishes without a close leaves a
					// connection that eventually fails.
					peer.OnConnectionLost(func() {
						stateMu.Lock()
						ok := dropPeer(viewerID, peer)
						stateMu.Unlock()
						if ok {
							_ = peer.Close()
						}
					})

					// The track has to exist before the answer is built, or the
					// answer carries no video and the browser waits forever for
					// a stream that was never offered back.
					if tErr := peer.CreateVideoTrack("barahn-screen", "screen"); tErr != nil {
						log.Printf("[AgentStream] video track unavailable for viewer %s, falling back to WebSocket frames: %v\n", logSafe(viewerID), tErr)
					} else {
						// A receiver that cannot decode asks for an intra frame.
						// Reset is the right answer to that: it forces a key
						// frame and clears the frame differ, which matters
						// because a differ seeing no change would suppress the
						// frame entirely and leave the request unanswered.
						peer.OnKeyFrameRequest(videoEncoder.Reset)
					}

					// This viewer has no reference frames yet, so whatever it
					// receives first must be a key frame. And it has not yet
					// shown it can decode anything, so it starts unconfirmed.
					delete(videoConfirmed, viewerID)
					videoEncoder.Reset()

					// The viewer creates the data channels in its offer, so
					// the handler has to be in place before the answer.
					peer.OnDataMessage(func(label string, msg []byte) {
						dp.handleChannelMessage(ctx, sender{id: viewerID, key: msgKey}, peer, label, msg)
					})

					peer.OnICECandidate(func(candJSON string) {
						data, err := encodeSignal(webrtc.SignalMessage{
							Type:      webrtc.SignalCandidate,
							SessionID: r.id,
							Candidate: candJSON,
						}, viewerID, offerID, signKey, time.Now())
						if err != nil {
							log.Printf("[AgentStream] not sending ICE candidate to viewer %s: %v\n", logSafe(viewerID), err)
							return
						}
						_ = safeWrite(data)
					})

					answered := false
					answerSDP, aErr := peer.CreateAnswer(sdp)
					if aErr == nil {
						ansBytes, sErr := encodeSignal(webrtc.SignalMessage{
							Type:      webrtc.SignalAnswer,
							SessionID: r.id,
							SDP:       answerSDP,
						}, viewerID, offerID, signKey, time.Now())
						if sErr != nil {
							log.Printf("[AgentStream] not sending answer to viewer %s: %v\n", logSafe(viewerID), sErr)
						} else {
							answered = safeWrite(ansBytes) == nil
						}
					} else {
						log.Printf("[AgentStream] WebRTC answer note (direct WebSocket stream active): %v\n", aErr)
					}
					// A viewer has joined once it has an answer, not before:
					// a negotiation that fails leaves no peer behind, and
					// ends the visit of a viewer whose re-offer failed.
					if answered {
						if !present[viewerID] {
							present[viewerID] = true
							events.push(ViewerEvent{ViewerID: viewerID, Kind: ViewerJoined, At: time.Now(), Key: msgKey})
						}
					} else {
						_ = peer.Close()
						dropPeer(viewerID, peer)
					}
				}
				stateMu.Unlock()
			}

			// Screen capture stream management: start if not already active
			stateMu.Lock()
			isAlreadyCapturing := (capturer != nil && activeCapCancel != nil)
			stateMu.Unlock()

			if !isAlreadyCapturing {
				newCapturer := r.newCapturer
				if newCapturer == nil {
					newCapturer = screen.NewCapturer
				}
				cap, err := newCapturer(screen.DefaultConfig())
				var framesChan <-chan *screen.Frame

				if err == nil {
					capCtx, cancelCap := context.WithCancel(ctx)
					if err = cap.Start(capCtx); err == nil {
						stateMu.Lock()
						capturer = cap
						activeCapCancel = cancelCap
						// A restarted capture begins on its own default
						// monitor, so neither the monitor the previous one
						// streamed nor the one last asked of it holds.
						activeDisplay = -1
						requested = -1
						stateMu.Unlock()
						framesChan = cap.Frames()
					} else {
						cancelCap()
					}
				}

				// Capture is either real or absent. It is never invented: an
				// operator looking at a fabricated desktop has no way to tell it
				// from the endpoint, and would act on it. See #157.
				if framesChan == nil {
					log.Printf("[AgentStream] screen capture unavailable - notifying the viewer instead of streaming placeholder frames: %v", err)
					unavailable := map[string]string{
						"type":   "capture_unavailable",
						"reason": "no screen capture backend is available on this endpoint",
					}
					// The machine could capture but has not been allowed to
					// (macOS Screen Recording): say which permission, so the
					// viewer can tell someone what to grant.
					var missing *permission.NotGrantedError
					if errors.As(err, &missing) {
						unavailable["reason"] = "the " + missing.Permission.String() + " permission is not granted on this endpoint"
						unavailable["permission"] = missing.Permission.ID()
					}
					notice, _ := json.Marshal(unavailable)
					_ = safeWrite(notice)
					continue
				}

				go func() {
					defer func() {
						if r := recover(); r != nil {
							log.Printf("[AgentStream] Recovered panic in frame sender: %v\n", r)
						}
					}()
					frameCount := 0
					var inputBounds image.Rectangle
					lastSample := time.Time{}
					for frame := range framesChan {
						frameCount++

						if ctx.Err() != nil {
							return
						}
						if frame == nil || frame.Image == nil {
							continue
						}
						// Consent can be withdrawn mid-session. Capture keeps
						// running so a fresh Grant resumes at once, but no
						// frame leaves the machine while it is revoked.
						if r.requireScreenView && !r.Granted(PermissionScreenView) {
							continue
						}
						// Input lands on the monitor being streamed. Bounds is
						// that monitor in global coordinates, which the
						// injector needs to aim at any monitor but the first;
						// it changes when the viewer picks another one.
						bounds := frame.Bounds
						if bounds.Empty() {
							bounds = frame.Image.Bounds()
						}
						if bounds != inputBounds && r.injector != nil {
							r.injector.SetScreenBounds(bounds)
							inputBounds = bounds
						}
						// A frame still buffered from a capture that has since
						// been stopped or replaced must not overwrite what the
						// current one reports.
						stateMu.Lock()
						current := capturer == cap
						changed := current && frame.DisplayIndex != activeDisplay
						if current {
							activeDisplay = frame.DisplayIndex
						}
						stateMu.Unlock()
						if changed {
							announceDisplays()
						}

						// Which viewers are reachable over WebRTC right now, and
						// which of those have shown they can decode what is being
						// sent. A connected peer is not yet a served viewer.
						stateMu.RLock()
						live := make([]*webrtc.PeerSession, 0, len(peers))
						unconfirmed := 0
						for id, p := range peers {
							if !p.IsConnected() {
								unconfirmed++
								continue
							}
							live = append(live, p)
							if !videoConfirmed[id] {
								unconfirmed++
							}
						}
						stateMu.RUnlock()

						if len(live) > 0 {
							// VP8 over WebRTC. The encoder skips frames that
							// carry no visual change, which is most of them on a
							// desktop, and returns nil for those.
							sample, encErr := videoEncoder.Encode(frame)
							if encErr != nil {
								log.Printf("[AgentStream] VP8 encode failed, falling back to WebSocket frames: %v\n", encErr)
							} else if len(sample) > 0 {
								now := time.Now()
								duration := 33 * time.Millisecond
								if !lastSample.IsZero() {
									duration = now.Sub(lastSample)
								}
								lastSample = now
								for _, p := range live {
									if wErr := p.WriteVideoSample(sample, duration); wErr != nil {
										log.Printf("[AgentStream] dropping a frame for one viewer: %v\n", wErr)
									}
								}
								if frameCount%150 == 1 {
									log.Printf("[AgentStream] VP8 frame #%d (%dx%d, %d bytes) to %d viewer(s)\n",
										frameCount, frame.Image.Bounds().Dx(), frame.Image.Bounds().Dy(), len(sample), len(live))
								}
							}
							if unconfirmed == 0 {
								// Every viewer is being served by WebRTC. The
								// JPEG path stops here: at 1080p, running both
								// for one frame is a VP8 encode plus a JPEG
								// encode inside a 33ms budget, and neither fits.
								relay.direct(safeWrite)
								continue
							}
							// Some viewer is not confirmed yet -- still
							// negotiating, or unable to decode this codec. It
							// gets JPEG, but at a third of the rate, so the
							// probation window costs bandwidth and a lower frame
							// rate rather than blowing the frame budget. A
							// viewer that never confirms simply stays here.
							if frameCount%3 != 0 {
								continue
							}
						}

						// No WebRTC viewer is connected -- either negotiation has
						// not finished yet or it failed. JPEG over the WebSocket
						// keeps the session usable meanwhile, unless the
						// consumer refused it, and never without saying so:
						// the server can see these frames.
						if !relay.relay(safeWrite) {
							continue
						}
						var buf bytes.Buffer
						if err := jpeg.Encode(&buf, frame.Image, &jpeg.Options{Quality: 60}); err == nil {
							b64 := base64.StdEncoding.EncodeToString(buf.Bytes())
							frameMsg := map[string]interface{}{
								"type":       "frame",
								"session_id": r.id,
								"data":       b64,
							}
							data, _ := json.Marshal(frameMsg)
							if err := safeWrite(data); err != nil {
								return // WebSocket closed
							}

							if frameCount%30 == 1 {
								log.Printf("[AgentStream] Streaming live screen frame #%d (%dx%d, jpeg b64: %d bytes)\n", frameCount, frame.Image.Bounds().Dx(), frame.Image.Bounds().Dy(), len(b64))
							}
						}
					}
				}()
			}

		case "video_ok", "video_stalled":
			// The viewer reports whether it is actually rendering decoded video.
			// This is the only evidence the agent has that the codec it is
			// sending is one this browser can read, so it is what gates turning
			// the JPEG fallback off -- not the ICE connection state.
			viewerID, _ := signal["viewer_id"].(string)
			stateMu.Lock()
			if msgType == "video_ok" {
				if present[viewerID] && !streamingSent[viewerID] {
					streamingSent[viewerID] = true
					events.push(ViewerEvent{ViewerID: viewerID, Kind: ViewerStreaming, At: time.Now(), Key: viewerKeys[viewerID]})
				}
				videoConfirmed[viewerID] = true
				log.Printf("[AgentStream] viewer %s is decoding WebRTC video; stopping its JPEG fallback\n", logSafe(viewerID))
			} else {
				delete(videoConfirmed, viewerID)
				log.Printf("[AgentStream] viewer %s reports stalled video; resuming the JPEG fallback\n", logSafe(viewerID))
			}
			stateMu.Unlock()

		case "candidate":
			cand, _ := signal["candidate"].(string)
			viewerID, _ := signal["viewer_id"].(string)
			offerID, _ := signal["offer_id"].(string)
			stateMu.RLock()
			peer := peers[viewerID]
			answered := peerOffers[viewerID]
			stateMu.RUnlock()
			// A candidate for an offer other than the one this viewer's
			// peer answered belongs to a peer that offer replaced: pion
			// would refuse it, its ICE credentials being the old ones. One
			// without an offer_id is from a viewer that predates them.
			if cand != "" && peer != nil && (offerID == "" || offerID == answered) {
				_ = peer.AddICECandidate(cand)
			}

		case "input", "clipboard", "file_start", "file_chunk", "file_complete":
			// Under RequireSignedViewers the relay is outside the trust
			// boundary, and these messages carry no signature that covers
			// their content: anything arriving over the socket could have
			// been forged or altered by it. They are accepted only over a
			// data channel, which the signed offer and answer bind to the
			// trusted viewer's DTLS session.
			if r.requireDataChannel || r.viewerAuth != nil {
				refusal.refuse(msgType, safeWrite)
				continue
			}
			fields := signal
			if msgType == "input" {
				// Over the socket the input event is nested in payload.
				payload, ok := signal["payload"].(map[string]interface{})
				if !ok {
					continue
				}
				fields = payload
			}
			viewerID, _ := signal["viewer_id"].(string)
			dp.handle(ctx, sender{id: viewerID}, msgType, fields, socketReply)

		case "resize", "viewport_size":
			// Informational signal indicating technician viewport dimensions
			w, _ := signal["width"].(float64)
			h, _ := signal["height"].(float64)
			if w > 0 && h > 0 {
				log.Printf("[AgentStream] Technician browser viewport size: %sx%s\n", logSafe(fmt.Sprintf("%.0f", w)), logSafe(fmt.Sprintf("%.0f", h)))
			}

		case "chat":
			text, _ := signal["text"].(string)
			sender, _ := signal["sender"].(string)
			log.Printf("[AgentStream] Chat message from %s: %s\n", logSafe(sender), logSafe(text))

		case "focus_state":
			focused, _ := signal["focused"].(bool)
			log.Printf("[AgentStream] Session focus state changed: focused=%s\n", logSafe(fmt.Sprint(focused)))

		case "close":
			r.Revoke()
			stateMu.Lock()
			dropAll()
			if capturer != nil {
				capturer.Stop()
				capturer = nil
			}
			if activeCapCancel != nil {
				activeCapCancel()
				activeCapCancel = nil
			}
			stateMu.Unlock()
			relay.stop()

		default:
			// Anything the core does not implement itself belongs to whoever
			// layered it on top -- see Handle.
			if h, ok := r.handlers[msgType]; ok {
				h(ctx, r.id, signal, safeWrite)
			}
		}
	}
}

// boolPayload reads a boolean field from a decoded JSON signal payload, defaulting to false.
func boolPayload(payload map[string]interface{}, key string) bool {
	v, _ := payload[key].(bool)
	return v
}

func (r *Runner) handleInputPayload(payload map[string]interface{}) {
	if !r.allow(PermissionRemoteControl, "input") {
		return
	}
	evtType, _ := payload["type"].(string)

	switch evtType {
	case "mousemove", "mouse_move":
		x, _ := payload["x"].(float64)
		y, _ := payload["y"].(float64)
		if r.injector != nil {
			_ = r.injector.MoveMouse(x, y)
		}

	case "mousedown", "mouse_down":
		x, _ := payload["x"].(float64)
		y, _ := payload["y"].(float64)
		btn, _ := payload["button"].(float64)
		if r.injector != nil {
			_ = r.injector.MouseDown(input.MouseButton(btn), x, y)
		}

	case "mouseup", "mouse_up":
		x, _ := payload["x"].(float64)
		y, _ := payload["y"].(float64)
		btn, _ := payload["button"].(float64)
		if r.injector != nil {
			_ = r.injector.MouseUp(input.MouseButton(btn), x, y)
		}

	case "wheel", "scroll":
		x, _ := payload["x"].(float64)
		y, _ := payload["y"].(float64)
		deltaX, _ := payload["deltaX"].(float64)
		deltaY, _ := payload["deltaY"].(float64)
		if r.injector != nil {
			_ = r.injector.Scroll(deltaX, deltaY, x, y)
		}

	case "keydown", "key_down":
		key, _ := payload["key"].(string)
		code, _ := payload["code"].(string)
		if r.injector != nil {
			_ = r.injector.KeyDown(input.KeyboardEvent{
				Key: key, Code: code,
				Ctrl:  boolPayload(payload, "ctrl"),
				Alt:   boolPayload(payload, "alt"),
				Shift: boolPayload(payload, "shift"),
				Meta:  boolPayload(payload, "meta"),
			})
		}

	case "keyup", "key_up":
		key, _ := payload["key"].(string)
		code, _ := payload["code"].(string)
		if r.injector != nil {
			_ = r.injector.KeyUp(input.KeyboardEvent{
				Key: key, Code: code,
				Ctrl:  boolPayload(payload, "ctrl"),
				Alt:   boolPayload(payload, "alt"),
				Shift: boolPayload(payload, "shift"),
				Meta:  boolPayload(payload, "meta"),
			})
		}
	}
}
