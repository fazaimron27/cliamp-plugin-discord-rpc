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
	"time"

	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/artwork"
	cliampipc "github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/cliamp"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/config"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/diag"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/discord"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/playback"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/presence"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/statewatch"
)

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
// A connection lost while the player is quiet is looked for from the quiet path
// too, because nothing else will ask: a paused player publishes no further
// events, and the clear that follows one stops the refresh timer that would
// otherwise come round again. So a clear retries the dial itself, which is what
// lets the loop reach Discord again without waiting for playback to resume.
// That retry is armed only once the loop has reached Discord at least once:
// dialing one it has no use for is the cry this loop exists to avoid, and the
// record of having reached it is what tells a lost connection from one never
// opened.
//
// A Discord that cannot be reached is reported once per outage rather than once
// per attempt. Both paths dial on every retry, and Discord being closed is an
// ordinary state rather than a fault, so a line per attempt would bury the
// journal it exists to explain. The report clears on a successful connect,
// because a latch that never cleared would fall silent exactly when the daemon
// started failing again, which is the state the report exists to surface.
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
//
// Two loggers arrive rather than one: the loop's own lines go through logger,
// and the Cliamp subscription's discarded-frame lines through cliampLogger, so
// each reaches the journal already naming the component that produced it. Where
// either one lands is Run's decision, not the loop's.
func run(
	ctx context.Context,
	cfg config.Config,
	client discordClient,
	resolver artworkResolver,
	logger diag.Logger,
	cliampLogger diag.Logger,
	now func() time.Time,
	refresh time.Duration,
) error {
	defer client.Close()

	var states <-chan playback.State
	var lastState playback.State
	var haveState bool
	tracker := timelineTracker{nowUnix: func() int64 { return now().Unix() }}
	var publishedKey string
	var publishedAt time.Time
	var discordUnreachableReported bool
	var rejections rejectionWatch
	reconnectDelay := time.Second
	var watch versionWatch
	var discordTried bool

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

	dialDiscord := func() error {
		if err := client.Connect(ctx); err != nil {
			if !discordUnreachableReported {
				logger.Printf("connect to Discord: %v", err)
				discordUnreachableReported = true
			}
			return err
		}
		discordUnreachableReported = false
		return nil
	}

	subscribe := func(ctx context.Context) (<-chan playback.State, error) {
		if cfg.Transport == config.TransportFile {
			return statewatch.Subscribe(ctx, cfg.StatePath, cfg.StateMaxAge)
		}
		return cliampipc.Subscribe(ctx, cfg.CliampSocket, cliampLogger)
	}
	subscription := "subscribed to Cliamp playback events"
	if cfg.Transport == config.TransportFile {
		subscription = fmt.Sprintf("watching for Cliamp state in %s", cfg.StatePath)
	}

	clear := func() {
		if client.Connected() && publishedKey != "clear" {
			if err := client.ClearActivity(); err != nil {
				logger.Printf("clear Discord presence: %v", err)
				var rejected *discord.RejectionError
				if !errors.As(err, &rejected) {
					_ = client.Close()
				}
			}
		}
		publishedKey = "clear"
		publishedAt = now()
		if !client.Connected() && discordTried {
			if err := dialDiscord(); err != nil {
				reset(refreshTimer, time.Second)
				return
			}
		}
		reset(refreshTimer, 0)
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
		discordTried = true
		if err := dialDiscord(); err != nil {
			reset(refreshTimer, time.Second)
			return
		}
		activity := presence.Build(lastState, presence.Options{LargeImage: cfg.LargeImage, LargeText: cfg.LargeText}, info.Image, links, currentTime)
		if err := client.SetActivity(activity); err != nil {
			var rejected *discord.RejectionError
			if errors.As(err, &rejected) {
				if rejections.observe(err) {
					logger.Printf("update Discord presence: %v", err)
				}
				reset(refreshTimer, refresh)
				return
			}
			logger.Printf("update Discord presence: %v", err)
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
			logger.Printf("%s", warning)
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
				logger.Printf("subscribe to Cliamp events: %v", err)
				reset(cliampTimer, reconnectDelay)
				reconnectDelay = min(2*reconnectDelay, 15*time.Second)
				continue
			}
			states = stream
			reconnectDelay = time.Second
			logger.Printf("%s", subscription)
		case state, ok := <-states:
			if !ok {
				states = nil
				haveState = false
				clear()
				reset(cliampTimer, reconnectDelay)
				reconnectDelay = min(2*reconnectDelay, 15*time.Second)
				logger.Printf("Cliamp event stream disconnected; reconnecting")
				continue
			}
			accept(state)
		case result := <-resolved:
			if result.track != artworkTrack {
				continue
			}
			if result.err != nil {
				logger.Printf("resolve Last.fm artwork: %v", result.err)
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
