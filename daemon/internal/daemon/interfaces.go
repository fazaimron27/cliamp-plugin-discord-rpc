package daemon

// This file holds the seams the loop is written against: the Discord client it
// drives and the artwork resolver it asks, plus the tagged result an answer
// arrives as. Each is an interface rather than a concrete type so a test can
// hold the loop without a socket or a network.

import (
	"context"

	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/artwork"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/presence"
)

type discordClient interface {
	Connected() bool
	Connect(context.Context) error
	SetActivity(*presence.Activity) error
	ClearActivity() error
	Close() error
}

// artworkResolver supplies the artwork, the track page and the artist page for
// one track. The request is a struct rather than three positional strings
// because the path is not something Last.fm can be asked about: it is what the
// sources that cost nothing key on, and naming it keeps a caller from reading
// it as a stray argument at the call site.
//
// The answer arrives through a callback rather than a return, because a resolver
// may have an answer before it has finished: the artwork a path derives costs no
// request, so publishing it must not wait for the lookup that supplies the
// pages. Each call is a complete merged answer, so the loop can publish every
// one it is handed.
type artworkResolver interface {
	Resolve(context.Context, artwork.Request, func(artwork.TrackInfo, error))
}

// artworkResult is a lookup's outcome, tagged with the track it was asked about
// so the loop can discard an answer that arrived after the track changed.
type artworkResult struct {
	track string
	info  artwork.TrackInfo
	err   error
}
