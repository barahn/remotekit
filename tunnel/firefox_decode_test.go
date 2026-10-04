// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package tunnel

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/barahn/remotekit/screen/codec/vp8"
	"github.com/barahn/remotekit/webrtc"
)

// A browser is the only thing that can say the stream is watchable. Chrome has
// said so; Chrome and the conformance oracle are both libvpx, so agreeing with
// each other is weaker evidence than it looks. Firefox decodes VP8 through its
// own stack, and more to the point it has its own RTP depacketizer and jitter
// buffer -- the parts between the wire and the decoder that a conformance check
// on raw frames never touches.
//
// This drives headless Firefox through geckodriver against a minimal page: one
// recvonly video transceiver, this project's encoder on the other end, and
// getStats().framesDecoded as the verdict. It is deliberately not the product's
// viewer, which needs an account and a TOTP prompt; what is under test is the
// media path, not the login.
//
// Opt-in behind BARAHN_BROWSER_TEST=1: it needs firefox and geckodriver
// installed, and CI runners have neither.
const (
	ffWidth  = 640
	ffHeight = 480
	ffFPS    = 30

	// Enough frames that a decoder which only manages the first key frame and
	// then stalls is not mistaken for one that is working.
	ffWantFrames = 45

	// On loopback the page connects in a second or two; this is headroom for
	// a busy runner, not an expected wait.
	ffConnectTimeout  = 20 * time.Second
	ffConnectAttempts = 2
)

func TestFirefoxDecodesTheStream(t *testing.T) {
	required := os.Getenv("BARAHN_BROWSER_TEST") == "1"
	if !required {
		t.Skip("needs firefox and geckodriver; set BARAHN_BROWSER_TEST=1 to run it")
	}
	// Past this point the caller has asked for this test, so a missing binary
	// is a broken environment rather than an absent one. Skipping here is how
	// the check ends up green while proving nothing -- the same failure the
	// VP8_CONFORMANCE_REQUIRED gate in this repository exists to prevent.
	for _, bin := range []string{"firefox", "geckodriver"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Fatalf("BARAHN_BROWSER_TEST=1 but %s is not installed: %v", bin, err)
		}
	}

	driver := startGeckodriver(t)
	defer driver.stop()

	session := driver.newSession(t)
	defer driver.deleteSession(session)

	// Setting up the connection is not what is under test, and on a loaded CI
	// runner it occasionally stalls with ICE connected and the DTLS handshake
	// never finishing. So a page that never connects gets one more try with a
	// fresh peer; a page that connects and then fails to decode does not.
	var srv *viewerHarness
	for attempt := 1; ; attempt++ {
		srv = newViewerHarness(t)
		driver.navigate(t, session, srv.url())
		if driver.waitConnected(t, session, ffConnectTimeout) {
			break
		}
		state := driver.evalString(t, session, pageStateScript)
		srv.stop()
		if attempt == ffConnectAttempts {
			t.Fatalf("Firefox did not connect in %d attempts of %s each (page state: %s)",
				ffConnectAttempts, ffConnectTimeout, state)
		}
		t.Logf("attempt %d: Firefox did not connect within %s (page state: %s); retrying with a fresh page",
			attempt, ffConnectTimeout, state)
	}
	defer srv.stop()

	deadline := time.Now().Add(45 * time.Second)
	var decoded float64
	for time.Now().Before(deadline) {
		decoded = driver.evalNumber(t, session, "return window.__framesDecoded || 0")
		driver.failOnPageError(t, session)
		if decoded >= ffWantFrames {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}

	if decoded < ffWantFrames {
		t.Fatalf("Firefox decoded %.0f frames in 45s, want at least %d (page state: %s)",
			decoded, ffWantFrames,
			driver.evalString(t, session, pageStateScript))
	}
	t.Logf("Firefox decoded %.0f frames of %dx%d VP8 from this project's encoder",
		decoded, ffWidth, ffHeight)

	if sent := srv.framesSent(); sent == 0 {
		t.Fatal("the harness never sent a frame, so the count above is not ours")
	}
}

// pageStateScript reports enough of the browser's view to tell a failure to
// connect from a failure to decode.
const pageStateScript = `return String(window.__stage) + " ice=" + window.__ice +
	" conn=" + window.__conn + " cands=" + (window.__candCount || 0) +
	" candErr=" + window.__candErr`

// viewerHarness serves the page and answers its offer with a live VP8 stream.
type viewerHarness struct {
	t        *testing.T
	ln       net.Listener
	srv      *http.Server
	mu       sync.Mutex
	peer     *webrtc.PeerSession
	cands    []string
	stopOnce sync.Once
	stopCh   chan struct{}
	sent     atomic.Int64
}

func newViewerHarness(t *testing.T) *viewerHarness {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	h := &viewerHarness{t: t, ln: ln, stopCh: make(chan struct{})}

	mux := http.NewServeMux()
	mux.HandleFunc("/", h.servePage)
	mux.HandleFunc("/offer", h.serveOffer)
	mux.HandleFunc("/candidates", h.serveCandidates)
	h.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = h.srv.Serve(ln) }()
	return h
}

func (h *viewerHarness) url() string { return "http://" + h.ln.Addr().String() + "/" }

func (h *viewerHarness) framesSent() int64 { return h.sent.Load() }

func (h *viewerHarness) stop() {
	h.stopOnce.Do(func() {
		close(h.stopCh)
		_ = h.srv.Close()
		h.mu.Lock()
		peer := h.peer
		h.mu.Unlock()
		if peer != nil {
			_ = peer.Close()
		}
	})
}

func (h *viewerHarness) servePage(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, viewerPage)
}

// serveOffer answers the browser's offer and starts encoding into the track.
func (h *viewerHarness) serveOffer(w http.ResponseWriter, r *http.Request) {
	offer, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	peer, err := webrtc.NewPeerSession(webrtc.PeerConfig{})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := peer.CreateVideoTrack("barahn-screen", "screen"); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	var wantKeyFrame atomic.Bool
	peer.OnKeyFrameRequest(func() { wantKeyFrame.Store(true) })
	peer.OnICECandidate(func(c string) {
		h.mu.Lock()
		h.cands = append(h.cands, c)
		h.mu.Unlock()
	})

	answer, err := peer.CreateAnswer(string(offer))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	h.mu.Lock()
	h.peer = peer
	h.mu.Unlock()

	go h.stream(peer, &wantKeyFrame)

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, answer)
}

func (h *viewerHarness) serveCandidates(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<16))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		h.mu.Lock()
		peer := h.peer
		h.mu.Unlock()
		if peer != nil && len(body) > 0 {
			_ = peer.AddICECandidate(string(body))
		}
		return
	}
	h.mu.Lock()
	out := append([]string(nil), h.cands...)
	h.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// stream encodes moving content for as long as the browser is watching.
func (h *viewerHarness) stream(peer *webrtc.PeerSession, wantKeyFrame *atomic.Bool) {
	enc, err := vp8.NewEncoder(ffWidth, ffHeight, ffFPS)
	if err != nil {
		return
	}
	enc.SetKeyFrameInterval(60)
	enc.SetRateControl(true)
	enc.SetBitrate(2_000_000)

	tick := time.NewTicker(time.Second / ffFPS)
	defer tick.Stop()
	for i := 0; ; i++ {
		select {
		case <-h.stopCh:
			return
		case <-tick.C:
		}
		if !peer.IsConnected() {
			i--
			continue
		}
		if wantKeyFrame.Swap(false) {
			enc.ForceKeyFrame()
		}
		frame, encErr := enc.Encode(ffSourceFrame(i))
		if encErr != nil {
			return
		}
		if err := peer.WriteVideoSample(frame, time.Second/ffFPS); err != nil {
			return
		}
		h.sent.Add(1)
	}
}

// ffSourceFrame is moving, structured content in I420.
func ffSourceFrame(idx int) []byte {
	buf := make([]byte, 0, ffWidth*ffHeight*3/2)
	for y := 0; y < ffHeight; y++ {
		for x := 0; x < ffWidth; x++ {
			v := (x/16+y/16)%2*80 + 60
			if bar := (idx * 6) % ffWidth; x >= bar && x < bar+24 {
				v = 240
			}
			buf = append(buf, byte(v))
		}
	}
	for _, base := range []int{95, 165} {
		for y := 0; y < ffHeight/2; y++ {
			for x := 0; x < ffWidth/2; x++ {
				buf = append(buf, byte(base+(x/8+y/8+idx)%2*35))
			}
		}
	}
	return buf
}

// viewerPage is the smallest client that exercises the path: a recvonly video
// transceiver, trickled candidates, and framesDecoded read back from getStats.
const viewerPage = `<!doctype html>
<meta charset="utf-8">
<title>barahn vp8 decode check</title>
<video id="v" autoplay muted playsinline></video>
<script>
window.__stage = "starting";
window.__framesDecoded = 0;
window.__error = null;

(async () => {
  try {
    const pc = new RTCPeerConnection();
    window.__pc = pc;
    pc.oniceconnectionstatechange = () => { window.__ice = pc.iceConnectionState; };
    pc.onconnectionstatechange = () => { window.__conn = pc.connectionState; };
    pc.addTransceiver("video", { direction: "recvonly" });
    pc.ontrack = (e) => {
      const v = document.getElementById("v");
      v.srcObject = e.streams[0] || new MediaStream([e.track]);
      window.__stage = "track";
    };
    pc.onicecandidate = (e) => {
      if (!e.candidate) return;
      fetch("/candidates", { method: "POST", body: JSON.stringify(e.candidate.toJSON()) });
    };

    const offer = await pc.createOffer();
    await pc.setLocalDescription(offer);
    window.__stage = "offered";

    const resp = await fetch("/offer", { method: "POST", body: pc.localDescription.sdp });
    if (!resp.ok) throw new Error("offer rejected: " + resp.status);
    await pc.setRemoteDescription({ type: "answer", sdp: await resp.text() });
    window.__stage = "answered";

    // The answer is produced before gathering finishes, so candidates are
    // polled rather than carried in the SDP.
    const seen = new Set();
    setInterval(async () => {
      const list = await (await fetch("/candidates")).json();
      for (const raw of list) {
        if (seen.has(raw)) continue;
        seen.add(raw);
        try { await pc.addIceCandidate(JSON.parse(raw)); window.__candCount = (window.__candCount||0)+1; } catch (err) { window.__candErr = String(err); }
      }
    }, 250);

    setInterval(async () => {
      const stats = await pc.getStats();
      stats.forEach((r) => {
        if (r.type === "inbound-rtp" && r.kind === "video") {
          window.__framesDecoded = r.framesDecoded || 0;
          window.__stage = "decoding:" + pc.connectionState;
        }
      });
    }, 200);
  } catch (err) {
    window.__error = err && err.message ? err.message : String(err);
  }
})();
</script>
`

// --- a WebDriver client, just enough of one ---

type gecko struct {
	cmd  *exec.Cmd
	addr string
}

func startGeckodriver(t *testing.T) *gecko {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("picking a port: %v", err)
	}
	addr := ln.Addr().String()
	_, port, _ := net.SplitHostPort(addr)
	_ = ln.Close()

	cmd := exec.Command("geckodriver", "--host", "127.0.0.1", "--port", port)
	// Left nil so the child writes straight to /dev/null. Handing os/exec a
	// non-file writer makes it pipe and copy instead, and Wait then blocks
	// until every process holding the write end exits -- which includes the
	// Firefox that geckodriver spawned, long after geckodriver is gone.
	cmd.Stdout = nil
	cmd.Stderr = nil
	isolateProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting geckodriver: %v", err)
	}
	g := &gecko{cmd: cmd, addr: "http://" + addr}

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(g.addr + "/status") //nolint:noctx // local, short-lived
		if err == nil {
			_ = resp.Body.Close()
			return g
		}
		time.Sleep(100 * time.Millisecond)
	}
	g.stop()
	t.Fatal("geckodriver did not come up")
	return nil
}

func (g *gecko) stop() {
	if g == nil || g.cmd == nil || g.cmd.Process == nil {
		return
	}
	killDriver(g.cmd.Process.Pid)
	_ = g.cmd.Process.Kill()
	// Reap in the background with a deadline: teardown is not worth hanging a
	// suite over.
	done := make(chan struct{})
	go func() {
		_ = g.cmd.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
	}
}

func (g *gecko) do(t *testing.T, method, path string, body any) map[string]any {
	t.Helper()
	var buf io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshalling %s %s: %v", method, path, err)
		}
		buf = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, g.addr+path, buf) //nolint:noctx // local, short-lived
	if err != nil {
		t.Fatalf("building %s %s: %v", method, path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close() //nolint:errcheck // test client

	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decoding the reply to %s %s: %v", method, path, err)
	}
	if resp.StatusCode >= 400 {
		t.Fatalf("%s %s failed: %v", method, path, out)
	}
	return out
}

func (g *gecko) newSession(t *testing.T) string {
	t.Helper()
	out := g.do(t, http.MethodPost, "/session", map[string]any{
		"capabilities": map[string]any{
			"alwaysMatch": map[string]any{
				"browserName": "firefox",
				"moz:firefoxOptions": map[string]any{
					"args": []string{"-headless"},
					// Firefox will not play media without a user gesture
					// unless autoplay is allowed, and the page has no one to
					// click it.
					"prefs": map[string]any{
						"media.autoplay.default":                            0,
						"media.autoplay.blocking_policy":                    0,
						"media.navigator.permission.disabled":               true,
						"media.peerconnection.ice.loopback":                 true,
						"media.peerconnection.ice.obfuscate_host_addresses": false,
					},
				},
			},
		},
	})
	value, _ := out["value"].(map[string]any)
	id, _ := value["sessionId"].(string)
	if id == "" {
		t.Fatalf("no session id in %v", out)
	}
	return id
}

func (g *gecko) deleteSession(id string) {
	req, err := http.NewRequest(http.MethodDelete, g.addr+"/session/"+id, nil) //nolint:noctx // teardown
	if err != nil {
		return
	}
	resp, err := http.DefaultClient.Do(req)
	if err == nil {
		_ = resp.Body.Close()
	}
}

func (g *gecko) navigate(t *testing.T, id, url string) {
	t.Helper()
	g.do(t, http.MethodPost, "/session/"+id+"/url", map[string]any{"url": url})
}

func (g *gecko) eval(t *testing.T, id, script string) any {
	t.Helper()
	out := g.do(t, http.MethodPost, "/session/"+id+"/execute/sync", map[string]any{
		"script": script, "args": []any{},
	})
	return out["value"]
}

func (g *gecko) evalNumber(t *testing.T, id, script string) float64 {
	t.Helper()
	v, ok := g.eval(t, id, script).(float64)
	if !ok {
		return 0
	}
	return v
}

// failOnPageError ends the test if the page's script has thrown.
func (g *gecko) failOnPageError(t *testing.T, id string) {
	t.Helper()
	if g.evalNumber(t, id, "return window.__error ? -1 : 0") < 0 {
		t.Fatalf("the page reported an error: %s",
			g.evalString(t, id, "return String(window.__error)"))
	}
}

// waitConnected reports whether the page's peer connection reaches
// "connected" within timeout.
func (g *gecko) waitConnected(t *testing.T, id string, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		g.failOnPageError(t, id)
		if g.evalString(t, id, "return String(window.__conn)") == "connected" {
			return true
		}
		time.Sleep(250 * time.Millisecond)
	}
	return false
}

func (g *gecko) evalString(t *testing.T, id, script string) string {
	t.Helper()
	v := g.eval(t, id, script)
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}
