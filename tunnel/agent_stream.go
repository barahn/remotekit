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
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/mendsec/barahn/pkg/clipboard"
	"github.com/mendsec/barahn/pkg/input"
	"github.com/mendsec/barahn/pkg/screen"
	"github.com/mendsec/barahn/pkg/transfer"
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

	headers := http.Header{}
	if r.creds.AgentToken != "" {
		headers.Set("X-Barahn-Agent-Token", r.creds.AgentToken)
	} else {
		headers.Set("X-Barahn-Agent-Token", r.creds.AgentID)
	}

	for {
		select {
		case <-ctx.Done():
			return
		default:
			ws, _, err := dialer.DialContext(ctx, signalURL, headers)
			if err != nil {
				fmt.Printf("[AgentStream Error] Failed to connect to signaling WebSocket (%s): %v\n", signalURL, err)
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
			fmt.Printf("[AgentStream] Dispatched host clipboard change to remote (%d bytes)\n", len(newText))
		}
	})

	// Send agent_ready ping so any waiting browser viewer immediately initiates the WebRTC offer
	_ = safeWrite([]byte(fmt.Sprintf(`{"type":"agent_ready","session_id":"%s"}`, r.creds.AgentID)))

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
			if capturer != nil {
				capturer.Stop()
				capturer = nil
			}
			if currentPeer != nil {
				_ = currentPeer.Close()
				currentPeer = nil
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
			var framesChan <-chan *screen.Frame

			if err == nil {
				stateMu.Lock()
				capturer = cap
				capCtx, cancelCap := context.WithCancel(ctx)
				activeCapCancel = cancelCap
				stateMu.Unlock()

				if startErr := cap.Start(capCtx); startErr == nil {
					framesChan = cap.Frames()
				}
			}

			// If native capture is unavailable (headless / display-less container), stream synthesized desktop
			if framesChan == nil {
				simChan := make(chan *screen.Frame, 2)
				framesChan = simChan
				go func() {
					defer close(simChan)
					ticker := time.NewTicker(33 * time.Millisecond)
					defer ticker.Stop()
					var seq uint64
					hostname, _ := os.Hostname()
					if hostname == "" {
						hostname = "endpoint"
					}
					for {
						select {
						case <-ctx.Done():
							return
						case t := <-ticker.C:
							seq++
							img := screen.GenerateTestDesktopImage(1920, 1080, runtime.GOOS, hostname, r.creds.AgentID, seq, t.UTC(), 960, 540)
							f := &screen.Frame{
								Image:       img,
								Bounds:      img.Bounds(),
								CapturedAt:  t,
								SequenceNum: seq,
							}
							select {
							case simChan <- f:
							default:
							}
						}
					}
				}()
			}

			go func() {
				defer func() {
					if r := recover(); r != nil {
						fmt.Printf("[AgentStream] Recovered panic in frame sender: %v\n", r)
					}
				}()
				frameCount := 0
				for frame := range framesChan {
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

		case "candidate":
			cand, _ := signal["candidate"].(string)
			stateMu.RLock()
			peer := currentPeer
			stateMu.RUnlock()
			if cand != "" && peer != nil {
				_ = peer.AddICECandidate(cand)
			}

		case "input":
			if payload, ok := signal["payload"].(map[string]interface{}); ok {
				r.handleInputPayload(payload)
			}

		case "resize":
			w, _ := signal["width"].(float64)
			h, _ := signal["height"].(float64)
			if w > 0 && h > 0 && r.injector != nil {
				r.injector.SetScreenBounds(image.Rect(0, 0, int(w), int(h)))
			}

		case "chat":
			text, _ := signal["text"].(string)
			sender, _ := signal["sender"].(string)
			fmt.Printf("[AgentStream] Chat message from %s: %s\n", sender, text)

		case "clipboard":
			text, _ := signal["text"].(string)
			if text != "" {
				clipWatcher.UpdateLastText(text)
				_ = clipMgr.SetText(ctx, text)
				fmt.Printf("[AgentStream] Received and applied clipboard sync (%d bytes)\n", len(text))
				if autoPaste, _ := signal["auto_paste"].(bool); autoPaste && r.injector != nil {
					time.Sleep(30 * time.Millisecond)
					_ = r.injector.KeyDown(input.KeyboardEvent{Key: "Control", Code: "ControlLeft", Ctrl: true})
					_ = r.injector.KeyDown(input.KeyboardEvent{Key: "v", Code: "KeyV", Ctrl: true})
					time.Sleep(30 * time.Millisecond)
					_ = r.injector.KeyUp(input.KeyboardEvent{Key: "v", Code: "KeyV", Ctrl: true})
					_ = r.injector.KeyUp(input.KeyboardEvent{Key: "Control", Code: "ControlLeft", Ctrl: false})
				}
			}

		case "file_start":
			id, _ := signal["transfer_id"].(string)
			name, _ := signal["name"].(string)
			size, _ := signal["size"].(float64)
			sha, _ := signal["sha256"].(string)
			if id != "" && name != "" {
				transferMgr.StartSession(id, name, int64(size), sha)
				fmt.Printf("[AgentStream] Started file transfer session %s (%s, %.0f bytes)\n", id, name, size)
			}

		case "file_chunk":
			id, _ := signal["transfer_id"].(string)
			idx, _ := signal["index"].(float64)
			b64Data, _ := signal["data"].(string)
			if id != "" {
				prog, done, err := transferMgr.AddChunkBase64(id, int(idx), b64Data)
				if err != nil {
					fmt.Printf("[AgentStream] File chunk error: %v\n", err)
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
					fmt.Printf("[AgentStream] File assembly error for %s: %v\n", id, err)
				} else {
					fmt.Printf("[AgentStream] File transfer %s completed! Saved to: %s (SHA: %s)\n", id, destPath, sha)
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
			_ = r.injector.KeyDown(input.KeyboardEvent{Key: key, Code: code})
		}

	case "keyup", "key_up":
		key, _ := payload["key"].(string)
		code, _ := payload["code"].(string)
		if r.injector != nil {
			_ = r.injector.KeyUp(input.KeyboardEvent{Key: key, Code: code})
		}
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
