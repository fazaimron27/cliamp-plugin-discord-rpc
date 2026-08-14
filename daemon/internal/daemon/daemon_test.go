package daemon

import (
	"testing"

	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/playback"
)

func TestTimelineTrackerPreservesProgressAndDetectsSeek(t *testing.T) {
	tracker := timelineTracker{}
	first := tracker.Accept(playback.State{Status: "playing", Title: "Track", Path: "track", Duration: 200, Position: 10, ObservedAt: 1000})
	if first.StartedAt != 990 {
		t.Fatalf("first StartedAt = %d, want 990", first.StartedAt)
	}
	natural := tracker.Accept(playback.State{Status: "playing", Title: "Track", Path: "track", Duration: 200, Position: 20, ObservedAt: 1010})
	if natural.StartedAt != 990 {
		t.Fatalf("natural StartedAt = %d, want 990", natural.StartedAt)
	}
	seek := tracker.Accept(playback.State{Status: "playing", Title: "Track", Path: "track", Duration: 200, Position: 80, ObservedAt: 1011})
	if seek.StartedAt != 931 {
		t.Fatalf("seek StartedAt = %d, want 931", seek.StartedAt)
	}
	paused := tracker.Accept(playback.State{Status: "paused", Title: "Track", Path: "track", Duration: 200, Position: 80, ObservedAt: 1020})
	if paused.StartedAt != 0 {
		t.Fatalf("paused StartedAt = %d, want 0", paused.StartedAt)
	}
	resumed := tracker.Accept(playback.State{Status: "playing", Title: "Track", Path: "track", Duration: 200, Position: 80, ObservedAt: 1030})
	if resumed.StartedAt != 950 {
		t.Fatalf("resumed StartedAt = %d, want 950", resumed.StartedAt)
	}
}

func TestTimelineTrackerUsesObservationFallback(t *testing.T) {
	tracker := timelineTracker{nowUnix: func() int64 { return 1000 }}
	state := tracker.Accept(playback.State{Status: "playing", Title: "Track", Duration: 100, Position: 25})
	if state.ObservedAt != 1000 || state.StartedAt != 975 {
		t.Fatalf("state = %#v", state)
	}
}
