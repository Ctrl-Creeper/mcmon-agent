package mcping

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net"
	"time"
)

type Result struct {
	OK        bool
	LatencyMs float64
	Err       error
}

func Ping(host string, port int, timeout time.Duration, protocolVersion int) Result {
	addr := net.JoinHostPort(host, fmt.Sprintf("%d", port))
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return Result{OK: false, Err: err}
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(timeout))

	if tc, ok := conn.(*net.TCPConn); ok {
		tc.SetNoDelay(true)
	}

	r := bufio.NewReader(conn)

	handshake := new(bytes.Buffer)
	writeVarInt(handshake, protocolVersion)
	writeString(handshake, host)
	binary.Write(handshake, binary.BigEndian, uint16(port))
	writeVarInt(handshake, 1)
	if err := writePacket(conn, 0x00, handshake.Bytes()); err != nil {
		return Result{OK: false, Err: err}
	}

	if err := writePacket(conn, 0x00, nil); err != nil {
		return Result{OK: false, Err: err}
	}

	pid, payload, err := readPacket(r)
	if err != nil {
		return Result{OK: false, Err: err}
	}
	if pid != 0x00 {
		return Result{OK: false, Err: fmt.Errorf("unexpected status packet id %d", pid)}
	}
	br := bytes.NewReader(payload)
	jsonLen, err := readVarInt(br)
	if err != nil {
		return Result{OK: false, Err: err}
	}
	jsonBytes := make([]byte, jsonLen)
	if _, err := readFull(br, jsonBytes); err != nil {
		return Result{OK: false, Err: err}
	}
	var status map[string]any
	if err := json.Unmarshal(jsonBytes, &status); err != nil {
		return Result{OK: false, Err: fmt.Errorf("invalid status json: %w", err)}
	}

	nowMs := time.Now().UnixMilli()
	pingPayload := new(bytes.Buffer)
	binary.Write(pingPayload, binary.BigEndian, nowMs)

	t0 := time.Now()
	if err := writePacket(conn, 0x01, pingPayload.Bytes()); err != nil {
		return Result{OK: false, Err: err}
	}
	pid2, _, err := readPacket(r)
	if err != nil {
		return Result{OK: false, Err: err}
	}
	if pid2 != 0x01 {
		return Result{OK: false, Err: fmt.Errorf("unexpected pong packet id %d", pid2)}
	}
	latency := time.Since(t0)

	return Result{OK: true, LatencyMs: float64(latency.Microseconds()) / 1000.0}
}

func readFull(r *bytes.Reader, buf []byte) (int, error) {
	n, err := r.Read(buf)
	for n < len(buf) && err == nil {
		var m int
		m, err = r.Read(buf[n:])
		n += m
	}
	return n, err
}

func writeVarInt(buf *bytes.Buffer, v int) {
	uv := uint32(v)
	for {
		if uv&^0x7F == 0 {
			buf.WriteByte(byte(uv))
			return
		}
		buf.WriteByte(byte(uv&0x7F) | 0x80)
		uv >>= 7
	}
}

func readVarInt(r interface{ ReadByte() (byte, error) }) (int, error) {
	var result uint32
	var numRead int
	for {
		b, err := r.ReadByte()
		if err != nil {
			return 0, err
		}
		result |= (uint32(b) & 0x7F) << (7 * numRead)
		numRead++
		if numRead > 5 {
			return 0, fmt.Errorf("varint too long")
		}
		if b&0x80 == 0 {
			break
		}
	}
	return int(result), nil
}

func writeString(buf *bytes.Buffer, s string) {
	writeVarInt(buf, len(s))
	buf.WriteString(s)
}

func writePacket(conn net.Conn, packetID int, payload []byte) error {
	body := new(bytes.Buffer)
	writeVarInt(body, packetID)
	body.Write(payload)
	packet := new(bytes.Buffer)
	writeVarInt(packet, body.Len())
	packet.Write(body.Bytes())
	_, err := conn.Write(packet.Bytes())
	return err
}

func readPacket(r *bufio.Reader) (packetID int, payload []byte, err error) {
	length, err := readVarInt(r)
	if err != nil {
		return 0, nil, err
	}
	data := make([]byte, length)
	if _, err := readFullReader(r, data); err != nil {
		return 0, nil, err
	}
	br := bytes.NewReader(data)
	pid, err := readVarInt(br)
	if err != nil {
		return 0, nil, err
	}
	rest := make([]byte, br.Len())
	br.Read(rest)
	return pid, rest, nil
}

func readFullReader(r *bufio.Reader, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := r.Read(buf[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}
