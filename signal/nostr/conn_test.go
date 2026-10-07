// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package nostr

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func listen(t *testing.T, cfg Config) *Conn {
	t.Helper()
	c, err := Listen(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func dial(t *testing.T, cfg Config, peer string) *Conn {
	t.Helper()
	c, err := Dial(context.Background(), cfg, peer)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

type readResult struct {
	msg []byte
	err error
}

// readWithin returns the next message on c, or fails the test after wait.
func readWithin(t *testing.T, c *Conn, wait time.Duration) []byte {
	t.Helper()
	ch := make(chan readResult, 1)
	go func() {
		m, err := c.ReadMessage()
		ch <- readResult{m, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatalf("ReadMessage: %v", r.err)
		}
		return r.msg
	case <-time.After(wait):
		t.Fatal("no message arrived")
		return nil
	}
}

// nothingWithin fails the test if a message arrives on c within wait. The
// read it starts is left behind; the test's cleanup closes c and ends it.
func nothingWithin(t *testing.T, c *Conn, wait time.Duration) {
	t.Helper()
	select {
	case m := <-c.in:
		t.Fatalf("unexpected message: %s", m)
	case <-time.After(wait):
	}
}

func fields(t *testing.T, msg []byte) map[string]any {
	t.Helper()
	var f map[string]any
	if err := json.Unmarshal(msg, &f); err != nil {
		t.Fatalf("%s: %v", msg, err)
	}
	return f
}

// A viewer reaches an agent through two relays: the message arrives once,
// labelled with the viewer's key, and the reply finds its way back.
func TestConnRoundTripOverTwoRelays(t *testing.T) {
	r1, r2 := newFakeRelay(t), newFakeRelay(t)
	relays := []string{r1.url(), r2.url()}
	agent := listen(t, Config{Relays: relays})
	viewer := dial(t, Config{Relays: relays}, agent.PublicKey())

	// A viewer_id the viewer chose for itself is overwritten: the agent
	// knows viewers by the key that sealed the message.
	if err := viewer.WriteMessage([]byte(`{"type":"session_start","viewer_id":"someone-else"}`)); err != nil {
		t.Fatal(err)
	}
	got := fields(t, readWithin(t, agent, 2*time.Second))
	if got["type"] != "session_start" || got["viewer_id"] != viewer.PublicKey() {
		t.Fatalf("agent got %v, want session_start from %s", got, viewer.PublicKey())
	}
	nothingWithin(t, agent, 200*time.Millisecond)

	if r1.count() != 1 || r2.count() != 1 {
		t.Errorf("relays got %d and %d events, want one each", r1.count(), r2.count())
	}

	reply, _ := json.Marshal(map[string]string{"type": "answer", "viewer_id": viewer.PublicKey()})
	if err := agent.WriteMessage(reply); err != nil {
		t.Fatal(err)
	}
	if f := fields(t, readWithin(t, viewer, 2*time.Second)); f["type"] != "answer" {
		t.Fatalf("viewer got %v", f)
	}
}

// With two viewers, a reply goes only to the one it names, and a message that
// names none goes to both.
func TestConnRoutesRepliesByViewer(t *testing.T) {
	relay := newFakeRelay(t)
	cfg := Config{Relays: []string{relay.url()}}
	agent := listen(t, cfg)
	v1 := dial(t, cfg, agent.PublicKey())
	v2 := dial(t, cfg, agent.PublicKey())

	for _, v := range []*Conn{v1, v2} {
		if err := v.WriteMessage([]byte(`{"type":"session_start"}`)); err != nil {
			t.Fatal(err)
		}
		readWithin(t, agent, 2*time.Second)
	}

	reply, _ := json.Marshal(map[string]string{"type": "answer", "viewer_id": v2.PublicKey()})
	if err := agent.WriteMessage(reply); err != nil {
		t.Fatal(err)
	}
	readWithin(t, v2, 2*time.Second)
	nothingWithin(t, v1, 200*time.Millisecond)

	if err := agent.WriteMessage([]byte(`{"type":"relay_mode","active":true}`)); err != nil {
		t.Fatal(err)
	}
	readWithin(t, v1, 2*time.Second)
	readWithin(t, v2, 2*time.Second)

	stranger, _ := GenerateKey()
	reply, _ = json.Marshal(map[string]string{"type": "answer", "viewer_id": stranger.PublicKey()})
	if err := agent.WriteMessage(reply); !errors.Is(err, ErrUnknownViewer) {
		t.Fatalf("reply to a viewer never heard from: %v, want ErrUnknownViewer", err)
	}
}

// Past MaxViewers, new senders are ignored; known ones still get through.
func TestConnCapsViewers(t *testing.T) {
	relay := newFakeRelay(t)
	agent := listen(t, Config{Relays: []string{relay.url()}, MaxViewers: 1})
	cfg := Config{Relays: []string{relay.url()}}
	v1 := dial(t, cfg, agent.PublicKey())
	v2 := dial(t, cfg, agent.PublicKey())

	_ = v1.WriteMessage([]byte(`{"type":"a"}`))
	readWithin(t, agent, 2*time.Second)
	_ = v2.WriteMessage([]byte(`{"type":"b"}`))
	nothingWithin(t, agent, 300*time.Millisecond)
	_ = v1.WriteMessage([]byte(`{"type":"c"}`))
	if f := fields(t, readWithin(t, agent, 2*time.Second)); f["type"] != "c" {
		t.Fatalf("got %v, want the known viewer's message", f)
	}
}

// A dialled conn reads only its peer.
func TestDialIgnoresOtherSenders(t *testing.T) {
	relay := newFakeRelay(t)
	cfg := Config{Relays: []string{relay.url()}}
	agent := listen(t, cfg)
	viewer := dial(t, cfg, agent.PublicKey())

	impostor, _ := GenerateKey()
	gift, err := wrap(`{"type":"answer"}`, impostor, viewer.PublicKey(), time.Now(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	relay.inject(gift)
	nothingWithin(t, viewer, 300*time.Millisecond)
}

// A relay that replays a wrap, or sends one it should not have, delivers
// nothing: duplicates are dropped and forgeries fail to open.
func TestConnDropsReplaysAndForgeries(t *testing.T) {
	relay := newFakeRelay(t)
	cfg := Config{Relays: []string{relay.url()}}
	agent := listen(t, cfg)
	viewer, _ := GenerateKey()

	gift, _ := wrap(`{"type":"session_start"}`, viewer, agent.PublicKey(), time.Now(), time.Minute)
	relay.inject(gift)
	readWithin(t, agent, 2*time.Second)
	relay.inject(gift)
	nothingWithin(t, agent, 200*time.Millisecond)

	forged := *gift
	forged.ID = ""
	forged.Content = gift.Content[:len(gift.Content)-4] + "AAAA"
	forged.setID(viewer) // a valid ID, but the signature is still the old one
	relay.inject(&forged)
	nothingWithin(t, agent, 200*time.Millisecond)
}

func TestConnRefusesOversizedMessages(t *testing.T) {
	relay := newFakeRelay(t)
	cfg := Config{Relays: []string{relay.url()}}
	agent := listen(t, cfg)
	viewer := dial(t, cfg, agent.PublicKey())
	big := make([]byte, 0, 70_000)
	big = append(big, `{"type":"frame","data":"`...)
	for len(big) < 69_000 {
		big = append(big, 'x')
	}
	big = append(big, `"}`...)
	if err := viewer.WriteMessage(big); !errors.Is(err, ErrMessageTooLarge) {
		t.Fatalf("want ErrMessageTooLarge, got %v", err)
	}
	if relay.count() != 0 {
		t.Fatal("an oversized message reached the relay")
	}
}

func TestCloseUnblocksRead(t *testing.T) {
	relay := newFakeRelay(t)
	c := listen(t, Config{Relays: []string{relay.url()}})
	errc := make(chan error, 1)
	go func() {
		_, err := c.ReadMessage()
		errc <- err
	}()
	time.Sleep(50 * time.Millisecond)
	_ = c.Close()
	_ = c.Close()
	select {
	case err := <-errc:
		if !errors.Is(err, ErrClosed) {
			t.Fatalf("want ErrClosed, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not unblock ReadMessage")
	}
	if err := c.WriteMessage([]byte(`{}`)); !errors.Is(err, ErrClosed) {
		t.Fatalf("write after close: %v", err)
	}
}

// Losing one relay is survivable; losing the last ends the conn.
func TestConnEndsWithItsLastRelay(t *testing.T) {
	r1, r2 := newFakeRelay(t), newFakeRelay(t)
	c := listen(t, Config{Relays: []string{r1.url(), r2.url()}})

	r1.close()
	time.Sleep(100 * time.Millisecond)
	select {
	case <-c.done:
		t.Fatal("conn ended with a relay still up")
	default:
	}

	r2.closeSubscriptions()
	select {
	case <-c.done:
	case <-time.After(2 * time.Second):
		t.Fatal("conn outlived its last relay")
	}
}

func TestOpenNeedsARelay(t *testing.T) {
	dead := newFakeRelay(t)
	url := dead.url()
	dead.close()
	if _, err := Listen(context.Background(), Config{Relays: []string{url}}); !errors.Is(err, ErrNoRelays) {
		t.Fatalf("want ErrNoRelays, got %v", err)
	}
	if _, err := Dial(context.Background(), Config{}, "not-a-key"); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("want ErrInvalidKey, got %v", err)
	}
}
