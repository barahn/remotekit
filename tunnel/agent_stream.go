package tunnel

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/mendsec/barahn/pkg/input"
	"github.com/mendsec/barahn/pkg/screen"
	"github.com/mendsec/barahn/pkg/webrtc"
)

type AgentStreamRunner struct {
	creds              *AgentCredentials
	insecureSkipVerify bool
	injector           input.Injector
}

func NewAgentStreamRunner(creds *AgentCredentials, insecureSkipVerify bool) *AgentStreamRunner {
	inj, _ := input.NewInjector()
	return &AgentStreamRunner{
		creds:              creds,
		insecureSkipVerify: insecureSkipVerify,
		injector:           inj,
	}
}

func (r *AgentStreamRunner) Start(ctx context.Context) {
	wsURL := strings.Replace(r.creds.ServerAddr, "http://", "ws://", 1)
	wsURL = strings.Replace(wsURL, "https://", "wss://", 1)
	signalURL := fmt.Sprintf("%s/api/v1/sessions/%s/signal", strings.TrimRight(wsURL, "/"), r.creds.AgentID)

	dialer := websocket.Dialer{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: r.insecureSkipVerify}, // #nosec G402 -- CLI opt-in flag for dev/test
	}

	for {
		select {
		case <-ctx.Done():
			return
		default:
			ws, _, err := dialer.DialContext(ctx, signalURL, http.Header{})
			if err != nil {
				time.Sleep(2 * time.Second)
				continue
			}

			fmt.Printf("[AgentStream] Connected to signaling channel for agent %s\n", r.creds.AgentID)
			r.runSignalingLoop(ctx, ws)
			_ = ws.Close()
			time.Sleep(1 * time.Second)
		}
	}
}

func (r *AgentStreamRunner) runSignalingLoop(ctx context.Context, ws *websocket.Conn) {
	var stateMu sync.RWMutex
	var currentPeer *webrtc.PeerSession
	var capturer screen.Capturer
	var activeCapCancel context.CancelFunc
	var writeMu sync.Mutex

	safeWrite := func(data []byte) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return ws.WriteMessage(websocket.TextMessage, data)
	}

	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("[AgentStream] Recovered panic in signaling loop: %v\n", r)
		}
		stateMu.Lock()
		if activeCapCancel != nil {
			activeCapCancel()
			activeCapCancel = nil
		}
		if currentPeer != nil {
			_ = currentPeer.Close()
			currentPeer = nil
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
		case "offer":
			sdp, _ := signal["sdp"].(string)
			if sdp == "" {
				continue
			}

			stateMu.Lock()
			if activeCapCancel != nil {
				activeCapCancel()
				activeCapCancel = nil
			}
			if currentPeer != nil {
				_ = currentPeer.Close()
				currentPeer = nil
			}
			if capturer != nil {
				capturer.Stop()
				capturer = nil
			}

			peer, err := webrtc.NewPeerSession(webrtc.DefaultPeerConfig())
			if err != nil {
				stateMu.Unlock()
				continue
			}
			currentPeer = peer
			stateMu.Unlock()

			peer.OnICECandidate(func(candJSON string) {
				candMsg := map[string]interface{}{
					"type":       "candidate",
					"session_id": r.creds.AgentID,
					"candidate":  candJSON,
				}
				data, _ := json.Marshal(candMsg)
				_ = safeWrite(data)
			})

			_ = peer.CreateVideoTrack("screen", "video")

			answerSDP, err := peer.CreateAnswer(sdp)
			if err != nil {
				continue
			}

			ansMsg := map[string]interface{}{
				"type":       "answer",
				"session_id": r.creds.AgentID,
				"sdp":        answerSDP,
			}
			ansBytes, _ := json.Marshal(ansMsg)
			_ = safeWrite(ansBytes)

			// Start screen capturer and feed samples to WebRTC & WebSocket
			cap, err := screen.NewCapturer(screen.DefaultConfig())
			if err == nil {
				stateMu.Lock()
				capturer = cap
				capCtx, cancelCap := context.WithCancel(ctx)
				activeCapCancel = cancelCap
				stateMu.Unlock()

				if err := cap.Start(capCtx); err == nil {
					go func() {
						defer func() {
							if r := recover(); r != nil {
								fmt.Printf("[AgentStream] Recovered panic in frame sender: %v\n", r)
							}
						}()
						frameCount := 0
						for frame := range cap.Frames() {
							frameCount++

							stateMu.RLock()
							peer := currentPeer
							stateMu.RUnlock()

							if peer == nil || peer.ConnectionState() == 4 /* Closed */ {
								return
							}
							vp8Sample := BuildVP8Sample(frame)
							_ = peer.WriteVideoSample(vp8Sample, 33*time.Millisecond)

							// Send direct JPEG frame fallback over WebSocket for Podman/Docker networks
							if frame != nil && frame.Image != nil {
								var buf bytes.Buffer
								if err := jpeg.Encode(&buf, frame.Image, &jpeg.Options{Quality: 60}); err == nil {
									b64 := base64.StdEncoding.EncodeToString(buf.Bytes())
									frameMsg := map[string]interface{}{
										"type":       "frame",
										"session_id": r.creds.AgentID,
										"data":       b64,
									}
									data, _ := json.Marshal(frameMsg)
									_ = safeWrite(data)

									if frameCount%30 == 1 {
										fmt.Printf("[AgentStream] Streaming live screen frame #%d (%dx%d, jpeg b64: %d bytes)\n", frameCount, frame.Image.Bounds().Dx(), frame.Image.Bounds().Dy(), len(b64))
									}
								}
							}
						}
					}()
				}
			}

		case "candidate":
			cand, _ := signal["candidate"].(string)
			stateMu.RLock()
			peer := currentPeer
			stateMu.RUnlock()
			if cand != "" && peer != nil {
				_ = peer.AddICECandidate(cand)
			}

		case "input":
			if payload, ok := signal["payload"].(map[string]interface{}); ok && r.injector != nil {
				r.handleInputPayload(payload)
			}

		case "resize":
			w, _ := signal["width"].(float64)
			h, _ := signal["height"].(float64)
			if w > 0 && h > 0 && r.injector != nil {
				r.injector.SetScreenBounds(image.Rect(0, 0, int(w), int(h)))
			}

		case "close":
			stateMu.Lock()
			if currentPeer != nil {
				_ = currentPeer.Close()
				currentPeer = nil
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
		}
	}
}

func (r *AgentStreamRunner) handleInputPayload(payload map[string]interface{}) {
	evtType, _ := payload["type"].(string)
	switch evtType {
	case "mousemove":
		x, _ := payload["x"].(float64)
		y, _ := payload["y"].(float64)
		_ = r.injector.MoveMouse(x, y)

	case "mousedown":
		x, _ := payload["x"].(float64)
		y, _ := payload["y"].(float64)
		btn, _ := payload["button"].(float64)
		_ = r.injector.MouseDown(input.MouseButton(btn), x, y)

	case "mouseup":
		x, _ := payload["x"].(float64)
		y, _ := payload["y"].(float64)
		btn, _ := payload["button"].(float64)
		_ = r.injector.MouseUp(input.MouseButton(btn), x, y)

	case "wheel":
		x, _ := payload["x"].(float64)
		y, _ := payload["y"].(float64)
		deltaX, _ := payload["deltaX"].(float64)
		deltaY, _ := payload["deltaY"].(float64)
		_ = r.injector.Scroll(deltaX, deltaY, x, y)

	case "keydown":
		key, _ := payload["key"].(string)
		code, _ := payload["code"].(string)
		_ = r.injector.KeyDown(input.KeyboardEvent{Key: key, Code: code})

	case "keyup":
		key, _ := payload["key"].(string)
		code, _ := payload["code"].(string)
		_ = r.injector.KeyUp(input.KeyboardEvent{Key: key, Code: code})
	}
}

// BuildVP8Sample constructs a valid VP8 keyframe uncompressed payload from RGBA screen frame.
func BuildVP8Sample(frame *screen.Frame) []byte {
	width := uint16(1920)
	height := uint16(1080)

	if frame != nil && frame.Image != nil {
		bounds := frame.Image.Bounds()
		if bounds.Dx() > 0 && bounds.Dx() <= 65535 && bounds.Dy() > 0 && bounds.Dy() <= 65535 {
			width = uint16(bounds.Dx())  // #nosec G115 -- bounds checked
			height = uint16(bounds.Dy()) // #nosec G115 -- bounds checked
		}
	}

	// Minimal VP8 Keyframe Header (10 bytes)
	// Frame Tag: 3 bytes (Keyframe = 0, Version = 0, ShowFrame = 1, PartSize = 0)
	// Start Code: 0x9D 0x01 0x2A
	// Width (14 bits) + Scale (2 bits), Height (14 bits) + Scale (2 bits)
	header := make([]byte, 10)
	header[0] = 0x10 // Keyframe, ShowFrame
	header[1] = 0x00
	header[2] = 0x00
	header[3] = 0x9D
	header[4] = 0x01
	header[5] = 0x2A
	header[6] = byte(width & 0xFF)
	header[7] = byte((width >> 8) & 0x3F)
	header[8] = byte(height & 0xFF)
	header[9] = byte((height >> 8) & 0x3F)

	// Encode RGBA image as JPEG payload or YUV420 sample
	var payloadBuf bytes.Buffer
	if frame != nil && frame.Image != nil {
		_ = jpeg.Encode(&payloadBuf, frame.Image, &jpeg.Options{Quality: 70})
	} else {
		img := image.NewRGBA(image.Rect(0, 0, int(width), int(height)))
		draw.Draw(img, img.Bounds(), &image.Uniform{color.RGBA{R: 22, G: 27, B: 34, A: 255}}, image.Point{}, draw.Src)
		_ = jpeg.Encode(&payloadBuf, img, &jpeg.Options{Quality: 50})
	}

	return append(header, payloadBuf.Bytes()...)
}
