package discord

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"time"
)

type opcode uint32

const (
	opHandshake opcode = 0
	opFrame     opcode = 1
	opClose     opcode = 2
	opPing      opcode = 3
	opPong      opcode = 4

	maxFrameSize = 1 << 20
	ioTimeout    = 2 * time.Second
)

func readFrame(conn net.Conn) (opcode, []byte, error) {
	_ = conn.SetReadDeadline(time.Now().Add(ioTimeout))
	header := make([]byte, 8)
	if _, err := io.ReadFull(conn, header); err != nil {
		return 0, nil, err
	}
	op := opcode(binary.LittleEndian.Uint32(header[:4]))
	length := binary.LittleEndian.Uint32(header[4:])
	if length > maxFrameSize {
		return 0, nil, errors.New("Discord IPC frame too large")
	}
	payload := make([]byte, length)
	_, err := io.ReadFull(conn, payload)
	return op, payload, err
}

func writeFrame(conn net.Conn, op opcode, payload []byte) error {
	if len(payload) > maxFrameSize {
		return errors.New("Discord IPC frame too large")
	}
	var packet bytes.Buffer
	_ = binary.Write(&packet, binary.LittleEndian, op)
	_ = binary.Write(&packet, binary.LittleEndian, uint32(len(payload)))
	_, _ = packet.Write(payload)
	_ = conn.SetWriteDeadline(time.Now().Add(ioTimeout))
	_, err := conn.Write(packet.Bytes())
	return err
}
