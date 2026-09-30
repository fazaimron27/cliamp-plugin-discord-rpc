package artwork_test

// This file tests the two rules that decide whether the player's artwork may be
// published: the answer must be about the track being reported, and the URL
// must be on a host known to serve public artwork. The first is a race the
// player cannot help — it answers from live state — and the second is the
// allowlist that keeps a self-hosted cover URL or a feed's own host off the
// card.

import (
	"context"
	"errors"
	"testing"

	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/artwork"
	cliampipc "github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/cliamp"
)

// snapshotOf builds the answer a running Cliamp would give for one track.
func snapshotOf(path, art string) cliampipc.Snapshot {
	var snapshot cliampipc.Snapshot
	snapshot.Track.Path = path
	snapshot.Track.AlbumArtURL = art
	return snapshot
}

// playerAnswering returns a Player whose socket is stubbed with a fixed answer.
func playerAnswering(snapshot cliampipc.Snapshot) *artwork.Player {
	return &artwork.Player{
		Socket: "/nonexistent/cliamp.sock",
		State: func(context.Context, string) (cliampipc.Snapshot, error) {
			return snapshot, nil
		},
	}
}

// TestPlayerAcceptsAnAllowlistedHost is the happy path: each host on the
// allowlist is published unchanged.
func TestPlayerAcceptsAnAllowlistedHost(t *testing.T) {
	urls := []string{
		"https://i.scdn.co/image/ab67616d0000b273",
		"https://thumbnailer.mixcloud.com/unsafe/600x600/extaudio/1/a/6/1/x.jpg",
		"https://I.SCDN.CO/image/ab67616d0000b273",
	}
	for _, url := range urls {
		player := playerAnswering(snapshotOf("spotify:track:abc", url))
		info, err := player.Resolve(context.Background(), artwork.Request{Path: "spotify:track:abc"})
		if err != nil {
			t.Fatalf("Resolve() error = %v for %q", err, url)
		}
		if info.Image != url {
			t.Errorf("Resolve() Image = %q; want %q", info.Image, url)
		}
	}
}

// TestPlayerRefusesEveryUnpublishableURL is the allowlist guard. Each case is a
// value a provider or a feed has actually produced, or one a lookalike host
// would need: a local tag's file URL, a feed's own host, a self-hosted cover
// URL carrying a token, and three spellings that must not fold into an
// allowlisted host.
func TestPlayerRefusesEveryUnpublishableURL(t *testing.T) {
	urls := []string{
		"",
		"   ",
		"file:///home/faza/.local/share/cliamp/art-cache/abc.jpg",
		"http://i.scdn.co/image/ab67616d0000b273",
		"https://podcast.example.com/cover.jpg",
		"https://navidrome.example.com/rest/getCoverArt?id=1&u=faza&t=deadbeef",
		"https://i.scdn.co.evil.example/image/x",
		"https://evilscdn.co/image/x",
		"https://m.i.scdn.co/image/x",
		"https://user:pass@i.scdn.co/image/x",
		"not a url at all",
	}
	for _, url := range urls {
		player := playerAnswering(snapshotOf("spotify:track:abc", url))
		info, err := player.Resolve(context.Background(), artwork.Request{Path: "spotify:track:abc"})
		if err != nil {
			t.Fatalf("Resolve() error = %v for %q", err, url)
		}
		if info.Image != "" {
			t.Errorf("Resolve() published %q for %q; want an empty image", info.Image, url)
		}
	}
}

// TestPlayerRefusesAnAnswerAboutAnotherTrack is the staleness guard. The player
// answers from live state, so an answer that arrives after the track changed
// describes a different track and its artwork must not be used.
func TestPlayerRefusesAnAnswerAboutAnotherTrack(t *testing.T) {
	player := playerAnswering(snapshotOf("spotify:track:next", "https://i.scdn.co/image/next"))
	info, err := player.Resolve(context.Background(), artwork.Request{Path: "spotify:track:abc"})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if info.Image != "" {
		t.Errorf("Resolve() Image = %q; want an empty image for a stale answer", info.Image)
	}
}

// TestPlayerRefusesANearMissPath pins the strictness of the match. The plugin's
// path and Cliamp's are produced independently, so a difference of a query, a
// fragment or a case is a miss rather than a guess.
func TestPlayerRefusesANearMissPath(t *testing.T) {
	paths := []string{
		"spotify:track:abc?si=1",
		"spotify:track:abc#fragment",
		"Spotify:track:abc",
		" spotify:track:abc",
		"spotify:track:abcd",
	}
	for _, path := range paths {
		player := playerAnswering(snapshotOf(path, "https://i.scdn.co/image/x"))
		info, err := player.Resolve(context.Background(), artwork.Request{Path: "spotify:track:abc"})
		if err != nil {
			t.Fatalf("Resolve() error = %v for %q", err, path)
		}
		if info.Image != "" {
			t.Errorf("Resolve() accepted %q as %q; want a miss", path, "spotify:track:abc")
		}
	}
}

// TestPlayerTreatsAFailedRequestAsAbsence covers degradation. A build that
// predates state.get, a socket nobody is listening on and a closed connection
// are all the absence of artwork, not an error worth reporting.
func TestPlayerTreatsAFailedRequestAsAbsence(t *testing.T) {
	player := &artwork.Player{
		Socket: "/nonexistent/cliamp.sock",
		State: func(context.Context, string) (cliampipc.Snapshot, error) {
			return cliampipc.Snapshot{}, errors.New("no listener on socket")
		},
	}
	info, err := player.Resolve(context.Background(), artwork.Request{Path: "spotify:track:abc"})
	if err != nil {
		t.Fatalf("Resolve() error = %v; want a miss", err)
	}
	if info.Image != "" {
		t.Errorf("Resolve() Image = %q; want an empty image", info.Image)
	}
}
