// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package webrtc

import (
	"reflect"
	"testing"
)

func TestDecodeSignalMessage(t *testing.T) {
	tests := []struct {
		name    string
		data    []byte
		want    *SignalMessage
		wantErr bool
	}{
		{
			name: "Valid Offer",
			data: []byte(`{"type":"offer","session_id":"sess-123","sdp":"v=0\r\n"}`),
			want: &SignalMessage{
				Type:      SignalOffer,
				SessionID: "sess-123",
				SDP:       "v=0\r\n",
			},
			wantErr: false,
		},
		{
			name: "Valid Candidate",
			data: []byte(`{"type":"candidate","candidate":"candidate:1 1 UDP 2130706431 127.0.0.1 53965 typ host"}`),
			want: &SignalMessage{
				Type:      SignalCandidate,
				Candidate: "candidate:1 1 UDP 2130706431 127.0.0.1 53965 typ host",
			},
			wantErr: false,
		},
		{
			name:    "Empty Data",
			data:    []byte(""),
			want:    nil,
			wantErr: true,
		},
		{
			name:    "Invalid JSON",
			data:    []byte("{invalid json}"),
			want:    nil,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := DecodeSignalMessage(tt.data)
			if (err != nil) != tt.wantErr {
				t.Errorf("DecodeSignalMessage() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("DecodeSignalMessage() got = %v, want %v", got, tt.want)
			}
		})
	}
}
