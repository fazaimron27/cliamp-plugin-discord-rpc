// Package cliamp subscribes to plugin events over Cliamp's local IPC socket.
package cliamp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/playback"
)

const PlaybackTopic = "plugin.discord-rpc.playback"

type response struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
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

	request, err := json.Marshal(map[string]any{
		"cmd":    "subscribe",
		"topics": []string{PlaybackTopic},
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
	if !ack.OK {
		return fail(fmt.Errorf("Cliamp rejected subscription: %s", ack.Error))
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
			if err := json.Unmarshal(scanner.Bytes(), &message); err != nil || message.Event != PlaybackTopic {
				continue
			}
			var state playback.State
			if err := json.Unmarshal(message.Data, &state); err != nil || state.Validate() != nil {
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
