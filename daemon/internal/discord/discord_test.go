package discord_test

// This file exercises the Discord IPC client against a socket a test stands up
// in place of Discord: it answers the handshake and the activity frames the
// client sends, so both the successful path and the connect-failure path can be
// tested without a running Discord.

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/diag"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/discord"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/presence"
)

// A stand-in Discord accepts the connection, checks the handshake and one
// SET_ACTIVITY request, and answers the activity by nonce. The client reaching
// the end without an error proves the handshake and an activity publish both
// complete against a well-behaved peer.
func TestDiscordClientHandshakeAndActivity(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", dir)
	socket := filepath.Join(dir, "discord-ipc-0")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	done := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			done <- err
			return
		}
		defer conn.Close()
		opcode, payload, err := readTestFrame(conn)
		if err != nil || opcode != 0 {
			done <- err
			return
		}
		var handshake map[string]any
		if err := json.Unmarshal(payload, &handshake); err != nil || handshake["client_id"] != "123" {
			done <- err
			return
		}
		if err := writeTestFrame(conn, 1, map[string]any{"evt": "READY"}); err != nil {
			done <- err
			return
		}
		opcode, payload, err = readTestFrame(conn)
		if err != nil || opcode != 1 {
			done <- err
			return
		}
		var request struct {
			Nonce string `json:"nonce"`
			Args  struct {
				Activity struct {
					Details string `json:"details"`
				} `json:"activity"`
			} `json:"args"`
		}
		if err := json.Unmarshal(payload, &request); err != nil || request.Args.Activity.Details != "Track" {
			done <- err
			return
		}
		done <- writeTestFrame(conn, 1, map[string]any{"nonce": request.Nonce})
	}()

	client := discord.NewClient("123", diag.Discard())
	if err := client.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.SetActivity(&presence.Activity{Details: "Track"}); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// Discord accepts the handshake and the request and then refuses the activity
// itself, which is the shape of a 4000. The refusal is reported as its own type
// because the caller has to tell it from a broken socket: Discord took the bytes
// and rejected their contents, so the connection is still good and a new one
// would refuse the same payload. The client must leave the socket open, which
// the last assertion pins.
func TestDiscordSetActivityReportsARejection(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", dir)
	socket := filepath.Join(dir, "discord-ipc-0")
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
		if _, _, err := readTestFrame(conn); err != nil {
			return
		}
		if err := writeTestFrame(conn, 1, map[string]any{"evt": "READY"}); err != nil {
			return
		}
		_, payload, err := readTestFrame(conn)
		if err != nil {
			return
		}
		var request struct {
			Nonce string `json:"nonce"`
		}
		if err := json.Unmarshal(payload, &request); err != nil {
			return
		}
		detail, err := json.Marshal(map[string]any{"code": 4000, "message": "Invalid payload"})
		if err != nil {
			return
		}
		_ = writeTestFrame(conn, 1, map[string]any{
			"evt":   "ERROR",
			"nonce": request.Nonce,
			"data":  json.RawMessage(detail),
		})
	}()

	client := discord.NewClient("123", diag.Discard())
	if err := client.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	err = client.SetActivity(&presence.Activity{Details: "Track"})
	if err == nil {
		t.Fatal("SetActivity() succeeded against a peer that refused the activity")
	}
	var rejection *discord.RejectionError
	if !errors.As(err, &rejection) {
		t.Fatalf("a refused activity is not reported as a rejection: %v", err)
	}
	if !strings.Contains(rejection.Detail, "4000") {
		t.Fatalf("rejection does not carry Discord's own detail: %q", rejection.Detail)
	}
	if !client.Connected() {
		t.Fatal("client dropped a connection Discord only refused a payload on")
	}
}

// Discord answers one handshake and then goes quiet for a while, so a second
// connection attempt fails at the socket Discord actually published. Connect
// walks a list of candidates and used to report whichever it tried last, which
// is a path that never existed, hiding the socket that refused us.
//
// The other search roots are emptied so the only candidate that exists is the
// one this test listens on.
//
// Accepting and then going silent stands in for Discord once it has seen enough
// handshakes, without depending on how long Discord waits.
func TestDiscordConnectNamesTheSocketThatRefusedIt(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", dir)
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("TMP", t.TempDir())
	t.Setenv("TEMP", t.TempDir())

	socket := filepath.Join(dir, "discord-ipc-0")
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
		<-time.After(50 * time.Millisecond)
		_ = conn.Close()
	}()

	client := discord.NewClient("123", diag.Discard())
	err = client.Connect(context.Background())
	if err == nil {
		t.Fatal("Connect() succeeded against a socket that never answered the handshake")
	}
	if !strings.Contains(err.Error(), socket) {
		t.Fatalf("error does not name the socket that refused us: %v", err)
	}
}

func readTestFrame(conn net.Conn) (uint32, []byte, error) {
	header := make([]byte, 8)
	if _, err := io.ReadFull(conn, header); err != nil {
		return 0, nil, err
	}
	payload := make([]byte, binary.LittleEndian.Uint32(header[4:]))
	_, err := io.ReadFull(conn, payload)
	return binary.LittleEndian.Uint32(header[:4]), payload, err
}

func writeTestFrame(conn net.Conn, opcode uint32, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	header := make([]byte, 8)
	binary.LittleEndian.PutUint32(header[:4], opcode)
	binary.LittleEndian.PutUint32(header[4:], uint32(len(payload)))
	if _, err := conn.Write(header); err != nil {
		return err
	}
	_, err = conn.Write(payload)
	return err
}
