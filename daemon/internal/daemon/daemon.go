// Package daemon coordinates Cliamp event subscriptions, artwork, and Discord IPC.
package daemon

// This file is the daemon's run loop: it subscribes to one of the two playback
// transports, turns snapshots into Discord activity, and keeps that activity
// alive until the context is cancelled.
//
// The loop is single-threaded and its shape is a select over timers and
// channels rather than a sequence of blocking calls, because everything it talks
// to is slow. A subscribe that fails, a stream that ends, and a Discord call that
// errors all reschedule a timer and come round again, so a Cliamp restart or a
// dropped socket is an event on the subscription rather than the end of the
// process.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/artwork"
	cliampipc "github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/cliamp"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/config"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/discord"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/playback"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/presence"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/statewatch"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/tracklink"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/version"
)

// presenceRefresh is how often Discord is told the same thing again. Discord
// drops an activity that is not re-sent, so a playing track is republished on
// this interval even when nothing about it changed.
const presenceRefresh = 15 * time.Second

const (
	repository = "fazaimron27/cliamp-plugin-discord-rpc"
	rawBase    = "https://raw.githubusercontent.com/" + repository + "/"
)

// normalize trims a plugin-reported version and drops any leading "v". The value
// arrives from the plugin, so it may carry the prefix, surrounding space, or
// neither, and it is rendered into log lines as well as URLs.
func normalize(value string) string {
	return strings.TrimPrefix(strings.TrimSpace(value), "v")
}

// tag renders a reported version as a release tag, carrying exactly one leading
// "v".
func tag(value string) string {
	return "v" + normalize(value)
}

// versionWatch reports a plugin/daemon release-line mismatch once per distinct
// plugin version. Repeating it on every snapshot would bury the genuine error
// traffic, and a mismatch is a one-time discovery rather than a per-track event.
type versionWatch struct {
	reported string
}

// observe returns the warning to log for a plugin version, or an empty string
// when the pairing is compatible, the plugin is too old to report a version, or
// this version has already been reported.
//
// The remembered value is the normalized one, because that is what the warning
// is written from: two spellings that render the same line are the same report,
// and deduping on the raw string would print it twice.
//
// The sentence it returns is the same one the --check report prints; only the
// remedy differs, because only this consumer knows that it is running. Naming
// the half that is behind is the point of that remedy: this daemon is usually a
// source build running ahead of the installed plugin, so a warning that always
// pointed at the plugin would have that user downgrade the half that is current.
func (w *versionWatch) observe(pluginVersion string) string {
	reported := normalize(pluginVersion)
	if reported == "" || reported == w.reported {
		return ""
	}
	w.reported = reported
	relation := version.Relate(reported, version.Number)
	explained := version.Explain(relation, reported, version.Number)
	switch relation {
	case version.PluginBehind:
		return fmt.Sprintf(
			"discord-rpc %s; these release lines use incompatible transports. Install matching halves with: cliamp plugins install %s@v%s",
			explained, repository, version.Number,
		)
	case version.DaemonBehind:
		return fmt.Sprintf(
			"discord-rpc %s. Update cliamp-rpcd with: curl -fsSL %s%s/install.sh | sh (or rebuild from source), then restart it.",
			explained, rawBase, tag(reported),
		)
	default:
		return ""
	}
}

type discordClient interface {
	Connected() bool
	Connect(context.Context) error
	SetActivity(*presence.Activity) error
	ClearActivity() error
	Close() error
}

// rejectionWatch reports a refused activity once per distinct rejection. The
// loop re-tries a refused payload on every refresh, so a line per attempt would
// bury the journal it exists to explain, and would do it at the refresh rate for
// as long as the payload stays refused.
//
// It is keyed on Discord's own detail rather than on the activity, because the
// detail is what names the fault: a second and different refusal is news, while
// the same one repeated is not. A successful publish clears it, because a latch
// that never cleared would fall silent exactly when the daemon began refusing
// activities again, which is the state the report exists to surface.
type rejectionWatch struct {
	reported string
}

// observe records a rejection and reports whether it is one still worth
// logging, which is the first of its kind since the last accepted activity.
func (w *rejectionWatch) observe(err error) bool {
	detail := err.Error()
	if detail == w.reported {
		return false
	}
	w.reported = detail
	return true
}

// accepted records that Discord took an activity, so a rejection after it is
// reported afresh.
func (w *rejectionWatch) accepted() {
	w.reported = ""
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

// Run constructs production dependencies and blocks until cancellation.
//
// The line it logs at startup names the source it is about to read, which is the
// first thing to check when nothing shows up on Discord.
func Run(ctx context.Context, cfg config.Config) error {
	switch cfg.Transport {
	case config.TransportFile:
		log.Printf("starting cliamp-rpcd %s (state file: %s)", version.Number, cfg.StatePath)
	default:
		log.Printf("starting cliamp-rpcd %s (Cliamp IPC: %s)", version.Number, cfg.CliampSocket)
	}
	if warning := cfg.TransportWarning(); warning != "" {
		log.Print(warning)
	}
	if cfg.LastFMAPIKey == "" {
		log.Printf("Last.fm artwork disabled: plugins.discord-rpc.lastfm_api_key is empty")
	}
	return run(ctx, cfg, discord.NewClient(cfg.ApplicationID), newResolver(cfg), time.Now, presenceRefresh)
}

// newResolver builds the artwork resolver the daemon runs with. It is a
// function rather than a literal inside Run so a test can hold the assembled
// resolver — the wiring is what decides which tiers exist, and a literal inside
// a constructor that dials Discord is a wiring nothing can check.
func newResolver(cfg config.Config) artwork.Resolver {
	return artwork.Resolver{
		Derived: tracklink.Artwork,
		Player:  &artwork.Player{Socket: cfg.CliampSocket},
		LastFM:  artwork.NewLastFM(cfg.LastFMAPIKey),
	}
}

// run is the daemon's event loop. refresh is a parameter rather than the
// presenceRefresh constant so a test can drive the loop's timed behavior without
// waiting out the real interval; production passes the constant.
//
// The two transports differ in the call that starts reading and in what the log
// calls it; everything downstream sees the same channel of snapshots. A Cliamp
// that cannot be reached at all is a failed subscribe on either one, so the retry
// below covers both — the file transport fails while the directory its document
// lives in does not exist yet.
//
// An activity Discord refuses is not a Discord that cannot be reached, and is
// answered differently. A refusal means the payload was read and turned down, so
// the connection is still good and only different content can succeed; the
// socket is therefore left open and the activity re-tried on the refresh.
// Reconnecting instead would tear down a completed handshake and send the same
// refused bytes down a fresh socket to be refused again, which is an endless
// loop of teardowns for a fault no reconnection can clear. Every other error out
// of SetActivity leaves the socket in doubt, and keeps the reconnect it has
// always had.
//
// The same rule governs clearing, because a clear is a SET_ACTIVITY like any
// other and only its activity differs. It is the more common of the two: a clear
// is what a pause reports, so a refusal there would cost a teardown on every
// pause rather than once per session.
//
// Artwork is looked up by a goroutine and delivered back here as a result tagged
// with the track it was asked about. The tag is what lets the loop discard an
// answer that arrived after the track changed, and the sending side abandons its
// send when the loop is gone, since a send with no reader left would outlive it.
//
// The loop remembers what it last knew about the artwork of the track it is
// showing. The resolver caches too, but asking it is only cheap when it already
// has the answer, so the loop asks on a track change and on the refresh rather
// than on every reconcile. A different track drops the previous answer rather
// than showing it, publishes with what is known now, and lets the reply
// republish: waiting there is what made a pause or a skip queue behind the
// network.
//
// An empty answer is remembered for its track rather than retried, because an
// answer must not be able to ask for itself — the loop would re-ask on every
// reply. That leaves the refresh as the one event no answer can cause, and so
// the only place a lookup that came back with nothing may be tried again, which
// is what lets a track recover from a request that failed or that ran before
// Last.fm had the artwork. The refresh passes artworkTrack as the identity to
// match the reply against, not as the artist: it names the track the loop is
// currently showing.
func run(ctx context.Context, cfg config.Config, client discordClient, resolver artworkResolver, now func() time.Time, refresh time.Duration) error {
	defer client.Close()

	var states <-chan playback.State
	var lastState playback.State
	var haveState bool
	tracker := timelineTracker{nowUnix: func() int64 { return now().Unix() }}
	var publishedKey string
	var publishedAt time.Time
	var rejections rejectionWatch
	reconnectDelay := time.Second
	var watch versionWatch

	resolved := make(chan artworkResult, 1)
	var artworkTrack string
	var artworkInfo artwork.TrackInfo

	requestArtwork := func(track string, request artwork.Request) {
		go func() {
			resolver.Resolve(ctx, request, func(info artwork.TrackInfo, err error) {
				select {
				case resolved <- artworkResult{track: track, info: info, err: err}:
				case <-ctx.Done():
				}
			})
		}()
	}

	refreshTimer := time.NewTimer(time.Hour)
	if !refreshTimer.Stop() {
		<-refreshTimer.C
	}
	cliampTimer := time.NewTimer(0)
	defer refreshTimer.Stop()
	defer cliampTimer.Stop()

	reset := func(timer *time.Timer, duration time.Duration) {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		if duration > 0 {
			timer.Reset(duration)
		}
	}

	subscribe := func(ctx context.Context) (<-chan playback.State, error) {
		if cfg.Transport == config.TransportFile {
			return statewatch.Subscribe(ctx, cfg.StatePath, cfg.StateMaxAge)
		}
		return cliampipc.Subscribe(ctx, cfg.CliampSocket)
	}
	subscription := "subscribed to Cliamp playback events"
	if cfg.Transport == config.TransportFile {
		subscription = fmt.Sprintf("watching for Cliamp state in %s", cfg.StatePath)
	}

	clear := func() {
		reset(refreshTimer, 0)
		if client.Connected() && publishedKey != "clear" {
			if err := client.ClearActivity(); err != nil {
				log.Printf("clear Discord presence: %v", err)
				var rejected *discord.RejectionError
				if !errors.As(err, &rejected) {
					_ = client.Close()
				}
			}
		}
		publishedKey = "clear"
		publishedAt = now()
	}

	reconcile := func() {
		if !haveState || !lastState.IsPlaying() {
			clear()
			return
		}
		track := lastState.TrackKey()
		if track != artworkTrack {
			artworkTrack = track
			artworkInfo = artwork.TrackInfo{}
			requestArtwork(track, artwork.Request{
				Path:   lastState.Path,
				Artist: lastState.Artist,
				Title:  lastState.Title,
			})
		}
		info := artworkInfo
		links := linksFor(lastState, info)
		currentTime := now()
		desiredKey := republishKey(lastState, info, links)
		if desiredKey == publishedKey && client.Connected() && currentTime.Sub(publishedAt) < refresh {
			reset(refreshTimer, refresh-currentTime.Sub(publishedAt))
			return
		}
		if err := client.Connect(ctx); err != nil {
			reset(refreshTimer, time.Second)
			return
		}
		activity := presence.Build(lastState, presence.Options{LargeImage: cfg.LargeImage, LargeText: cfg.LargeText}, info.Image, links, currentTime)
		if err := client.SetActivity(activity); err != nil {
			var rejected *discord.RejectionError
			if errors.As(err, &rejected) {
				if rejections.observe(err) {
					log.Printf("update Discord presence: %v", err)
				}
				reset(refreshTimer, refresh)
				return
			}
			log.Printf("update Discord presence: %v", err)
			_ = client.Close()
			reset(refreshTimer, time.Second)
			return
		}
		rejections.accepted()
		publishedKey = desiredKey
		publishedAt = currentTime
		reset(refreshTimer, refresh)
	}

	accept := func(state playback.State) {
		if warning := watch.observe(state.PluginVersion); warning != "" {
			log.Print(warning)
		}
		lastState = tracker.Accept(state)
		haveState = true
		reconcile()
	}

	for {
		select {
		case <-ctx.Done():
			clear()
			return nil
		case <-cliampTimer.C:
			stream, err := subscribe(ctx)
			if err != nil {
				if ctx.Err() != nil {
					clear()
					return nil
				}
				log.Printf("subscribe to Cliamp events: %v", err)
				reset(cliampTimer, reconnectDelay)
				reconnectDelay = min(2*reconnectDelay, 15*time.Second)
				continue
			}
			states = stream
			reconnectDelay = time.Second
			log.Print(subscription)
		case state, ok := <-states:
			if !ok {
				states = nil
				haveState = false
				clear()
				reset(cliampTimer, reconnectDelay)
				reconnectDelay = min(2*reconnectDelay, 15*time.Second)
				log.Printf("Cliamp event stream disconnected; reconnecting")
				continue
			}
			accept(state)
		case result := <-resolved:
			if result.track != artworkTrack {
				continue
			}
			if result.err != nil {
				log.Printf("resolve Last.fm artwork: %v", result.err)
			}
			artworkInfo = result.info
			reconcile()
		case <-refreshTimer.C:
			if artworkInfo.Image == "" && lastState.IsPlaying() {
				requestArtwork(artworkTrack, artwork.Request{
					Path:   lastState.Path,
					Artist: lastState.Artist,
					Title:  lastState.Title,
				})
			}
			reconcile()
		}
	}
}
