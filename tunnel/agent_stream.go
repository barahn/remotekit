// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package tunnel

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image/jpeg"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/barahn/remotekit/clipboard"
	"github.com/barahn/remotekit/input"
	"github.com/barahn/remotekit/screen"
	"github.com/barahn/remotekit/transfer"
	"github.com/barahn/remotekit/webrtc"
	"github.com/gorilla/websocket"
)

type AgentStreamRunner struct {
	creds              *AgentCredentials
	insecureSkipVerify bool
	injector           input.Injector
	// handlers holds message types layered on top of the core session by a
	// consumer; see Handle.
	handlers map[string]MessageHandler
}

func NewAgentStreamRunner(creds *AgentCredentials, insecureSkipVerify bool) *AgentStreamRunner {
	inj, _ := input.NewInjector()
	return &AgentStreamRunner{
		creds:              creds,
		insecureSkipVerify: insecureSkipVerify,
		injector:           inj,
	}
}

// Start connects to the signalling channel and serves it until ctx is
// cancelled, reconnecting whenever the connection drops.
//
// It never connects without a credential: if the runner's AgentToken is
// empty, Start logs why and returns immediately, and nothing will retry.
// Start reports no error, so callers that need to know should check
// AgentToken themselves before calling it -- an empty token means the agent
// has to re-enrol.
func (r *AgentStreamRunner) Start(ctx context.Context) {
	wsURL := r.creds.ServerAddr
	if strings.HasPrefix(wsURL, "https://") {
		wsURL = "wss://" + wsURL[8:]
	} else if strings.HasPrefix(wsURL, "http://") {
		wsURL = "ws://" + wsURL[7:]
	}
	signalURL := fmt.Sprintf("%s/api/v1/sessions/%s/signal", strings.TrimRight(wsURL, "/"), r.creds.AgentID)

	dialer := wsDialer(r.creds.ServerKeyPin, r.insecureSkipVerify)

	headers, err := signalHeaders(r.creds)
	if err != nil {
		log.Printf("[AgentStream Error] %v; not connecting\n", err)
		return
	}

	signKey, err := signalSigningKey(r.creds)
	if err != nil {
		log.Printf("[AgentStream Error] %v; not connecting\n", err)
		return
	}

	for {
		select {
		case <-ctx.Done():
			return
		default:
			// The proof carries a timestamp and a single-use nonce, so it
			// is rebuilt for every attempt rather than once: a reconnect
			// would otherwise present a stale or already used one.
			attempt := headers.Clone()
			if err := connectProofHeaders(attempt, r.creds, AudienceSignal, time.Now()); err != nil {
				log.Printf("[AgentStream Error] %v; not connecting\n", err)
				return
			}
			ws, _, err := dialer.DialContext(ctx, signalURL, attempt)
			if err != nil {
				log.Printf("[AgentStream Error] Failed to connect to signaling WebSocket (%s): %v\n", signalURL, err)
				time.Sleep(2 * time.Second)
				continue
			}

			ws.SetReadLimit(10 * 1024 * 1024) // 10MB limit for fallback JPEG frames

			log.Printf("[AgentStream] Connected to signaling channel for agent %s\n", r.creds.AgentID)
			r.runSignalingLoop(ctx, ws, signKey)
			_ = ws.Close()
			time.Sleep(1 * time.Second)
		}
	}
}

// errNoAgentToken is why Start refuses to dial without a credential.
var errNoAgentToken = errors.New("agent_stream: agent has no token; re-enrol before connecting")

// signalHeaders builds the headers the signalling socket authenticates with.
//
// It refuses to proceed without a token. It used to fall back to sending the
// agent ID in the token header, and the ID is not a secret -- the server hands
// it out and it appears in URLs and logs. A server that accepted that fallback
// would let anyone who had seen an ID connect as that agent.
func signalHeaders(creds *AgentCredentials) (http.Header, error) {
	if creds == nil || creds.AgentToken == "" {
		return nil, errNoAgentToken
	}
	h := http.Header{}
	h.Set("X-Barahn-Agent-Token", creds.AgentToken)
	return h, nil
}

// logSafe strips line breaks from a value that came off the wire before it
// is logged. viewer_id is chosen by whoever is on the other end of the
// signalling socket; logged as is, a "\n" in it would forge log lines.
func logSafe(s string) string {
	s = strings.ReplaceAll(s, "\n", "")
	return strings.ReplaceAll(s, "\r", "")
}

// signalTTL is how long a signed answer or candidate stays valid. Negotiation
// finishes in seconds; the margin covers a slow relay and clock skew without
// leaving a long replay window.
const signalTTL = 2 * time.Minute

// signalSigningKey loads the device key the agent signs its answers and ICE
// candidates with, or returns nil for an agent enrolled without one, whose
// messages go out unsigned as before. It is loaded once per Start rather than
// per message.
func signalSigningKey(creds *AgentCredentials) (ed25519.PrivateKey, error) {
	if creds == nil || creds.DeviceKeyPath == "" {
		return nil, nil
	}
	priv, err := LoadDeviceKey(creds.DeviceKeyPath)
	if err != nil {
		return nil, fmt.Errorf("agent_stream: loading device key: %w", err)
	}
	return priv, nil
}

// encodeSignal encodes an outgoing answer or candidate for one viewer.
//
// With a key, the message is signed with webrtc.SignalMessage.Sign, so a
// viewer that knows the agent's device key can tell the SDP and candidates
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

func (r *AgentStreamRunner) runSignalingLoop(ctx context.Context, ws *websocket.Conn, signKey ed25519.PrivateKey) {
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

	safeWrite := func(data []byte) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return ws.WriteMessage(websocket.TextMessage, data)
	}

	// Start clipboard watcher for continuous sync from host to technician
	clipCtx, clipCancel := context.WithCancel(ctx)
	defer clipCancel()

	go clipWatcher.Start(clipCtx, func(newText string) {
		clipMsg, err := json.Marshal(map[string]interface{}{
			"type":       "clipboard",
			"session_id": r.creds.AgentID,
			"text":       newText,
		})
		if err == nil {
			_ = safeWrite(clipMsg)
			log.Printf("[AgentStream] Dispatched host clipboard change to remote (%d bytes)\n", len(newText))
		}
	})

	// Send agent_ready ping so any waiting browser viewer immediately initiates the WebRTC offer
	_ = safeWrite([]byte(fmt.Sprintf(`{"type":"agent_ready","session_id":"%s"}`, r.creds.AgentID)))

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
	}()

	for {
		_, msgBytes, err := ws.ReadMessage()
		if err != nil {
			break
		}

		var signal map[string]interface{}
		if err := json.Unmarshal(msgBytes, &signal); err != nil {
			continue
		}

		msgType, _ := signal["type"].(string)

		switch msgType {
		case "offer", "session_start":
			sdp, _ := signal["sdp"].(string)
			viewerID, _ := signal["viewer_id"].(string)

			// WebRTC negotiation (if SDP offer is provided)
			if sdp != "" {
				stateMu.Lock()
				if old, ok := peers[viewerID]; ok {
					_ = old.Close()
					delete(peers, viewerID)
				}

				peer, err := webrtc.NewPeerSession(webrtc.DefaultPeerConfig())
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

					peer.OnICECandidate(func(candJSON string) {
						data, err := encodeSignal(webrtc.SignalMessage{
							Type:      webrtc.SignalCandidate,
							SessionID: r.creds.AgentID,
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
							SessionID: r.creds.AgentID,
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
						// keeps the session usable meanwhile.
						var buf bytes.Buffer
						if err := jpeg.Encode(&buf, frame.Image, &jpeg.Options{Quality: 60}); err == nil {
							b64 := base64.StdEncoding.EncodeToString(buf.Bytes())
							frameMsg := map[string]interface{}{
								"type":       "frame",
								"session_id": r.creds.AgentID,
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

		case "input":
			if payload, ok := signal["payload"].(map[string]interface{}); ok {
				r.handleInputPayload(payload)
			}

		case "resize", "viewport_size":
			// Informational signal indicating technician viewport dimensions
			w, _ := signal["width"].(float64)
			h, _ := signal["height"].(float64)
			if w > 0 && h > 0 {
				log.Printf("[AgentStream] Technician browser viewport size: %.0fx%.0f\n", w, h)
			}

		case "chat":
			text, _ := signal["text"].(string)
			sender, _ := signal["sender"].(string)
			log.Printf("[AgentStream] Chat message from %s: %s\n", logSafe(sender), logSafe(text))

		case "focus_state":
			focused, _ := signal["focused"].(bool)
			log.Printf("[AgentStream] Session focus state changed: focused=%v\n", focused)

		case "clipboard":
			text, _ := signal["text"].(string)
			if text != "" {
				clipWatcher.UpdateLastText(text)
				_ = clipMgr.SetText(ctx, text)
				log.Printf("[AgentStream] Received and applied clipboard sync (%d bytes)\n", len(text))
			}

		case "file_start":
			id, _ := signal["transfer_id"].(string)
			name, _ := signal["name"].(string)
			size, _ := signal["size"].(float64)
			sha, _ := signal["sha256"].(string)
			if id != "" && name != "" {
				transferMgr.StartSession(id, name, int64(size), sha)
				log.Printf("[AgentStream] Started file transfer session %s (%s, %d bytes)\n", logSafe(id), logSafe(name), int64(size))
			}

		case "file_chunk":
			id, _ := signal["transfer_id"].(string)
			idx, _ := signal["index"].(float64)
			b64Data, _ := signal["data"].(string)
			if id != "" {
				prog, done, err := transferMgr.AddChunkBase64(id, int(idx), b64Data)
				if err != nil {
					log.Printf("[AgentStream] File chunk error: %v\n", err)
				}
				progMsg, _ := json.Marshal(map[string]interface{}{
					"type":        "file_progress",
					"transfer_id": id,
					"progress":    prog,
					"done":        done,
				})
				_ = safeWrite(progMsg)
			}

		case "file_complete":
			id, _ := signal["transfer_id"].(string)
			if id != "" {
				destPath, sha, err := transferMgr.AssembleFile(id)
				errStr := ""
				if err != nil {
					errStr = err.Error()
					log.Printf("[AgentStream] File assembly error for %s: %v\n", id, err)
				} else {
					log.Printf("[AgentStream] File transfer %s completed! Saved to: %s (SHA: %s)\n", logSafe(id), logSafe(destPath), sha)
				}
				resMsg, _ := json.Marshal(map[string]interface{}{
					"type":        "file_saved",
					"transfer_id": id,
					"path":        destPath,
					"sha256":      sha,
					"error":       errStr,
				})
				_ = safeWrite(resMsg)
			}

		case "close":
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

		default:
			// Anything the core does not implement itself belongs to whoever
			// layered it on top -- see Handle.
			if h, ok := r.handlers[msgType]; ok {
				h(ctx, r.creds.AgentID, signal, safeWrite)
			}
		}
	}
}

// boolPayload reads a boolean field from a decoded JSON signal payload, defaulting to false.
func boolPayload(payload map[string]interface{}, key string) bool {
	v, _ := payload[key].(bool)
	return v
}

func (r *AgentStreamRunner) handleInputPayload(payload map[string]interface{}) {
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
