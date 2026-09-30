package cliamp_test

// This file tests the state request against a fake Cliamp socket: the v2 frame
// it must send, the snapshot it must decode, and the answers that must be
// reported as failures rather than read as an empty track. An empty track is
// indistinguishable from "the player holds no artwork", so a malformed reply
// that decoded to one would silently disable the tier that depends on it.

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	cliampipc "github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/cliamp"
)

// serveState replies to one request with the given raw frame, and returns a
// channel carrying the request frame it was asked with. A handler callback like
// serveConn's would be more general, but every caller here wants the same two
// things — read the request, write a canned reply — so the helper does both and
// the channel is how a test inspects what was sent.
func serveState(t *testing.T, socket, reply string) <-chan map[string]any {
	t.Helper()
	asked := make(chan map[string]any, 1)
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		scanner := bufio.NewScanner(conn)
		if !scanner.Scan() {
			return
		}
		var frame map[string]any
		_ = json.Unmarshal(scanner.Bytes(), &frame)
		asked <- frame
		_, _ = conn.Write([]byte(reply + "\n"))
	}()
	return asked
}

// TestStateReadsTheTrackAndItsArtwork covers the exchange itself: the version 2
// frame carrying method state.get, and the snapshot underneath the snapshot key.
func TestStateReadsTheTrackAndItsArtwork(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "cliamp.sock")
	serveState(t, socket, `{"version":2,"id":"discord-rpc-state","ok":true,"snapshot":{"track":{"path":"spotify:track:abc","album_art_url":"https://i.scdn.co/image/x"}}}`)

	snapshot, err := cliampipc.State(context.Background(), socket)
	if err != nil {
		t.Fatalf("State() error = %v", err)
	}
	if snapshot.Track.Path != "spotify:track:abc" {
		t.Errorf("Track.Path = %q; want the player's path", snapshot.Track.Path)
	}
	if snapshot.Track.AlbumArtURL != "https://i.scdn.co/image/x" {
		t.Errorf("Track.AlbumArtURL = %q; want the artwork URL", snapshot.Track.AlbumArtURL)
	}
}

// TestStateSendsTheVersionTwoFrame pins the request shape. Cliamp rejects an
// unversioned or misaddressed frame rather than interpreting it, so a request
// that drifted here would fail as an unexplained absence of artwork.
func TestStateSendsTheVersionTwoFrame(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "cliamp.sock")
	asked := serveState(t, socket, `{"version":2,"id":"discord-rpc-state","ok":true,"snapshot":{}}`)

	if _, err := cliampipc.State(context.Background(), socket); err != nil {
		t.Fatalf("State() error = %v", err)
	}
	frame := <-asked
	if frame["version"] != float64(2) {
		t.Errorf("request version = %v; want 2", frame["version"])
	}
	if frame["method"] != "state.get" {
		t.Errorf("request method = %v; want state.get", frame["method"])
	}
}

// TestStateReportsEveryUnusableAnswer is the guard that keeps a broken reply
// from being read as an empty track. Each case below is an answer Cliamp can
// legitimately produce, and each must be an error.
func TestStateReportsEveryUnusableAnswer(t *testing.T) {
	cases := []struct {
		name  string
		reply string
	}{
		{
			name:  "an older Cliamp rejects the version",
			reply: `{"version":1,"id":"discord-rpc-state","ok":false,"error":{"code":"invalid_version","message":"unsupported"}}`,
		},
		{
			name:  "a structured error",
			reply: `{"version":2,"id":"discord-rpc-state","ok":false,"error":{"code":"unavailable","message":"no dispatcher"}}`,
		},
		{
			name:  "a failure with no error object",
			reply: `{"version":2,"id":"discord-rpc-state","ok":false}`,
		},
		{
			name:  "a success with no snapshot",
			reply: `{"version":2,"id":"discord-rpc-state","ok":true}`,
		},
		{
			name:  "a snapshot that is not an object",
			reply: `{"version":2,"id":"discord-rpc-state","ok":true,"snapshot":"nope"}`,
		},
		{
			name:  "an undecodable frame",
			reply: `{"version":2,`,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			socket := filepath.Join(t.TempDir(), "cliamp.sock")
			serveState(t, socket, testCase.reply)
			if snapshot, err := cliampipc.State(context.Background(), socket); err == nil {
				t.Fatalf("State() = %+v, nil; want an error", snapshot)
			}
		})
	}
}

// TestStateReportsAnOversizedReply covers the buffer cap. A reply past the
// scanner's limit must be reported, not truncated: a truncated frame that
// happened to decode would read as a track with no artwork, which is the
// failure mode that disables the player tier without saying so.
func TestStateReportsAnOversizedReply(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "cliamp.sock")
	padding := strings.Repeat("x", 256*1024)
	serveState(t, socket, `{"version":2,"id":"discord-rpc-state","ok":true,"snapshot":{"track":{"path":"`+padding+`"}}}`)

	if _, err := cliampipc.State(context.Background(), socket); err == nil {
		t.Fatal("State() succeeded on an oversized reply; want an error")
	}
}

// TestStateReportsAMissingSocket covers the daemon's normal case on a machine
// where Cliamp is not running.
func TestStateReportsAMissingSocket(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "absent.sock")
	if _, err := cliampipc.State(context.Background(), socket); err == nil {
		t.Fatal("State() succeeded with no listener; want an error")
	}
}

// TestStateReportsASilentListener covers a connection that closes without
// answering, which must not read as an empty track either.
func TestStateReportsASilentListener(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "cliamp.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		_ = conn.Close()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := cliampipc.State(ctx, socket); err == nil {
		t.Fatal("State() succeeded against a listener that never answered; want an error")
	}
}
