package daemon

// This file is the daemon's run loop with its state given a name. The loop
// needs fourteen mutable values, seven pieces of behaviour that read and write
// them, and the invariant tying the two together — which is a type. It was
// fourteen locals and seven closures inside one function instead, and that shape
// is what hid the invariant: nothing named the set, so nothing could say what a
// value in it means, and no piece could be exercised without the whole loop
// running.

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

// session is one run loop: the dependencies it was built with, what it has
// learned so far, and the two timers it lives on.
//
// The fields fall into three groups, and the order below is that grouping. The
// first is fixed for the loop's whole life and is only ever read. The second is
// the loop's memory, written on one path and read on several. The third is the
// artwork lookup's channel and the answer it last recorded.
//
// The invariant a type exists to state is that nothing in the second group is
// trustworthy alone. publishedKey means nothing without publishedAt, which is
// what the refresh interval is measured from; haveState is what makes lastState
// a reading rather than a zero value; artworkTrack is what makes artworkInfo an
// answer rather than a leftover from the track before. Apart they are silently
// wrong, which is the failure a set of locals cannot warn about and a struct
// with a sentence on it can.
type session struct {
	cfg          config.Config
	client       discordClient
	resolver     artworkResolver
	logger       diag.Logger
	cliampLogger diag.Logger
	now          func() time.Time
	refresh      time.Duration

	states       <-chan playback.State
	tracker      timelineTracker
	lastState    playback.State
	haveState    bool
	publishedKey string
	publishedAt  time.Time

	rejections     rejectionWatch
	unreachable    bool
	reconnectDelay time.Duration
	watched        versionWatch
	triedDiscord   bool

	resolved     chan artworkResult
	artworkTrack string
	artworkInfo  artwork.TrackInfo

	refreshTimer *time.Timer
	cliampTimer  *time.Timer
}

// loop runs until ctx is cancelled, driving Discord from Cliamp's playback
// events. Its shape is a select over timers and channels rather than a sequence
// of blocking calls, because everything it talks to is slow: a subscribe that
// fails, a stream that ends, and a Discord call that errors all reschedule a
// timer and come round again, so a Cliamp restart or a dropped socket is an
// event on the subscription rather than the end of the process.
//
// The artwork case discards an answer tagged with a track the loop has since
// moved off. The refresh case is the one event no answer can cause, which is
// what makes it the only place a lookup that came back with nothing may be
// tried again; it names artworkTrack as the identity to match the reply
// against, not as the artist, because it is the track currently on screen.
//
// The timers are built here rather than alongside the other fields so their
// lifetime is the loop's: both are armed and disarmed throughout, and both are
// stopped on the way out.
func (s *session) loop(ctx context.Context) error {
	s.refreshTimer = time.NewTimer(time.Hour)
	if !s.refreshTimer.Stop() {
		<-s.refreshTimer.C
	}
	s.cliampTimer = time.NewTimer(0)
	defer s.client.Close()
	defer s.refreshTimer.Stop()
	defer s.cliampTimer.Stop()

	subscription := "subscribed to Cliamp playback events"
	if s.cfg.Transport == config.TransportFile {
		subscription = fmt.Sprintf("watching for Cliamp state in %s", s.cfg.StatePath)
	}

	for {
		select {
		case <-ctx.Done():
			s.clear(ctx)
			return nil
		case <-s.cliampTimer.C:
			stream, err := s.subscribe(ctx)
			if err != nil {
				if ctx.Err() != nil {
					s.clear(ctx)
					return nil
				}
				s.logger.Printf("subscribe to Cliamp events: %v", err)
				reset(s.cliampTimer, s.reconnectDelay)
				s.reconnectDelay = min(2*s.reconnectDelay, 15*time.Second)
				continue
			}
			s.states = stream
			s.reconnectDelay = time.Second
			s.logger.Printf("%s", subscription)
		case state, ok := <-s.states:
			if !ok {
				s.states = nil
				s.haveState = false
				s.clear(ctx)
				reset(s.cliampTimer, s.reconnectDelay)
				s.reconnectDelay = min(2*s.reconnectDelay, 15*time.Second)
				s.logger.Printf("Cliamp event stream disconnected; reconnecting")
				continue
			}
			s.accept(ctx, state)
		case result := <-s.resolved:
			if result.track != s.artworkTrack {
				continue
			}
			if result.err != nil {
				s.logger.Printf("resolve Last.fm artwork: %v", result.err)
			}
			s.artworkInfo = result.info
			s.reconcile(ctx)
		case <-s.refreshTimer.C:
			if s.artworkInfo.Image == "" && s.lastState.IsPlaying() {
				s.requestArtwork(ctx, s.artworkTrack, artwork.Request{
					Path:   s.lastState.Path,
					Artist: s.lastState.Artist,
					Title:  s.lastState.Title,
				})
			}
			s.reconcile(ctx)
		}
	}
}

// requestArtwork asks the resolver about one track on a goroutine and delivers
// the answer back to the loop tagged with the track it was asked about. The tag
// is what lets the loop discard an answer that arrived after the track changed,
// and the sending side abandons its send when ctx is done, since a send with no
// reader left would outlive the loop it was answering.
func (s *session) requestArtwork(ctx context.Context, track string, request artwork.Request) {
	go func() {
		s.resolver.Resolve(ctx, request, func(info artwork.TrackInfo, err error) {
			select {
			case s.resolved <- artworkResult{track: track, info: info, err: err}:
			case <-ctx.Done():
			}
		})
	}()
}

// dialDiscord opens the socket, reporting an unreachable Discord once per
// outage rather than once per attempt. Both dial paths retry on every failure
// and Discord being closed is an ordinary state rather than a fault, so a line
// per attempt would bury the journal it exists to explain. The latch clears on
// a successful connect: one that never cleared would fall silent exactly when
// the daemon started failing again, which is the state the report exists to
// surface.
func (s *session) dialDiscord(ctx context.Context) error {
	if err := s.client.Connect(ctx); err != nil {
		if !s.unreachable {
			s.logger.Printf("connect to Discord: %v", err)
			s.unreachable = true
		}
		return err
	}
	s.unreachable = false
	return nil
}

// subscribe starts reading Cliamp's playback events over the configured
// transport. The two differ in the call that starts reading and in what the log
// calls them; everything downstream sees the same channel of snapshots. A
// Cliamp that cannot be reached at all is a failed subscribe on either one, so
// the loop's retry covers both — the file transport fails while the directory
// its document lives in does not exist yet.
func (s *session) subscribe(ctx context.Context) (<-chan playback.State, error) {
	if s.cfg.Transport == config.TransportFile {
		return statewatch.Subscribe(ctx, s.cfg.StatePath, s.cfg.StateMaxAge)
	}
	return cliampipc.Subscribe(ctx, s.cfg.CliampSocket, s.cliampLogger)
}

// clear takes the card off Discord and leaves the loop quiet.
//
// A clear is a SET_ACTIVITY like any other and only its activity differs, so a
// refusal here is answered the way one from reconcile is: the payload was read
// and turned down, the connection is still good, and the socket is kept. Every
// other error leaves the socket in doubt and closes it. This is the more common
// of the two refusals, because a clear is what a pause reports — reconnecting
// on one would cost a teardown on every pause rather than once per session.
//
// A connection lost while the player is quiet is looked for from here too,
// because nothing else will ask: a paused player publishes no further events,
// and the clear that follows one stops the refresh timer that would otherwise
// come round again. So the clear retries the dial itself, which is what lets the
// loop reach Discord again without waiting for playback to resume. That retry is
// armed only once the loop has reached Discord at least once — dialing one it
// has no use for is the cry this loop exists to avoid, and the record of having
// reached it is what tells a lost connection from one never opened.
func (s *session) clear(ctx context.Context) {
	if s.client.Connected() && s.publishedKey != "clear" {
		if err := s.client.ClearActivity(); err != nil {
			s.logger.Printf("clear Discord presence: %v", err)
			var rejected *discord.RejectionError
			if !errors.As(err, &rejected) {
				_ = s.client.Close()
			}
		}
	}
	s.publishedKey = "clear"
	s.publishedAt = s.now()
	if !s.client.Connected() && s.triedDiscord {
		if err := s.dialDiscord(ctx); err != nil {
			reset(s.refreshTimer, time.Second)
			return
		}
	}
	reset(s.refreshTimer, 0)
}

// reconcile publishes what the loop currently knows, or clears the card when
// nothing is playing. Every path that can change the answer calls it: a new
// snapshot, an artwork reply, and the refresh.
//
// The loop remembers what it last knew about the artwork of the track it is
// showing. The resolver caches too, but asking it is only cheap when it already
// has the answer, so the loop asks on a track change and on the refresh rather
// than on every reconcile. A different track drops the previous answer rather
// than showing it, publishes with what is known now, and lets the reply
// republish: waiting there is what made a pause or a skip queue behind the
// network.
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
func (s *session) reconcile(ctx context.Context) {
	if !s.haveState || !s.lastState.IsPlaying() {
		s.clear(ctx)
		return
	}
	track := s.lastState.TrackKey()
	if track != s.artworkTrack {
		s.artworkTrack = track
		s.artworkInfo = artwork.TrackInfo{}
		s.requestArtwork(ctx, track, artwork.Request{
			Path:   s.lastState.Path,
			Artist: s.lastState.Artist,
			Title:  s.lastState.Title,
		})
	}
	info := s.artworkInfo
	links := linksFor(s.lastState, info)
	currentTime := s.now()
	desiredKey := republishKey(s.lastState, info, links)
	if desiredKey == s.publishedKey && s.client.Connected() && currentTime.Sub(s.publishedAt) < s.refresh {
		reset(s.refreshTimer, s.refresh-currentTime.Sub(s.publishedAt))
		return
	}
	s.triedDiscord = true
	if err := s.dialDiscord(ctx); err != nil {
		reset(s.refreshTimer, time.Second)
		return
	}
	activity := presence.Build(s.lastState, presence.Options{LargeImage: s.cfg.LargeImage, LargeText: s.cfg.LargeText}, info.Image, links, currentTime)
	if err := s.client.SetActivity(activity); err != nil {
		var rejected *discord.RejectionError
		if errors.As(err, &rejected) {
			if s.rejections.observe(err) {
				s.logger.Printf("update Discord presence: %v", err)
			}
			reset(s.refreshTimer, s.refresh)
			return
		}
		s.logger.Printf("update Discord presence: %v", err)
		_ = s.client.Close()
		reset(s.refreshTimer, time.Second)
		return
	}
	s.rejections.accepted()
	s.publishedKey = desiredKey
	s.publishedAt = currentTime
	reset(s.refreshTimer, s.refresh)
}

// accept takes one Cliamp snapshot as the loop's current state and reconciles
// on it. The plugin's reported version is checked here too: an incompatible
// release line is warned about once and then left alone, because the transports
// fail later and more loudly if the warning was right, and refusing to run would
// turn a version skew into silence.
func (s *session) accept(ctx context.Context, state playback.State) {
	if warning := s.watched.observe(state.PluginVersion); warning != "" {
		s.logger.Printf("%s", warning)
	}
	s.lastState = s.tracker.Accept(state)
	s.haveState = true
	s.reconcile(ctx)
}

// reset arms a timer for duration, or only stops it when duration is not
// positive. Stopping is a non-blocking drain rather than a bare Stop, because a
// timer that has already fired leaves a value in its channel, and the next
// Reset would otherwise fire on that stale value the moment it was armed.
func reset(timer *time.Timer, duration time.Duration) {
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
