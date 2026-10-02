package daemon

// This file turns a playback snapshot into what the card is built from: the key
// that decides whether the card is re-sent, the public links a snapshot resolves
// to, and the anchor its progress timeline starts from.

import (
	"time"

	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/artwork"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/playback"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/presence"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/tracklink"
)

// republishKey is what decides whether the card is re-sent. It covers the
// snapshot's public identity, the resolved artwork, and the derived links.
//
// The links belong here because the provider link is derived from the playback
// path, and PresenceKey deliberately excludes the path. A path change also
// re-anchors the timeline, so this is defence against a future PresenceKey
// rather than a fix for an observable bug today — but the republish is
// conditional on this value and the links are a real input to the payload, so
// the two are kept in step by construction.
func republishKey(state playback.State, info artwork.TrackInfo, links presence.Links) string {
	return state.PresenceKey() + "\x00" + info.Image + "\x00" + links.Key()
}

// linksFor gathers every public URL a snapshot resolves to: the pages Last.fm
// reported, and the provider page the playback path identifies.
//
// The provider half is a pure string parse of a value the daemon already holds,
// so deriving it here costs nothing and adds no request. A path matching no
// allowlist yields no provider link at all, which is what keeps a local
// filename and a credential-bearing stream URL out of the payload.
func linksFor(state playback.State, info artwork.TrackInfo) presence.Links {
	links := presence.Links{TrackURL: info.TrackURL, ArtistURL: info.ArtistURL}
	link, ok := tracklink.Find(state.Path)
	if !ok {
		return links
	}
	links.Provider = link.Provider
	links.ProviderURL = link.URL
	if search, ok := link.ArtistSearch(state.Artist); ok {
		links.ArtistSearchURL = search
	}
	return links
}

type timelineTracker struct {
	last    playback.State
	have    bool
	nowUnix func() int64
}

// Accept stamps a snapshot with the time it was observed and decides where its
// progress timeline starts. A playing snapshot whose position has advanced
// naturally from the previous one keeps the existing anchor, so the bar holds
// still instead of being re-anchored on every event; a track change, a seek, or
// a resume anchors it afresh.
func (t *timelineTracker) Accept(state playback.State) playback.State {
	observed := state.ObservedAt
	if observed <= 0 {
		if t.nowUnix != nil {
			observed = t.nowUnix()
		} else {
			observed = time.Now().Unix()
		}
	}
	state.ObservedAt = observed
	if state.IsPlaying() {
		keepTimeline := t.have && t.last.IsPlaying() && state.TrackKey() == t.last.TrackKey()
		if keepTimeline {
			expected := t.last.Position + max(observed-t.last.ObservedAt, 0)
			delta := state.Position - expected
			if delta < 0 {
				delta = -delta
			}
			keepTimeline = delta <= 2
		}
		if keepTimeline {
			state.StartedAt = t.last.StartedAt
		} else {
			state.StartedAt = observed - min(max(state.Position, 0), state.Duration)
		}
	}
	t.last = state
	t.have = true
	return state
}
