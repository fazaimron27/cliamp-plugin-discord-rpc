package cliamp

// This file is the request half of the ipc transport. Where client.go streams
// events for as long as a subscription lives, this asks one question and reads
// one answer over a connection of its own. The daemon uses it for the artwork
// Cliamp holds but never publishes to plugins, so nothing here is fatal: a
// failure is the absence of artwork, and the caller treats it as one.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"time"
)

// stateID identifies this request so an answer meant for a different request is
// reported rather than read as the answer to this one.
const stateID = "discord-rpc-state"

// stateRequestFrame is the v2 frame that asks Cliamp for its runtime state.
type stateRequestFrame struct {
	Version int    `json:"version"`
	ID      string `json:"id"`
	Method  string `json:"method"`
}

// SnapshotTrack is the track a state answer describes. Path is the track's
// identity for this daemon, and it is the same value the plugin publishes, so
// an answer can be matched to the track it is about.
type SnapshotTrack struct {
	Path        string `json:"path"`
	AlbumArtURL string `json:"album_art_url"`
}

// Snapshot is the part of one state answer the daemon reads. Everything else
// Cliamp reports about its runtime is left undecoded, because nothing else is
// this side's to act on.
type Snapshot struct {
	Track SnapshotTrack `json:"track"`
}

// State asks Cliamp for its runtime state and returns the current track and the
// artwork URL it holds for it.
//
// It runs on its own short-lived connection rather than on the subscription, so
// a slow or absent answer cannot disturb the event stream the daemon depends
// on. Every unusable answer is an error rather than an empty snapshot, because
// an empty snapshot is indistinguishable from a track the player holds no
// artwork for.
func State(ctx context.Context, socketPath string) (Snapshot, error) {
	dialer := net.Dialer{Timeout: 3 * time.Second}
	conn, err := dialer.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return Snapshot{}, fmt.Errorf("connect to Cliamp IPC: %w", err)
	}
	defer conn.Close()

	request, err := json.Marshal(stateRequestFrame{
		Version: protocolVersion,
		ID:      stateID,
		Method:  "state.get",
	})
	if err != nil {
		return Snapshot{}, fmt.Errorf("encode Cliamp state request: %w", err)
	}
	request = append(request, '\n')
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write(request); err != nil {
		return Snapshot{}, fmt.Errorf("request Cliamp state: %w", err)
	}

	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 64*1024), 128*1024)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return Snapshot{}, fmt.Errorf("read Cliamp state response: %w", err)
		}
		return Snapshot{}, errors.New("Cliamp closed the state request without a response")
	}
	var reply response
	if err := json.Unmarshal(scanner.Bytes(), &reply); err != nil {
		return Snapshot{}, fmt.Errorf("decode Cliamp state response: %w", err)
	}
	if err := reply.checkState(); err != nil {
		return Snapshot{}, err
	}
	var snapshot Snapshot
	if err := json.Unmarshal(reply.Snapshot, &snapshot); err != nil {
		return Snapshot{}, fmt.Errorf("decode Cliamp state snapshot: %w", err)
	}
	return snapshot, nil
}

// checkState validates the parts of an answer that decide whether its snapshot
// may be read at all.
func (r response) checkState() error {
	if r.Version != protocolVersion {
		return fmt.Errorf("Cliamp IPC response version %d, want %d", r.Version, protocolVersion)
	}
	if r.ID != "" && r.ID != stateID {
		return fmt.Errorf("Cliamp state response ID %q, want %q", r.ID, stateID)
	}
	if !r.OK {
		if r.Error == nil {
			return errors.New("Cliamp rejected the state request without an error")
		}
		return fmt.Errorf("Cliamp rejected the state request: %s: %s", r.Error.Code, r.Error.Message)
	}
	if len(r.Snapshot) == 0 {
		return errors.New("Cliamp state response carried no snapshot")
	}
	return nil
}
