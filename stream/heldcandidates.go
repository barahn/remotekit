// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package stream

// heldCandidates keeps the candidates viewers send ahead of the offer they
// belong to, until that offer is answered. Over a transport that does not
// keep order, such as Nostr relays, a candidate can overtake its offer; the
// viewer holds the agent's for the same reason (Viewer.addCandidate).
type heldCandidates map[string][]heldCandidate

type heldCandidate struct{ offerID, candidate string }

// hold keeps a candidate for viewerID's offer offerID. Past
// maxPendingCandidates for one viewer the oldest goes, so candidates for
// offers that are never answered cannot crowd out the current one's.
func (h heldCandidates) hold(viewerID, offerID, candidate string) {
	held := append(h[viewerID], heldCandidate{offerID, candidate})
	if len(held) > maxPendingCandidates {
		held = held[len(held)-maxPendingCandidates:]
	}
	h[viewerID] = held
}

// release returns viewerID's held candidates for offerID, or naming no
// offer, as an older viewer's do, and forgets them. Those for other offers
// stay: an offer that overtook this one is answered next.
func (h heldCandidates) release(viewerID, offerID string) []string {
	var out []string
	var kept []heldCandidate
	for _, c := range h[viewerID] {
		if c.offerID == "" || c.offerID == offerID {
			out = append(out, c.candidate)
		} else {
			kept = append(kept, c)
		}
	}
	if len(kept) == 0 {
		delete(h, viewerID)
	} else {
		h[viewerID] = kept
	}
	return out
}
