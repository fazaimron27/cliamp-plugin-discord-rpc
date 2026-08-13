package tests

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"path/filepath"
	"testing"

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
