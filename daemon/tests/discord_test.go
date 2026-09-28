package tests

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/discord"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/presence"
)

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

	client := discord.NewClient("123")
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

// Discord answers one handshake and then goes quiet for a while, so a second
// connection attempt fails at the socket Discord actually published. Connect
// walks a list of candidates and used to report whichever it tried last, which
// is a path that never existed, hiding the socket that refused us.
func TestDiscordConnectNamesTheSocketThatRefusedIt(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", dir)
	// Empty the other search roots so the only candidate that exists is the one
	// this test listens on.
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
		// Accepting and then going silent stands in for Discord once it has seen
		// enough handshakes, without depending on how long Discord waits.
		<-time.After(50 * time.Millisecond)
		_ = conn.Close()
	}()

	client := discord.NewClient("123")
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
