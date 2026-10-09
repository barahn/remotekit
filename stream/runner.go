// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package stream

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image/jpeg"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/barahn/remotekit/bark"
	"github.com/barahn/remotekit/clipboard"
	"github.com/barahn/remotekit/input"
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

	// viewerAuth, when set, restricts negotiation to signed viewers; see
	// RequireSignedViewers.
	viewerAuth *viewerAuth

	// requireScreenView, when set, withholds the screen until screen_view is
	// granted; see RequireScreenViewConsent.
	requireScreenView bool

	// requireDataChannel, when set, refuses user data over the signalling
	// socket; see RequireDataChannel.
	requireDataChannel bool

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
	// webrtc.DefaultPeerConfig(), which contacts public STUN servers run by
	// a third party on every session; a deployment that should not, or that
	// runs its own STUN/TURN, sets it. A non-nil config with no servers uses
	// host candidates only, which works on a LAN and fails behind most NATs.
	PeerConfig *webrtc.PeerConfig
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
func encodeSignal(m webrtc.SignalMessage, viewerID string, key ed25519.PrivateKey, now time.Time) ([]byte, error) {
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

	clipMgr := clipboard.NewManager()
	clipWatcher := clipboard.NewWatcher(clipMgr, 500*time.Millisecond)
	transferMgr, _ := transfer.NewManager("")
	dp := &dataPlane{r: r, clipMgr: clipMgr, clipWatcher: clipWatcher, transfer: transferMgr}
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
	})

	// Send agent_ready ping so any waiting browser viewer immediately initiates the WebRTC offer
	_ = safeWrite([]byte(fmt.Sprintf(`{"type":"agent_ready","session_id":"%s"}`, r.id)))

	defer func() {
		if r := recover(); r != nil {
			log.Printf("[AgentStream] Recovered panic in signaling loop: %v\n", r)
		}
		stateMu.Lock()
		if activeCapCancel != nil {
			activeCapCancel()
			activeCapCancel = nil
		}
		for id, peer := range peers {
			_ = peer.Close()
			delete(peers, id)
		}
		if capturer != nil {
			capturer.Stop()
			capturer = nil
		}
		stateMu.Unlock()
		relay.stop()
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

		if r.viewerAuth != nil {
			switch msgType {
			case "offer", "session_start", "candidate":
				if err := r.viewerAuth.check(msgBytes, r.id, time.Now()); err != nil {
					viewerID, _ := signal["viewer_id"].(string)
					// The error can carry peer-supplied text, such as the
					// TargetID a message was signed for.
					log.Printf("[AgentStream] refusing %s from viewer %s: %s\n", logSafe(msgType), logSafe(viewerID), logSafe(err.Error()))
					continue
				}
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
			relay.viewerJoined()

			// WebRTC negotiation (if SDP offer is provided)
			if sdp != "" {
				stateMu.Lock()
				if old, ok := peers[viewerID]; ok {
					_ = old.Close()
					delete(peers, viewerID)
				}

				peer, err := webrtc.NewPeerSession(r.peerConfig)
				if err == nil {
					peers[viewerID] = peer

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
						dp.handleChannelMessage(ctx, peer, label, msg)
					})

					peer.OnICECandidate(func(candJSON string) {
						data, err := encodeSignal(webrtc.SignalMessage{
							Type:      webrtc.SignalCandidate,
							SessionID: r.id,
							Candidate: candJSON,
						}, viewerID, signKey, time.Now())
						if err != nil {
							log.Printf("[AgentStream] not sending ICE candidate to viewer %s: %v\n", logSafe(viewerID), err)
							return
						}
						_ = safeWrite(data)
					})

					answerSDP, aErr := peer.CreateAnswer(sdp)
					if aErr == nil {
						ansBytes, sErr := encodeSignal(webrtc.SignalMessage{
							Type:      webrtc.SignalAnswer,
							SessionID: r.id,
							SDP:       answerSDP,
						}, viewerID, signKey, time.Now())
						if sErr != nil {
							log.Printf("[AgentStream] not sending answer to viewer %s: %v\n", logSafe(viewerID), sErr)
						} else {
							_ = safeWrite(ansBytes)
						}
					} else {
						log.Printf("[AgentStream] WebRTC answer note (direct WebSocket stream active): %v\n", aErr)
					}
				}
				stateMu.Unlock()
			}

			// Screen capture stream management: start if not already active
			stateMu.Lock()
			isAlreadyCapturing := (capturer != nil && activeCapCancel != nil)
			stateMu.Unlock()

			if !isAlreadyCapturing {
				cap, err := screen.NewCapturer(screen.DefaultConfig())
				var framesChan <-chan *screen.Frame

				if err == nil {
					capCtx, cancelCap := context.WithCancel(ctx)
					if startErr := cap.Start(capCtx); startErr == nil {
						stateMu.Lock()
						capturer = cap
						activeCapCancel = cancelCap
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
					log.Printf("[AgentStream] screen capture unavailable - notifying the viewer instead of streaming placeholder frames")
					notice, _ := json.Marshal(map[string]string{
						"type":   "capture_unavailable",
						"reason": "no screen capture backend is available on this endpoint",
					})
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
						if frameCount == 1 && r.injector != nil {
							r.injector.SetScreenBounds(frame.Image.Bounds())
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
			stateMu.RLock()
			peer := peers[viewerID]
			stateMu.RUnlock()
			if cand != "" && peer != nil {
				_ = peer.AddICECandidate(cand)
			}

		case "input", "clipboard", "file_start", "file_chunk", "file_complete":
			if r.requireDataChannel {
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
			dp.handle(ctx, msgType, fields, socketReply)

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
			for id, peer := range peers {
				_ = peer.Close()
				delete(peers, id)
			}
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
