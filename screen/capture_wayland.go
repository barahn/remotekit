// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

//go:build linux

package screen

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"image"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/godbus/dbus/v5"
)

// portalNegotiationTimeout bounds session setup. It is generous on purpose: the
// xdg-desktop-portal flow shows a consent dialog and blocks until a person
// clicks Share, so anything under a few seconds guarantees a timeout and a
// silent fall back to an empty X11 grab.
const portalNegotiationTimeout = 60 * time.Second

const (
	mutterDest            = "org.gnome.Mutter.ScreenCast"
	mutterPath            = "/org/gnome/Mutter/ScreenCast"
	mutterScreenCast      = "org.gnome.Mutter.ScreenCast"
	mutterSessionIface    = "org.gnome.Mutter.ScreenCast.Session"
	mutterStreamIface     = "org.gnome.Mutter.ScreenCast.Stream"
	portalDest            = "org.freedesktop.portal.Desktop"
	portalPath            = "/org/freedesktop/portal/desktop"
	portalScreenCast      = "org.freedesktop.portal.ScreenCast"
	portalRequestIface    = "org.freedesktop.portal.Request"
	restoreSessionFileRel = ".config/barahn/wayland_screencast_session" // #nosec G101
)

// waylandCapturer implements Capturer on Wayland via XDG Desktop Portal ScreenCast and PipeWire.
type waylandCapturer struct {
	config   CaptureConfig
	bus      *dbus.Conn
	session  dbus.ObjectPath
	isMutter bool
	nodeID   uint32
	// sessionKind records which negotiation produced nodeID: "mutter" or
	// "portal". They behave differently downstream, so the logs must say which.
	sessionKind string
	// pipewireFD is the restricted PipeWire connection returned by
	// OpenPipeWireRemote. The portal's node id is only valid on it.
	pipewireFD *os.File
	width      int
	height     int

	frames    chan *Frame
	running   atomic.Bool
	seqNum    atomic.Uint64
	stopOnce  sync.Once
	closeOnce sync.Once
	cancel    context.CancelFunc

	cmd    *exec.Cmd
	pipeIn io.ReadCloser
	mu     sync.Mutex
}

// logf records which capture path was taken. This file used to have no logging
// at all, which made a black screen indistinguishable from a working capture of
// a static desktop: the frame counter increments identically either way.
func (c *waylandCapturer) logf(format string, args ...interface{}) {
	log.Printf("[WaylandCapture] "+format, args...)
}

// releasePipeWireFD closes the portal's PipeWire descriptor. GStreamer inherits a
// duplicate, so closing here does not disturb a running pipeline.
func (c *waylandCapturer) releasePipeWireFD() {
	if c.pipewireFD != nil {
		_ = c.pipewireFD.Close()
		c.pipewireFD = nil
	}
}

func (c *waylandCapturer) closeFrames() {
	c.closeOnce.Do(func() {
		if c.frames != nil {
			close(c.frames)
		}
	})
}

// newWaylandCapturer initializes a ScreenCast portal session on Wayland.
func newWaylandCapturer(config CaptureConfig) (*waylandCapturer, error) {
	if config.FrameBufferSize <= 0 {
		config.FrameBufferSize = 2
	}
	if config.TargetFPS <= 0 {
		config.TargetFPS = 30
	}
	if config.DisplayIndex < 0 {
		config.DisplayIndex = 0
	}

	bus, err := dbus.SessionBus()
	if err != nil {
		return nil, fmt.Errorf("wayland: cannot connect to D-Bus session bus: %w", err)
	}

	c := &waylandCapturer{
		config: config,
		bus:    bus,
		width:  1920,
		height: 1080,
	}

	return c, nil
}

func (c *waylandCapturer) Displays() ([]Display, error) {
	// Under Wayland, displays are dynamic per PipeWire stream or negotiated via Portal.
	return []Display{
		{
			Index:   0,
			Name:    "Wayland Display (PipeWire)",
			Bounds:  image.Rect(0, 0, c.width, c.height),
			Primary: true,
		},
	}, nil
}

func (c *waylandCapturer) Start(ctx context.Context) error {
	if c.running.Swap(true) {
		return ErrAlreadyStarted
	}

	c.frames = make(chan *Frame, c.config.FrameBufferSize)
	c.stopOnce = sync.Once{}
	c.closeOnce = sync.Once{}
	captureCtx, cancel := context.WithCancel(ctx)
	c.cancel = cancel

	// Initialize portal session and PipeWire stream with a quick timeout
	portalCtx, portalCancel := context.WithTimeout(captureCtx, portalNegotiationTimeout)
	defer portalCancel()

	if err := c.initPortalSession(portalCtx); err != nil || c.nodeID == 0 {
		if err != nil {
			c.logf("session negotiation failed: %v", err)
		} else {
			c.logf("session negotiation returned no PipeWire node id")
		}
		if errors.Is(portalCtx.Err(), context.DeadlineExceeded) {
			c.logf("negotiation hit its %s deadline - note the portal dialog waits for a human to click Share, which takes longer than that",
				portalNegotiationTimeout)
		}
		if !c.fallbackToX11(captureCtx) {
			// Leave the capturer retryable and let the caller see the failure.
			// Returning nil here handed back a closed frame channel that read
			// as a healthy start, so pkg/tunnel never sent capture_unavailable
			// and the operator watched a spinner forever.
			c.running.Store(false)
			cancel()
			return fmt.Errorf("screen: no capture backend available on this %s session", DetectDisplayServer())
		}
		return nil
	}

	c.logf("capturing via %s, node id %d, %dx%d", c.sessionKind, c.nodeID, c.width, c.height)
	go c.streamLoop(captureCtx)
	return nil
}

func (c *waylandCapturer) Frames() <-chan *Frame {
	return c.frames
}

func (c *waylandCapturer) Stop() {
	c.stopOnce.Do(func() {
		c.running.Store(false)
		if c.cancel != nil {
			c.cancel()
		}
		c.mu.Lock()
		c.releasePipeWireFD()
		if c.cmd != nil && c.cmd.Process != nil {
			_ = c.cmd.Process.Kill()
		}
		if c.pipeIn != nil {
			_ = c.pipeIn.Close()
		}
		if c.session != "" && c.bus != nil {
			stopCtx, stopCancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer stopCancel()
			// Close portal / mutter session with short timeout
			if c.isMutter {
				_ = c.bus.Object(mutterDest, c.session).CallWithContext(stopCtx, mutterSessionIface+".Stop", 0).Store()
			} else {
				_ = c.bus.Object(portalDest, c.session).CallWithContext(stopCtx, "org.freedesktop.portal.Session.Close", 0).Store()
			}
		}
		c.mu.Unlock()
		c.closeFrames()
	})
}

func (c *waylandCapturer) SetDisplay(index int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if index < 0 || index > 0 {
		return ErrDisplayNotFound
	}
	c.config.DisplayIndex = index
	return nil
}

func getRestoreTokenPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, restoreSessionFileRel)
}

func readRestoreToken() string {
	p := getRestoreTokenPath()
	if p == "" {
		return ""
	}
	cleanPath := filepath.Clean(p)
	data, err := os.ReadFile(cleanPath) // #nosec G304 -- path resolved from user home config
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// SaveRestoreToken persists the portal session restore token.
func SaveRestoreToken(token string) {
	if token == "" {
		return
	}
	p := getRestoreTokenPath()
	if p == "" {
		return
	}
	cleanPath := filepath.Clean(p)
	_ = os.MkdirAll(filepath.Dir(cleanPath), 0700)
	_ = os.WriteFile(cleanPath, []byte(token), 0600)
}

// awaitPortalResponse blocks until the portal answers the given Request with a
// Response signal and returns its results.
//
// Every xdg-desktop-portal method returns a Request object path, not its result:
// the result arrives asynchronously on that Request. Treating the returned path
// as the result is what produced "Invalid session" here for as long as Wayland
// capture has existed - the session handle passed to SelectSources and Start was
// a request path, and the portal was right to refuse it.
func (c *waylandCapturer) awaitPortalResponse(ctx context.Context, sigChan <-chan *dbus.Signal, request dbus.ObjectPath) (map[string]dbus.Variant, error) {
	for {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("portal did not answer request %s: %w", request, ctx.Err())
		case sig := <-sigChan:
			if sig == nil || sig.Path != request || sig.Name != portalRequestIface+".Response" {
				continue
			}
			if len(sig.Body) < 2 {
				return nil, fmt.Errorf("malformed Response on %s", request)
			}
			code, _ := sig.Body[0].(uint32)
			results, _ := sig.Body[1].(map[string]dbus.Variant)
			switch code {
			case 0:
				return results, nil
			case 1:
				return nil, fmt.Errorf("the user dismissed the screen-share dialog")
			default:
				return nil, fmt.Errorf("portal refused request %s (response code %d)", request, code)
			}
		}
	}
}

func (c *waylandCapturer) initPortalSession(ctx context.Context) error {
	// 1. Try GNOME Mutter native ScreenCast first (direct Mutter Monitor recording)
	if err := c.initMutterSession(ctx); err == nil && c.nodeID > 0 {
		c.sessionKind = "mutter"
		c.logf("Mutter ScreenCast negotiated, node id %d", c.nodeID)
		return nil
	} else if err != nil {
		c.logf("Mutter ScreenCast unavailable (%v), falling through to xdg-desktop-portal", err)
	} else {
		c.logf("Mutter ScreenCast returned no node id, falling through to xdg-desktop-portal")
	}

	// initMutterSession sets c.session and c.isMutter as soon as CreateSession
	// succeeds, before RecordMonitor and the node-id lookup that can still fail.
	// Both fall-through paths above therefore arrive here with a live Mutter
	// session on the compositor, and the portal path below overwrites c.session
	// with its own handle - after which Stop() closes the portal session and
	// never the Mutter one. GNOME Shell then holds that session, and its
	// screen-capture grant, until this process exits. Close it before the
	// handle is lost.
	c.releaseMutterSession(ctx)

	// 2. XDG Desktop Portal ScreenCast.
	obj := c.bus.Object(portalDest, dbus.ObjectPath(portalPath))

	// Subscribe before the first call: the portal may answer before we would
	// otherwise be listening.
	if err := c.bus.AddMatchSignal(
		dbus.WithMatchInterface(portalRequestIface),
		dbus.WithMatchMember("Response"),
	); err != nil {
		return fmt.Errorf("subscribing to portal Response signals: %w", err)
	}
	defer func() {
		_ = c.bus.RemoveMatchSignal(
			dbus.WithMatchInterface(portalRequestIface),
			dbus.WithMatchMember("Response"),
		)
	}()

	sigChan := make(chan *dbus.Signal, 16)
	c.bus.Signal(sigChan)
	defer c.bus.RemoveSignal(sigChan)

	token := func(prefix string) string {
		return fmt.Sprintf("barahn_%s_%d", prefix, time.Now().UnixNano())
	}

	// 2a. CreateSession. The session handle comes back in the Response results.
	var request dbus.ObjectPath
	if err := obj.CallWithContext(ctx, portalScreenCast+".CreateSession", 0, map[string]dbus.Variant{
		"session_handle_token": dbus.MakeVariant(token("session")),
		"handle_token":         dbus.MakeVariant(token("create")),
	}).Store(&request); err != nil {
		return fmt.Errorf("CreateSession call failed: %w", err)
	}
	results, err := c.awaitPortalResponse(ctx, sigChan, request)
	if err != nil {
		return fmt.Errorf("CreateSession: %w", err)
	}
	handle, ok := results["session_handle"].Value().(string)
	if !ok || handle == "" {
		return fmt.Errorf("CreateSession returned no session_handle")
	}
	c.session = dbus.ObjectPath(handle)
	c.isMutter = false
	c.sessionKind = "portal"
	c.logf("portal session created: %s", c.session)

	// 2b. SelectSources.
	selectOptions := map[string]dbus.Variant{
		"types":        dbus.MakeVariant(uint32(1)), // Monitor
		"multiple":     dbus.MakeVariant(false),
		"cursor_mode":  dbus.MakeVariant(uint32(2)), // Embedded cursor
		"handle_token": dbus.MakeVariant(token("sources")),
	}
	if tok := readRestoreToken(); tok != "" {
		selectOptions["restore_token"] = dbus.MakeVariant(tok)
	}
	if err := obj.CallWithContext(ctx, portalScreenCast+".SelectSources", 0, c.session, selectOptions).Store(&request); err != nil {
		return fmt.Errorf("SelectSources call failed: %w", err)
	}
	if _, err := c.awaitPortalResponse(ctx, sigChan, request); err != nil {
		return fmt.Errorf("SelectSources: %w", err)
	}

	// 2c. Start. This is where the consent dialog appears, so it can block for
	// as long as a person takes to answer.
	c.logf("portal Start called - waiting for the user to accept the screen-share dialog")
	if err := obj.CallWithContext(ctx, portalScreenCast+".Start", 0, c.session, "", map[string]dbus.Variant{
		"handle_token": dbus.MakeVariant(token("start")),
	}).Store(&request); err != nil {
		return fmt.Errorf("Start call failed: %w", err)
	}
	results, err = c.awaitPortalResponse(ctx, sigChan, request)
	if err != nil {
		return fmt.Errorf("Start: %w", err)
	}
	if streams, exists := results["streams"]; exists {
		c.parseStreamsVariant(streams)
	}
	if tokVar, exists := results["restore_token"]; exists {
		if tok, ok := tokVar.Value().(string); ok {
			SaveRestoreToken(tok)
		}
	}
	if c.nodeID == 0 {
		return fmt.Errorf("Start returned no PipeWire node id")
	}

	// 2d. OpenPipeWireRemote. The node id is only addressable on the restricted
	// PipeWire connection this returns; on the session's default connection it
	// does not exist, which is why pipewiresrc produced no buffers.
	var fd dbus.UnixFD
	if err := obj.CallWithContext(ctx, portalScreenCast+".OpenPipeWireRemote", 0, c.session, map[string]dbus.Variant{}).Store(&fd); err != nil {
		return fmt.Errorf("OpenPipeWireRemote call failed: %w", err)
	}
	c.pipewireFD = os.NewFile(uintptr(fd), "pipewire-remote")
	if c.pipewireFD == nil {
		return fmt.Errorf("OpenPipeWireRemote returned an unusable descriptor")
	}
	c.logf("portal negotiated: node id %d on a dedicated PipeWire fd", c.nodeID)

	return nil
}

// parseStreamsVariant reads the PipeWire node id and geometry out of the
// portal's `streams` result, whose signature is a(ua{sv}) - an array of
// (node_id, properties) structs.
//
// The concrete Go type godbus produces for that is [][]interface{}, since it
// represents a struct as []interface{}. The previous implementation matched
// [][2]interface{} and []interface{} and therefore matched nothing, leaving
// nodeID at zero after a Start the portal had answered successfully. Reflection
// is used rather than a type switch so a different-but-equivalent shape does
// not silently fall through again.
func (c *waylandCapturer) parseStreamsVariant(streamsVar dbus.Variant) {
	outer := reflect.ValueOf(streamsVar.Value())
	if !isSequence(outer) || outer.Len() == 0 {
		c.logf("portal returned a streams value this code cannot read (%T)", streamsVar.Value())
		return
	}

	for i := 0; i < outer.Len(); i++ {
		entry := reflect.ValueOf(deref(outer.Index(i)))
		if !isSequence(entry) || entry.Len() < 2 {
			continue
		}

		id, ok := deref(entry.Index(0)).(uint32)
		if !ok {
			continue
		}
		c.nodeID = id

		if props, ok := deref(entry.Index(1)).(map[string]dbus.Variant); ok {
			c.extractDimensions(props)
		}
		return // first stream wins; SelectSources asked for a single monitor
	}

	c.logf("portal returned %d stream(s) but none carried a usable node id", outer.Len())
}

// isSequence reports whether v is indexable. A D-Bus struct can arrive as a
// slice or as a fixed-size array depending on how it was decoded, and checking
// only for Slice is what let the previous parser miss the real shape.
func isSequence(v reflect.Value) bool {
	return v.IsValid() && (v.Kind() == reflect.Slice || v.Kind() == reflect.Array)
}

// deref unwraps interface values so reflection sees the concrete type.
func deref(v reflect.Value) interface{} {
	for v.Kind() == reflect.Interface {
		v = v.Elem()
	}
	if !v.IsValid() {
		return nil
	}
	return v.Interface()
}

func (c *waylandCapturer) extractDimensions(props map[string]dbus.Variant) {
	if sizeVar, exists := props["size"]; exists {
		if sizeSlice, ok := sizeVar.Value().([]int32); ok && len(sizeSlice) == 2 {
			if sizeSlice[0] > 0 && sizeSlice[1] > 0 {
				c.width = int(sizeSlice[0])
				c.height = int(sizeSlice[1])
			}
		}
	}
}

// releaseMutterSession stops a Mutter ScreenCast session that initMutterSession
// created but could not carry to a usable node id. It is a no-op when there is
// nothing to release, so callers need not check first.
func (c *waylandCapturer) releaseMutterSession(ctx context.Context) {
	if !c.isMutter || c.session == "" {
		return
	}
	stopCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	if err := c.bus.Object(mutterDest, c.session).CallWithContext(stopCtx, mutterSessionIface+".Stop", 0).Store(); err != nil {
		c.logf("could not stop the partially negotiated Mutter session: %v", err)
	}
	c.session = ""
	c.isMutter = false
}

func (c *waylandCapturer) initMutterSession(ctx context.Context) error {
	obj := c.bus.Object(mutterDest, dbus.ObjectPath(mutterPath))
	var sessionPath dbus.ObjectPath
	createOpts := map[string]dbus.Variant{}
	err := obj.CallWithContext(ctx, mutterScreenCast+".CreateSession", 0, createOpts).Store(&sessionPath)
	if err != nil {
		return err
	}
	c.session = sessionPath
	c.isMutter = true

	sessObj := c.bus.Object(mutterDest, sessionPath)
	var streamPath dbus.ObjectPath
	recordOpts := map[string]dbus.Variant{
		"cursor-mode": dbus.MakeVariant(uint32(1)), // Embedded cursor
	}

	// Prioritize RecordMonitor to record user's physical screen
	err = sessObj.CallWithContext(ctx, mutterSessionIface+".RecordMonitor", 0, "", recordOpts).Store(&streamPath)
	if err != nil {
		// Fallback to RecordVirtual only if RecordMonitor is not supported
		err = sessObj.CallWithContext(ctx, mutterSessionIface+".RecordVirtual", 0, recordOpts).Store(&streamPath)
	}
	if err != nil {
		return err
	}

	_ = sessObj.CallWithContext(ctx, mutterSessionIface+".Start", 0).Store()

	streamObj := c.bus.Object(mutterDest, streamPath)
	if propVal, propErr := streamObj.GetProperty(mutterStreamIface + ".Parameters"); propErr == nil {
		if params, ok := propVal.Value().(map[string]dbus.Variant); ok {
			if nodeIDVar, exists := params["pipewire-node-id"]; exists {
				if id, ok := nodeIDVar.Value().(uint32); ok {
					c.nodeID = id
				}
			}
			if sizeVar, exists := params["size"]; exists {
				if sizeSlice, ok := sizeVar.Value().([]int32); ok && len(sizeSlice) == 2 {
					if sizeSlice[0] > 0 && sizeSlice[1] > 0 {
						c.width = int(sizeSlice[0])
						c.height = int(sizeSlice[1])
					}
				}
			}
		}
	}
	return nil
}

func (c *waylandCapturer) streamLoop(ctx context.Context) {
	defer c.closeFrames()
	gstPath, err := exec.LookPath("gst-launch-1.0")
	if err != nil {
		c.logf("gst-launch-1.0 not found - no screen capture is possible (install gstreamer1.0-tools and gstreamer1.0-pipewire)")
		return
	}

	// GStreamer pipeline to pull frames from PipeWire into raw RGBA stdout
	var args []string
	if c.pipewireFD != nil && c.nodeID > 0 {
		// ExtraFiles[0] lands as fd 3 in the child. The portal's node is only
		// reachable on that connection, so both parts are required.
		args = []string{
			"-q",
			"pipewiresrc", "fd=3", fmt.Sprintf("path=%d", c.nodeID), "do-timestamp=true", "keepalive-time=1000",
			"!", "videoconvert",
			"!", fmt.Sprintf("video/x-raw,format=RGBA,width=%d,height=%d,framerate=%d/1", c.width, c.height, c.config.TargetFPS),
			"!", "fdsink", "fd=1",
		}
	} else if c.nodeID > 0 {
		args = []string{
			"-q",
			"pipewiresrc", fmt.Sprintf("target-object=%d", c.nodeID), "do-timestamp=true", "keepalive-time=1000",
			"!", "videoconvert",
			"!", fmt.Sprintf("video/x-raw,format=RGBA,width=%d,height=%d,framerate=%d/1", c.width, c.height, c.config.TargetFPS),
			"!", "fdsink", "fd=1",
		}
	} else {
		args = []string{
			"-q",
			"pipewiresrc", "do-timestamp=true", "keepalive-time=1000",
			"!", "videoconvert",
			"!", fmt.Sprintf("video/x-raw,format=RGBA,width=%d,height=%d,framerate=%d/1", c.width, c.height, c.config.TargetFPS),
			"!", "fdsink", "fd=1",
		}
	}

	c.logf("starting GStreamer: %s %s", gstPath, strings.Join(args, " "))
	cmd := exec.CommandContext(ctx, gstPath, args...) // #nosec G204 -- gstPath resolved via exec.LookPath
	if c.pipewireFD != nil {
		cmd.ExtraFiles = []*os.File{c.pipewireFD}
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		c.logf("could not open the GStreamer stdout pipe: %v", err)
		// Mid-stream: Start has already returned, so the closed frame channel
		// is the only signal available to the consumer.
		_ = c.fallbackToX11(ctx)
		return
	}

	c.mu.Lock()
	c.cmd = cmd
	c.pipeIn = stdout
	c.mu.Unlock()

	if err := cmd.Start(); err != nil {
		c.logf("GStreamer failed to start: %v", err)
		_ = c.fallbackToX11(ctx)
		return
	}

	frameSize := c.width * c.height * 4
	bufReader := bufio.NewReaderSize(stdout, frameSize*2)
	frameBuf := make([]byte, frameSize)
	framesDelivered := 0

	targetInterval := time.Second / time.Duration(c.config.TargetFPS)
	ticker := time.NewTicker(targetInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_, err := io.ReadFull(bufReader, frameBuf)
			if err != nil {
				if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
					c.logf("GStreamer produced no frames (%v) after %d delivered - the PipeWire node was never readable", err, framesDelivered)
					_ = c.fallbackToX11(ctx)
					return
				}
				continue
			}

			if framesDelivered == 0 {
				c.logf("first real frame received from PipeWire (%dx%d) - this is the live screen", c.width, c.height)
			}
			framesDelivered++

			img := &image.RGBA{
				Pix:    make([]byte, frameSize),
				Stride: c.width * 4,
				Rect:   image.Rect(0, 0, c.width, c.height),
			}
			copy(img.Pix, frameBuf)

			frame := &Frame{
				Image:        img,
				Bounds:       img.Rect,
				DisplayIndex: c.config.DisplayIndex,
				CapturedAt:   time.Now(),
				SequenceNum:  c.seqNum.Add(1),
			}

			select {
			case c.frames <- frame:
			default:
				// Drop frame if consumer buffer is full
			}
		}
	}
}

// fallbackToX11 reports whether it established a frame source. False means the
// frame channel has been closed and no frames will ever arrive on it, which
// callers must not mistake for a working capture: Start returning nil in that
// case is what let a closed channel look like a live session, so the
// "capture unavailable" notice never reached the viewer.
func (c *waylandCapturer) fallbackToX11(ctx context.Context) bool {
	// Under Wayland this fallback is worse than none. Xwayland is running for
	// X11 app compatibility, so an X11 capturer connects, reports the right
	// geometry and grabs the root window - which the compositor never draws
	// native Wayland surfaces into. The capture succeeds and returns nothing,
	// which is how blank frames were streamed for weeks while every layer
	// reported success. A failure that announces itself beats one that does not.
	if DetectDisplayServer() == DisplayServerWayland {
		c.logf("not falling back to X11: this is a Wayland session, where an X11 grab returns a blank screen rather than failing")
		c.closeFrames()
		return false
	}

	if xcap, xerr := newX11Capturer(c.config); xerr == nil {
		if err := xcap.Start(ctx); err == nil {
			// Under a Wayland session this usually captures nothing useful:
			// Xwayland does not composite the Wayland desktop into the X root
			// window, so the grab is an empty image at the right geometry.
			c.logf("falling back to X11 capture - under Wayland this typically yields a BLANK screen, not the desktop")
			go func() {
				defer c.closeFrames()
				defer xcap.Stop()
				for {
					select {
					case <-ctx.Done():
						return
					case f, ok := <-xcap.Frames():
						if !ok {
							return
						}
						if !c.running.Load() {
							return
						}
						select {
						case <-ctx.Done():
							return
						case c.frames <- f:
						}
					}
				}
			}()
			return true
		}
	}
	c.logf("no capture backend available - closing the frame channel instead of inventing frames")
	c.closeFrames()
	return false
}
