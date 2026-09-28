// Package cliamp subscribes to plugin events over Cliamp's local IPC socket.
package cliamp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"time"

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

type response struct {
	Version int       `json:"version"`
	ID      string    `json:"id,omitempty"`
	OK      bool      `json:"ok"`
	Error   *rpcError `json:"error,omitempty"`
}

type event struct {
	Event string          `json:"event"`
	Time  int64           `json:"time"`
	Data  json.RawMessage `json:"data"`
}

// Subscribe connects to Cliamp and returns retained and live playback states.
// The channel closes when Cliamp exits or the connection fails.
func Subscribe(ctx context.Context, socketPath string) (<-chan playback.State, error) {
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
	if ack.Version != protocolVersion {
		return fail(fmt.Errorf("Cliamp IPC response version %d, want %d", ack.Version, protocolVersion))
	}
	if ack.ID != "" && ack.ID != subscriptionID {
		return fail(fmt.Errorf("Cliamp subscription response ID %q, want %q", ack.ID, subscriptionID))
	}
	if !ack.OK {
		if ack.Error == nil {
			return fail(errors.New("Cliamp rejected subscription without an error"))
		}
		return fail(fmt.Errorf("Cliamp rejected subscription: %s: %s", ack.Error.Code, ack.Error.Message))
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
				log.Printf("cliamp: discarding undecodable frame: %v", err)
				continue
			}
			if message.Event != PlaybackTopic {
				continue
			}
			// A rejected snapshot is dropped rather than republished, so it
			// leaves Discord showing whatever the last good snapshot said. Say
			// so: this is the only account of why presence stopped tracking.
			var state playback.State
			if err := json.Unmarshal(message.Data, &state); err != nil {
				log.Printf("cliamp: discarding unreadable %s snapshot: %v", PlaybackTopic, err)
				continue
			}
			if err := state.Validate(); err != nil {
				log.Printf("cliamp: discarding invalid %s snapshot: %v", PlaybackTopic, err)
				continue
			}
			state.ObservedAt = message.Time
			select {
			case states <- state:
			default:
				// Presence needs the newest complete snapshot, not every
				// intermediate transition from a burst of Cliamp events.
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
