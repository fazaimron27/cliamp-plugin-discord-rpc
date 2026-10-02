// Package cliamp subscribes to plugin events over Cliamp's local IPC socket.
package cliamp

// This file is the subscription half of the ipc transport: it completes
// Cliamp's version 2 handshake, then streams playback snapshots for as long as
// the socket stays open. The file transport's counterpart, which reads the state
// document instead, is the statewatch package.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/diag"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/playback"
)

// PlaybackTopic is the retained pub/sub topic the plugin publishes playback
// snapshots on. Cliamp builds the plugin.discord-rpc.* namespace from the
// installed plugin's filename rather than from anything the plugin sends, so
// this name cannot be used to impersonate another plugin.
const PlaybackTopic = "plugin.discord-rpc.playback"

// protocolVersion is the mandatory Cliamp IPC envelope version. Cliamp rejects
// unversioned frames with a structured invalid_version error, so this constant
// must track Cliamp's docs/upgrading-ipc-v2.md.
const protocolVersion = 2

// subscriptionID identifies this request so a mismatched acknowledgment is
// reported rather than accepted as the answer to a different request.
const subscriptionID = "discord-rpc-subscribe"

type subscriptionRequest struct {
	Version int      `json:"version"`
	ID      string   `json:"id"`
	Method  string   `json:"method"`
	Topics  []string `json:"topics"`
}

// rpcError mirrors Cliamp's v2 error object. It is a struct rather than a
// string because v2 reports failures as an object, not a bare message.
type rpcError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// response is the v2 answer frame, shared by the subscription acknowledgement
// and the state request. The snapshot is kept raw because only the state
// request reads it, and the two callers decode different parts.
type response struct {
	Version  int             `json:"version"`
	ID       string          `json:"id,omitempty"`
	OK       bool            `json:"ok"`
	Snapshot json.RawMessage `json:"snapshot,omitempty"`
	Error    *rpcError       `json:"error,omitempty"`
}

type event struct {
	Event string          `json:"event"`
	Time  int64           `json:"time"`
	Data  json.RawMessage `json:"data"`
}

// checkEnvelope validates the parts of an answer frame that every response must
// satisfy whatever it was an answer to: the protocol version, the reply ID when
// the frame carries one, and the rejection branch.
//
// It is the one site for a rule the subscription acknowledgment and the state
// request used to spell separately, right down to a byte-identical version
// line. The subject names the request in the message, so a line still says
// which half of the transport failed.
func (r response) checkEnvelope(id, subject string) error {
	if r.Version != protocolVersion {
		return fmt.Errorf("Cliamp IPC response version %d, want %d", r.Version, protocolVersion)
	}
	if r.ID != "" && r.ID != id {
		return fmt.Errorf("Cliamp %s response ID %q, want %q", subject, r.ID, id)
	}
	if !r.OK {
		if r.Error == nil {
			return fmt.Errorf("Cliamp rejected the %s request without an error", subject)
		}
		return fmt.Errorf("Cliamp rejected the %s request: %s: %s", subject, r.Error.Code, r.Error.Message)
	}
	return nil
}

// Subscribe connects to Cliamp and returns retained and live playback states.
// The channel closes when Cliamp exits or the connection fails.
//
// The channel holds one snapshot and drops the oldest to make room, because
// presence needs the newest complete snapshot rather than every intermediate
// transition from a burst of Cliamp events.
//
// A frame that cannot be decoded, and a snapshot that fails validation, are each
// logged and dropped rather than republished. Discord therefore keeps showing
// the last good snapshot, and that log line is the only account of why presence
// stopped tracking.
//
// The line is written through logger, which the caller built with this
// package's name, so it arrives already attributed and no call site here spells
// the prefix itself.
func Subscribe(ctx context.Context, socketPath string, logger diag.Logger) (<-chan playback.State, error) {
	dialer := net.Dialer{Timeout: 3 * time.Second}
	conn, err := dialer.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("connect to Cliamp IPC: %w", err)
	}
	fail := func(err error) (<-chan playback.State, error) {
		_ = conn.Close()
		return nil, err
	}

	request, err := json.Marshal(subscriptionRequest{
		Version: protocolVersion,
		ID:      subscriptionID,
		Method:  "subscribe",
		Topics:  []string{PlaybackTopic},
	})
	if err != nil {
		return fail(fmt.Errorf("encode Cliamp subscription: %w", err))
	}
	request = append(request, '\n')
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write(request); err != nil {
		return fail(fmt.Errorf("subscribe to Cliamp events: %w", err))
	}

	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 64*1024), 128*1024)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return fail(fmt.Errorf("read Cliamp subscription response: %w", err))
		}
		return fail(errors.New("Cliamp closed subscription without a response"))
	}
	var ack response
	if err := json.Unmarshal(scanner.Bytes(), &ack); err != nil {
		return fail(fmt.Errorf("decode Cliamp subscription response: %w", err))
	}
	if err := ack.checkEnvelope(subscriptionID, "subscription"); err != nil {
		return fail(err)
	}
	_ = conn.SetDeadline(time.Time{})

	states := make(chan playback.State, 1)
	go func() {
		defer close(states)
		defer conn.Close()
		stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
		defer stop()

		for scanner.Scan() {
			var message event
			if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
				logger.Printf("discarding undecodable frame: %v", err)
				continue
			}
			if message.Event != PlaybackTopic {
				continue
			}
			var state playback.State
			if err := json.Unmarshal(message.Data, &state); err != nil {
				logger.Printf("discarding unreadable %s snapshot: %v", PlaybackTopic, err)
				continue
			}
			if err := state.Validate(); err != nil {
				logger.Printf("discarding invalid %s snapshot: %v", PlaybackTopic, err)
				continue
			}
			state.ObservedAt = message.Time
			select {
			case states <- state:
			default:
				select {
				case <-states:
				default:
				}
				select {
				case states <- state:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return states, nil
}
