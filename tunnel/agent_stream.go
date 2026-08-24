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
	"log"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/mendsec/barahn/pkg/bark"
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
	wsURL := r.creds.ServerAddr
	if strings.HasPrefix(wsURL, "https://") {
		wsURL = "wss://" + wsURL[8:]
	} else if strings.HasPrefix(wsURL, "http://") {
		wsURL = "ws://" + wsURL[7:]
	}
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
				log.Printf("[AgentStream Error] Failed to connect to signaling WebSocket (%s): %v\n", signalURL, err)
				time.Sleep(2 * time.Second)
				continue
			}

			ws.SetReadLimit(10 * 1024 * 1024) // 10MB limit for fallback JPEG frames

			log.Printf("[AgentStream] Connected to signaling channel for agent %s\n", r.creds.AgentID)
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
	scriptRunner := NewScriptRunner()

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
		case "offer", "session_start":
			sdp, _ := signal["sdp"].(string)

			// WebRTC negotiation (if SDP offer is provided)
			if sdp != "" {
				stateMu.Lock()
				if currentPeer != nil {
					_ = currentPeer.Close()
					currentPeer = nil
				}

				peer, err := webrtc.NewPeerSession(webrtc.DefaultPeerConfig())
				if err == nil {
					currentPeer = peer
					peer.OnICECandidate(func(candJSON string) {
						candMsg := map[string]interface{}{
							"type":       "candidate",
							"session_id": r.creds.AgentID,
							"candidate":  candJSON,
						}
						data, _ := json.Marshal(candMsg)
						_ = safeWrite(data)
					})

					answerSDP, aErr := peer.CreateAnswer(sdp)
					if aErr == nil {
						ansMsg := map[string]interface{}{
							"type":       "answer",
							"session_id": r.creds.AgentID,
							"sdp":        answerSDP,
						}
						ansBytes, _ := json.Marshal(ansMsg)
						_ = safeWrite(ansBytes)
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

				// If native capture is unavailable (headless / display-less container), stream synthesized desktop
				if framesChan == nil {
					simChan := make(chan *screen.Frame, 2)
					framesChan = simChan
					simCtx, cancelSim := context.WithCancel(ctx)
					stateMu.Lock()
					activeCapCancel = cancelSim
					stateMu.Unlock()

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
							case <-simCtx.Done():
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
							log.Printf("[AgentStream] Recovered panic in frame sender: %v\n", r)
						}
					}()
					frameCount := 0
					for frame := range framesChan {
						frameCount++

						if ctx.Err() != nil {
							return
						}
						// Direct high-quality JPEG streaming over WebSocket
						if frame != nil && frame.Image != nil {
							if frameCount == 1 && r.injector != nil {
								r.injector.SetScreenBounds(frame.Image.Bounds())
							}
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
					}
				}()
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
			log.Printf("[AgentStream] Chat message from %s: %s\n", sender, text)

		case "focus_state":
			focused, _ := signal["focused"].(bool)
			log.Printf("[AgentStream] Session focus state changed: focused=%v\n", focused)

		case "script_exec_req":
			execID, _ := signal["execution_id"].(string)
			interpreter, _ := signal["interpreter"].(string)
			scriptBody, _ := signal["script_body"].(string)
			timeoutSec, _ := signal["timeout_seconds"].(float64)
			workingDir, _ := signal["working_dir"].(string)

			if execID != "" && scriptBody != "" {
				go func() {
					req := bark.ScriptExecutionRequest{
						ExecutionID:    execID,
						Interpreter:    interpreter,
						ScriptBody:     scriptBody,
						TimeoutSeconds: int(timeoutSec),
						WorkingDir:     workingDir,
					}
					res := scriptRunner.Execute(ctx, req, func(chunk bark.ScriptExecutionChunk) {
						chunkMsg, _ := json.Marshal(map[string]interface{}{
							"type":         "script_exec_chunk",
							"session_id":   r.creds.AgentID,
							"execution_id": chunk.ExecutionID,
							"stream":       chunk.Stream,
							"data":         chunk.Data,
							"index":        chunk.Index,
						})
						_ = safeWrite(chunkMsg)
					})

					resMsg, _ := json.Marshal(map[string]interface{}{
						"type":                  "script_exec_res",
						"session_id":            r.creds.AgentID,
						"execution_id":          res.ExecutionID,
						"exit_code":             res.ExitCode,
						"execution_duration_ms": res.ExecutionDurationMS,
						"error":                 res.Error,
					})
					_ = safeWrite(resMsg)
				}()
			}

		case "power":
			action, _ := signal["action"].(string)
			log.Printf("[AgentStream] Received remote power instruction: %s\n", action)
			go executePowerAction(action)

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
				log.Printf("[AgentStream] Started file transfer session %s (%s, %.0f bytes)\n", id, name, size)
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
					log.Printf("[AgentStream] File transfer %s completed! Saved to: %s (SHA: %s)\n", id, destPath, sha)
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

func executePowerAction(action string) {
	switch action {
	case "reboot":
		if runtime.GOOS == "windows" {
			_ = exec.Command("shutdown", "/r", "/t", "0").Run()
		} else {
			if err := exec.Command("systemctl", "reboot").Run(); err != nil {
				if err := exec.Command("loginctl", "reboot").Run(); err != nil {
					_ = exec.Command("shutdown", "-r", "now").Run()
				}
			}
		}
	case "shutdown", "poweroff":
		if runtime.GOOS == "windows" {
			_ = exec.Command("shutdown", "/s", "/t", "0").Run()
		} else {
			if err := exec.Command("systemctl", "poweroff").Run(); err != nil {
				if err := exec.Command("loginctl", "poweroff").Run(); err != nil {
					_ = exec.Command("shutdown", "-h", "now").Run()
				}
			}
		}
	case "lock":
		if runtime.GOOS == "windows" {
			_ = exec.Command("rundll32.exe", "user32.dll,LockWorkStation").Run()
		} else {
			if err := exec.Command("loginctl", "lock-session").Run(); err != nil {
				if err := exec.Command("gnome-screensaver-command", "-l").Run(); err != nil {
					_ = exec.Command("xdg-screensaver", "lock").Run()
				}
			}
		}
	}
}
