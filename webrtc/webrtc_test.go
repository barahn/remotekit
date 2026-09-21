// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package webrtc

import (
	"testing"
	"time"
)

func TestSignalMessage_EncodeDecode(t *testing.T) {
	msg := &SignalMessage{
		Type:      SignalOffer,
		SessionID: "sess-12345",
		TargetID:  "agent-987",
		SDP:       "v=0\r\no=- 12345 2 IN IP4 127.0.0.1\r\n",
	}

	encoded, err := msg.Encode()
	if err != nil {
		t.Fatalf("Encode failed: %v", err)
	}

	decoded, err := DecodeSignalMessage(encoded)
	if err != nil {
		t.Fatalf("DecodeSignalMessage failed: %v", err)
	}

	if decoded.Type != SignalOffer {
		t.Errorf("expected type SignalOffer, got %s", decoded.Type)
	}
	if decoded.SessionID != "sess-12345" {
		t.Errorf("expected SessionID sess-12345, got %s", decoded.SessionID)
	}
	if decoded.SDP != msg.SDP {
		t.Errorf("expected SDP %s, got %s", msg.SDP, decoded.SDP)
	}
}

func TestPeerSession_OfferAnswerExchange(t *testing.T) {
	config := DefaultPeerConfig()

	// Create Offerer (Agent)
	offerer, err := NewPeerSession(config)
	if err != nil {
		t.Fatalf("failed to create offerer: %v", err)
	}
	defer offerer.Close()

	// Add video track to offerer
	if err := offerer.CreateVideoTrack("screen-stream", "video-track-1"); err != nil {
		t.Fatalf("CreateVideoTrack failed: %v", err)
	}

	// Create Answerer (Browser / Control Plane)
	answerer, err := NewPeerSession(config)
	if err != nil {
		t.Fatalf("failed to create answerer: %v", err)
	}
	defer answerer.Close()

	// 1. Offerer creates SDP Offer
	offerSDP, err := offerer.CreateOffer()
	if err != nil {
		t.Fatalf("CreateOffer failed: %v", err)
	}
	if offerSDP == "" {
		t.Fatal("offerSDP is empty")
	}

	// 2. Answerer accepts Offer and creates SDP Answer
	answerSDP, err := answerer.CreateAnswer(offerSDP)
	if err != nil {
		t.Fatalf("CreateAnswer failed: %v", err)
	}
	if answerSDP == "" {
		t.Fatal("answerSDP is empty")
	}

	// 3. Offerer applies remote Answer
	if err := offerer.SetRemoteAnswer(answerSDP); err != nil {
		t.Fatalf("SetRemoteAnswer failed: %v", err)
	}

	t.Log("Offer/Answer exchange completed successfully")
}

func TestPeerSession_WriteVideoSample(t *testing.T) {
	config := DefaultPeerConfig()
	peer, err := NewPeerSession(config)
	if err != nil {
		t.Fatalf("NewPeerSession failed: %v", err)
	}
	defer peer.Close()

	if err := peer.CreateVideoTrack("stream", "track"); err != nil {
		t.Fatalf("CreateVideoTrack failed: %v", err)
	}

	// Write mock VP8 frame sample
	mockVP8Frame := []byte{0x10, 0x02, 0x00, 0x9d, 0x01, 0x2a}
	if err := peer.WriteVideoSample(mockVP8Frame, 33*time.Millisecond); err != nil {
		t.Fatalf("WriteVideoSample failed: %v", err)
	}
}
