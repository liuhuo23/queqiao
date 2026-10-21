package protocol

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestMessageRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	in := Hello{
		Type:         TypeHello,
		Version:      Version,
		PublicKey:    "pub",
		MachineID:    "mid",
		Hostname:     "host",
		OS:           "darwin",
		Arch:         "arm64",
		AgentVersion: "0.1.0",
	}
	if err := WriteMessage(&buf, &in); err != nil {
		t.Fatal(err)
	}
	var out Hello
	if err := ReadMessage(&buf, &out); err != nil {
		t.Fatal(err)
	}
	if out != in {
		t.Fatalf("往返不一致: %+v != %+v", out, in)
	}
}

func TestOversizeFrameRejected(t *testing.T) {
	var buf bytes.Buffer
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], MaxFrame+1)
	buf.Write(hdr[:])
	if _, err := ReadRaw(&buf); err != ErrFrameTooLarge {
		t.Fatalf("应拒绝超限帧, got %v", err)
	}
}

func TestPeekType(t *testing.T) {
	raw := []byte(`{"type":"ping","ts":1}`)
	typ, err := PeekType(raw)
	if err != nil || typ != TypePing {
		t.Fatalf("PeekType=%q err=%v", typ, err)
	}
}
