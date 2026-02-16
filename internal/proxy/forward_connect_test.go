package proxy

import (
	"bufio"
	"io"
	"net"
	"testing"
	"time"
)

func TestMakeTunnelClientReader_PreservesBufferedBytes(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	clientReader := bufio.NewReaderSize(clientConn, 64)

	const firstChunk = "hello"
	const secondChunk = " world"

	go func() {
		_, _ = serverConn.Write([]byte(firstChunk))
		time.Sleep(10 * time.Millisecond)
		_, _ = serverConn.Write([]byte(secondChunk))
		_ = serverConn.Close()
	}()

	// Simulate net/http having pre-read bytes into the hijacked buffer.
	if _, err := clientReader.Peek(len(firstChunk)); err != nil {
		t.Fatalf("peek buffered bytes: %v", err)
	}

	merged, err := makeTunnelClientReader(clientConn, clientReader)
	if err != nil {
		t.Fatalf("make tunnel client reader: %v", err)
	}

	got, err := io.ReadAll(merged)
	if err != nil {
		t.Fatalf("read merged stream: %v", err)
	}
	if string(got) != firstChunk+secondChunk {
		t.Fatalf("merged stream mismatch: got %q, want %q", string(got), firstChunk+secondChunk)
	}
}

func TestMakeTunnelClientReader_NoBufferedBytesReturnsConn(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	clientReader := bufio.NewReader(clientConn)
	merged, err := makeTunnelClientReader(clientConn, clientReader)
	if err != nil {
		t.Fatalf("make tunnel client reader: %v", err)
	}
	if merged != clientConn {
		t.Fatal("expected raw client conn when no buffered bytes are present")
	}
}
