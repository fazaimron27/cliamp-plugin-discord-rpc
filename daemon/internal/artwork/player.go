package artwork

// This file is the player half of artwork resolution: it asks Cliamp what
// artwork it holds for the track being played. Two rules decide whether the
// answer is published, and both are here because both are about not trusting
// it. The player answers from live state rather than replying about what it was
// asked, so an answer can describe a track the player has already left. And the
// URL comes from a provider rather than from this daemon, so it is admitted by
// host allowlist the way every other provider-supplied URL in this repository
// is.

import (
	"context"
	"strings"

	cliampipc "github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/cliamp"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/tracklink"
)

// artHosts is the allowlist of hosts whose artwork this daemon will publish.
//
// It is an allowlist rather than a blocklist because a blocklist fails open the
// moment a provider is added, and the sources absent from it now — a feed's own
// image host, a self-hosted server's cover URL — are exactly the ones that
// could carry a credential. The cost is deliberate: podcast artwork, whose host
// is whatever the publisher's feed points at, is refused and falls back to
// Last.fm. Anything added here is a host whose URLs are public by construction.
var artHosts = map[string]bool{
	"i.scdn.co":                true,
	"thumbnailer.mixcloud.com": true,
}

// Player resolves the artwork Cliamp holds for a track, over the owner-only
// socket the subscription already uses.
type Player struct {
	// Socket is the Cliamp IPC socket path.
	Socket string
	// State reads one runtime snapshot. A nil value uses cliamp.State, which
	// exists so the rules in this file can be tested without a socket.
	State func(context.Context, string) (cliampipc.Snapshot, error)
}

// Resolve returns the player's artwork for the requested track, and an empty
// answer whenever the player holds none, cannot be reached, or is describing a
// different track. No failure here is reported: an absent socket, a build that
// predates the request and a refused URL are all the absence of artwork, and
// the caller falls through to Last.fm in every one of those cases.
func (p *Player) Resolve(ctx context.Context, request Request) (TrackInfo, error) {
	read := p.State
	if read == nil {
		read = cliampipc.State
	}
	snapshot, err := read(ctx, p.Socket)
	if err != nil {
		return TrackInfo{}, nil
	}
	if snapshot.Track.Path != request.Path {
		return TrackInfo{}, nil
	}
	image := artworkURL(snapshot.Track.AlbumArtURL)
	if image == "" {
		return TrackInfo{}, nil
	}
	return TrackInfo{Image: image}, nil
}

// artworkURL returns raw only when it is a URL this daemon may publish and it
// names a host on the allowlist. Everything the player reports goes through
// it, so the image is held to one rule.
//
// The host test is exact equality after folding case, and nothing is trimmed.
// A suffix test would accept i.scdn.co.evil.example, and dropping a leading
// www. or m. the way tracklink's normaliseHost does — which is right for the
// hosts a person may spell by hand — would accept m.i.scdn.co as i.scdn.co.
func artworkURL(raw string) string {
	raw = strings.TrimSpace(raw)
	parsed, ok := tracklink.PublishableHTTPS(raw)
	if !ok {
		return ""
	}
	if !artHosts[strings.ToLower(parsed.Hostname())] {
		return ""
	}
	return raw
}
