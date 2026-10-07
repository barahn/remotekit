// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package nostr

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"math/big"
	"strconv"
	"time"
)

// wrapJitter is how far into the past NIP-59 backdates a seal's and a wrap's
// created_at, so that their timestamps say nothing about when the message was
// written. The rumor inside keeps the real time.
const wrapJitter = 2 * 24 * time.Hour

var (
	errNotForUs     = errors.New("nostr: gift wrap not addressed to this key")
	errBadSeal      = errors.New("nostr: malformed seal")
	errSenderSpoof  = errors.New("nostr: rumor author is not the seal's signer")
	errWrongKind    = errors.New("nostr: unexpected event kind")
	errStaleMessage = errors.New("nostr: message outside the freshness window")
)

// backdate returns now moved a random amount, up to wrapJitter, into the past.
func backdate(now time.Time) (int64, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(int64(wrapJitter/time.Second)))
	if err != nil {
		return 0, err
	}
	return now.Unix() - n.Int64(), nil
}

// wrap gift-wraps content from sender to recipient: rumor, sealed by sender,
// wrapped under a one-time key. The wrap expires (NIP-40) at now+ttl.
func wrap(content string, sender *SecretKey, recipient string, now time.Time, ttl time.Duration) (*Event, error) {
	recipientPub, err := parsePublicKey(recipient)
	if err != nil {
		return nil, err
	}

	rumor := Event{CreatedAt: now.Unix(), Kind: KindSignal, Tags: [][]string{}, Content: content}
	rumor.setID(sender)
	rumorJSON, err := json.Marshal(rumor)
	if err != nil {
		return nil, err
	}

	sealed, err := nip44Encrypt(rumorJSON, conversationKey(sender, recipientPub))
	if err != nil {
		return nil, err
	}
	sealAt, err := backdate(now)
	if err != nil {
		return nil, err
	}
	seal := Event{CreatedAt: sealAt, Kind: KindSeal, Tags: [][]string{}, Content: sealed}
	if err := seal.sign(sender); err != nil {
		return nil, err
	}
	sealJSON, err := json.Marshal(seal)
	if err != nil {
		return nil, err
	}

	oneTime, err := GenerateKey()
	if err != nil {
		return nil, err
	}
	wrapped, err := nip44Encrypt(sealJSON, conversationKey(oneTime, recipientPub))
	if err != nil {
		return nil, err
	}
	wrapAt, err := backdate(now)
	if err != nil {
		return nil, err
	}
	gift := Event{
		CreatedAt: wrapAt,
		Kind:      KindGiftWrap,
		Tags: [][]string{
			{"p", recipient},
			{"expiration", strconv.FormatInt(now.Add(ttl).Unix(), 10)},
		},
		Content: wrapped,
	}
	if err := gift.sign(oneTime); err != nil {
		return nil, err
	}
	return &gift, nil
}

// unwrap opens a gift wrap addressed to key and returns who sent it and what
// it said. It checks every layer: the wrap's signature and recipient, the
// seal's signature, that the rumor's author is the seal's signer -- without
// that, anyone could seal a rumor claiming another sender -- and that the
// rumor was written within maxAge of now.
func unwrap(gift *Event, key *SecretKey, now time.Time, maxAge time.Duration) (sender, content string, err error) {
	if gift.Kind != KindGiftWrap {
		return "", "", errWrongKind
	}
	if gift.tag("p") != key.PublicKey() {
		return "", "", errNotForUs
	}
	if err := gift.verify(); err != nil {
		return "", "", err
	}
	wrapPub, err := parsePublicKey(gift.PubKey)
	if err != nil {
		return "", "", err
	}
	sealJSON, err := nip44Decrypt(gift.Content, conversationKey(key, wrapPub))
	if err != nil {
		return "", "", err
	}

	var seal Event
	if err := json.Unmarshal(sealJSON, &seal); err != nil {
		return "", "", errBadSeal
	}
	if seal.Kind != KindSeal || len(seal.Tags) != 0 {
		return "", "", errBadSeal
	}
	if err := seal.verify(); err != nil {
		return "", "", err
	}
	sealPub, err := parsePublicKey(seal.PubKey)
	if err != nil {
		return "", "", err
	}
	rumorJSON, err := nip44Decrypt(seal.Content, conversationKey(key, sealPub))
	if err != nil {
		return "", "", err
	}

	var rumor Event
	if err := json.Unmarshal(rumorJSON, &rumor); err != nil {
		return "", "", errBadSeal
	}
	if rumor.PubKey != seal.PubKey {
		return "", "", errSenderSpoof
	}
	if rumor.Kind != KindSignal {
		return "", "", errWrongKind
	}
	if err := rumor.checkID(); err != nil {
		return "", "", err
	}
	written := time.Unix(rumor.CreatedAt, 0)
	if written.Before(now.Add(-maxAge)) || written.After(now.Add(maxAge)) {
		return "", "", errStaleMessage
	}
	return seal.PubKey, rumor.Content, nil
}
