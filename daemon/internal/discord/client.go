// Package discord implements Discord RPC over the local IPC socket.
package discord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"time"

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
	conn          net.Conn
	nonce         uint64
}

func NewClient(applicationID string) *Client {
	return &Client{applicationID: applicationID}
}

func (c *Client) Connected() bool { return c.conn != nil }

// Connect probes known socket locations and completes Discord's handshake.
func (c *Client) Connect(ctx context.Context) error {
	if c.conn != nil {
		return nil
	}
	var lastErr error
	dialer := net.Dialer{Timeout: 500 * time.Millisecond}
	for _, path := range SocketPaths() {
		conn, err := dialer.DialContext(ctx, "unix", path)
		if err != nil {
			lastErr = err
			continue
		}
		if err := verifyPeer(conn); err != nil {
			lastErr = err
			_ = conn.Close()
			continue
		}
		c.conn = conn
		if err := c.handshake(); err != nil {
			lastErr = err
			_ = c.Close()
			continue
		}
		log.Printf("connected to Discord at %s", path)
		return nil
	}
	if lastErr == nil {
		lastErr = errors.New("no Discord IPC socket candidates")
	}
	return fmt.Errorf("Discord IPC unavailable: %w", lastErr)
}

func (c *Client) SetActivity(activity *presence.Activity) error {
	return c.setActivity(activity)
}

func (c *Client) ClearActivity() error {
	return c.setActivity(nil)
}

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
			return fmt.Errorf("Discord rejected activity: %s", response.Data)
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
