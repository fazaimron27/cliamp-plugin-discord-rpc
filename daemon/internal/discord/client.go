// Package discord implements Discord RPC over the local IPC socket.
package discord

// This file is the Discord IPC client: it discovers a socket, verifies the peer
// it reached, completes the handshake, and publishes activities over the
// connection it opened.
//
// Connect depends on SocketPaths being ordered. The first candidate is the one
// Discord would have published, so it is the one named when nothing answers;
// naming a later one would report a path that was never plausible to begin
// with.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"time"

	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/diag"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/presence"
)

type frame struct {
	Event string          `json:"evt"`
	Nonce string          `json:"nonce"`
	Data  json.RawMessage `json:"data"`
}

// Client maintains one authenticated Discord IPC connection.
type Client struct {
	applicationID string
	logger        diag.Logger
	conn          net.Conn
	nonce         uint64
}

// NewClient returns a client for the Discord application with no connection
// open. Connect discovers the socket and completes the handshake.
//
// The logger is where Connect's one line about the socket it reached is
// written. It is required rather than defaulted, because the caller that omits
// it is the caller that has not decided where this component's output belongs,
// and a silent default would hide that until someone went looking for a line
// that was never written.
func NewClient(applicationID string, logger diag.Logger) *Client {
	return &Client{applicationID: applicationID, logger: logger}
}

// RejectionError reports that Discord took a request and refused its contents.
//
// It is a type rather than a message because the two failures out of
// SetActivity call for opposite remedies. A rejection means Discord read the
// bytes and refused what they said, so the connection is still good and only a
// different payload can succeed; every other error leaves the socket in doubt
// and is repaired by reconnecting. A caller that cannot tell them apart
// reconnects on a rejection, sends the same bytes down a fresh socket, and is
// refused again -- which is how one unacceptable field becomes an endless loop
// of teardowns.
type RejectionError struct {
	// Detail is the body of Discord's ERROR frame, kept verbatim so the journal
	// carries what Discord actually said rather than a paraphrase of it.
	Detail string
}

// Error renders the rejection in the words the daemon has always logged for it.
func (e *RejectionError) Error() string {
	return "Discord rejected activity: " + e.Detail
}

// Connected reports whether a connection is currently open. It only answers the
// question; Connect is what repairs a client that reads false.
func (c *Client) Connected() bool { return c.conn != nil }

// Connect probes known socket locations and completes Discord's handshake.
//
// Two failures are kept apart because they call for different fixes. A candidate
// that was never there says Discord is not running; a candidate that accepted
// the connection and then refused the handshake says Discord is running but
// would not talk to us. Only the second one is reported when both occur, because
// it is the one that explains the outcome.
func (c *Client) Connect(ctx context.Context) error {
	if c.conn != nil {
		return nil
	}
	var absentErr, refusedErr error
	dialer := net.Dialer{Timeout: 500 * time.Millisecond}
	for _, path := range SocketPaths() {
		conn, err := dialer.DialContext(ctx, "unix", path)
		if err != nil {
			if absentErr == nil {
				absentErr = err
			}
			continue
		}
		if err := verifyPeer(conn); err != nil {
			if refusedErr == nil {
				refusedErr = fmt.Errorf("%s: %w", path, err)
			}
			_ = conn.Close()
			continue
		}
		c.conn = conn
		if err := c.handshake(); err != nil {
			if refusedErr == nil {
				refusedErr = fmt.Errorf("%s: %w", path, err)
			}
			_ = c.Close()
			continue
		}
		c.logger.Printf("connected to Discord at %s", path)
		return nil
	}
	switch {
	case refusedErr != nil:
		return fmt.Errorf("Discord IPC unavailable: %w", refusedErr)
	case absentErr != nil:
		return fmt.Errorf("Discord IPC unavailable: %w", absentErr)
	default:
		return errors.New("Discord IPC unavailable: no Discord IPC socket candidates")
	}
}

// SetActivity publishes an activity, replacing whatever Discord is showing.
func (c *Client) SetActivity(activity *presence.Activity) error {
	return c.setActivity(activity)
}

// ClearActivity removes the current activity. It is how pause and stop are
// reported: Discord cannot freeze an activity's timer, so a cleared card is
// what reads as paused.
func (c *Client) ClearActivity() error {
	return c.setActivity(nil)
}

// Close ends the connection if one is open, and is safe on a client that never
// connected.
func (c *Client) Close() error {
	if c.conn == nil {
		return nil
	}
	err := c.conn.Close()
	c.conn = nil
	return err
}

func (c *Client) handshake() error {
	if err := c.write(opHandshake, map[string]any{"v": 1, "client_id": c.applicationID}); err != nil {
		return err
	}
	for {
		op, payload, err := readFrame(c.conn)
		if err != nil {
			return err
		}
		if op == opPing {
			if err := writeFrame(c.conn, opPong, payload); err != nil {
				return err
			}
			continue
		}
		if op == opClose {
			return fmt.Errorf("Discord rejected handshake: %s", payload)
		}
		var response frame
		if op == opFrame && json.Unmarshal(payload, &response) == nil && response.Event == "READY" {
			return nil
		}
	}
}

func (c *Client) setActivity(activity *presence.Activity) error {
	if c.conn == nil {
		return errors.New("Discord IPC is not connected")
	}
	c.nonce++
	nonce := strconv.FormatUint(c.nonce, 10)
	payload := map[string]any{
		"cmd":   "SET_ACTIVITY",
		"nonce": nonce,
		"args":  map[string]any{"pid": os.Getpid(), "activity": activity},
	}
	if err := c.write(opFrame, payload); err != nil {
		return err
	}

	for {
		op, data, err := readFrame(c.conn)
		if err != nil {
			return err
		}
		if op == opPing {
			if err := writeFrame(c.conn, opPong, data); err != nil {
				return err
			}
			continue
		}
		if op == opClose {
			return fmt.Errorf("Discord closed IPC: %s", data)
		}
		var response frame
		if op != opFrame || json.Unmarshal(data, &response) != nil || response.Nonce != nonce {
			continue
		}
		if response.Event == "ERROR" {
			return &RejectionError{Detail: string(response.Data)}
		}
		return nil
	}
}

func (c *Client) write(op opcode, value any) error {
	if c.conn == nil {
		return errors.New("Discord IPC is not connected")
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return writeFrame(c.conn, op, payload)
}
