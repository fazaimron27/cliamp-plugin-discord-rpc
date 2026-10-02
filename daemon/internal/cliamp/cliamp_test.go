package cliamp_test

// This file tests Subscribe against a fake Cliamp socket: the version 2
// handshake it must complete, the structured error it must surface, and the
// snapshots it streams once subscribed. The fake stands in for Cliamp's IPC
// server, so each case drives one exchange without a running Cliamp.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	cliampipc "github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/cliamp"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/diag"
)

// cliampRequest mirrors the v2 envelope Cliamp requires before it will serve a
// subscription. Cliamp rejects unversioned frames outright, so the fake server
// here enforces the same contract as ipc/server.go.
type cliampRequest struct {
	Version int      `json:"version"`
	ID      string   `json:"id"`
	Method  string   `json:"method"`
	Topics  []string `json:"topics"`
}

func serveConn(t *testing.T, socket string, handle func(net.Conn, cliampRequest)) {
	t.Helper()
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
		var request cliampRequest
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			return
		}
		handle(conn, request)
	}()
}

// A subscriber that completes the handshake receives the snapshot Cliamp
// publishes on the playback topic, with the event timestamp recorded as its
// observed time.
func TestCliampSubscriptionReceivesPlayback(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "cliamp.sock")
	serveConn(t, socket, func(conn net.Conn, request cliampRequest) {
		if request.Version != 2 || request.Method != "subscribe" ||
			len(request.Topics) != 1 || request.Topics[0] != cliampipc.PlaybackTopic {
			_, _ = conn.Write([]byte("{\"version\":2,\"ok\":false,\"error\":{\"code\":\"invalid_version\",\"message\":\"unsupported protocol version\"}}\n"))
			return
		}
		_, _ = conn.Write([]byte("{\"version\":2,\"id\":\"discord-rpc-subscribe\",\"ok\":true}\n"))
		_, _ = conn.Write([]byte("{\"event\":\"plugin.discord-rpc.playback\",\"time\":1000,\"data\":{\"status\":\"playing\",\"title\":\"Track\",\"position\":12,\"duration\":200}}\n"))
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	states, err := cliampipc.Subscribe(ctx, socket, diag.Discard())
	if err != nil {
		t.Fatal(err)
	}
	select {
	case state := <-states:
		if state.Status != "playing" || state.Title != "Track" || state.Position != 12 || state.ObservedAt != 1000 {
			t.Fatalf("state = %#v", state)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for playback state")
	}
}

// A rejected subscription must surface Cliamp's structured error rather than a
// JSON decode failure caused by reading the v2 error object as a bare string.
func TestCliampSubscriptionReportsProtocolError(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "cliamp.sock")
	serveConn(t, socket, func(conn net.Conn, _ cliampRequest) {
		_, _ = conn.Write([]byte("{\"version\":2,\"ok\":false,\"error\":{\"code\":\"invalid_version\",\"message\":\"unsupported protocol version\"}}\n"))
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := cliampipc.Subscribe(ctx, socket, diag.Discard()); err == nil {
		t.Fatal("expected an error for a rejected subscription")
	} else if !strings.Contains(err.Error(), "invalid_version") {
		t.Fatalf("err = %v, want it to name the Cliamp error code", err)
	}
}

// A snapshot the daemon cannot use must not vanish silently. Cliamp answers nil
// when it has no player state, and a snapshot built from that answer carries no
// usable status; discarding it is right, discarding it invisibly is not, because
// the gap it leaves in Discord presence then has no explanation anywhere.
//
// The fake sends the unusable snapshot before a good one. The reader consumes
// frames in order, so receiving the good one proves the bad one was handled, and
// the log line for it must already have been written by then.
func TestCliampSubscriptionReportsDiscardedSnapshot(t *testing.T) {
	t.Parallel()
	socket := filepath.Join(t.TempDir(), "cliamp.sock")
	serveConn(t, socket, func(conn net.Conn, _ cliampRequest) {
		_, _ = conn.Write([]byte("{\"version\":2,\"id\":\"discord-rpc-subscribe\",\"ok\":true}\n"))
		_, _ = conn.Write([]byte("{\"event\":\"plugin.discord-rpc.playback\",\"time\":1000,\"data\":{\"title\":\"Track\"}}\n"))
		_, _ = conn.Write([]byte("{\"event\":\"plugin.discord-rpc.playback\",\"time\":1001,\"data\":{\"status\":\"playing\",\"title\":\"Track\"}}\n"))
	})

	var captured bytes.Buffer
	logger := diag.New(&captured, "cliamp")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	states, err := cliampipc.Subscribe(ctx, socket, logger)
	if err != nil {
		t.Fatal(err)
	}

	select {
	case state := <-states:
		if state.ObservedAt != 1001 {
			t.Fatalf("received the rejected snapshot (time %d), want the one published after it", state.ObservedAt)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for playback state")
	}

	if !strings.Contains(captured.String(), "status") {
		t.Errorf("discarded a snapshot without logging why; log was %q", captured.String())
	}
}
