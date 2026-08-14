package tests

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"testing"
	"time"

	cliampipc "github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/cliamp"
)

func TestCliampSubscriptionReceivesPlayback(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "cliamp.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

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
		var request struct {
			Cmd    string   `json:"cmd"`
			Topics []string `json:"topics"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil || request.Cmd != "subscribe" || len(request.Topics) != 1 || request.Topics[0] != cliampipc.PlaybackTopic {
			return
		}
		_, _ = conn.Write([]byte("{\"ok\":true}\n"))
		_, _ = conn.Write([]byte("{\"event\":\"plugin.discord-rpc.playback\",\"time\":1000,\"data\":{\"status\":\"playing\",\"title\":\"Track\",\"position\":12,\"duration\":200}}\n"))
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	states, err := cliampipc.Subscribe(ctx, socket)
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
