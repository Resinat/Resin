package proxy

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/config"
	M "github.com/sagernet/sing/common/metadata"
)

func startSocks5Session(t *testing.T, inbound *Socks5Inbound) (net.Conn, *bufio.Reader, <-chan struct{}) {
	t.Helper()

	clientConn, serverConn := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		inbound.ServeConn(serverConn)
	}()

	return clientConn, bufio.NewReader(clientConn), done
}

func readExactly(t *testing.T, r io.Reader, n int) []byte {
	t.Helper()

	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		t.Fatalf("read %d bytes: %v", n, err)
	}
	return buf
}

func writeAll(t *testing.T, w io.Writer, data []byte) {
	t.Helper()
	if _, err := w.Write(data); err != nil {
		t.Fatalf("write %d bytes: %v", len(data), err)
	}
}

func socks5UserPassPacket(username, password string) []byte {
	buf := []byte{socks5UserPassVersion, byte(len(username))}
	buf = append(buf, username...)
	buf = append(buf, byte(len(password)))
	buf = append(buf, password...)
	return buf
}

func socks5ConnectIPv4Packet(addr string) []byte {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		panic(err)
	}
	ip := net.ParseIP(host).To4()
	if ip == nil {
		panic("expected IPv4 address")
	}
	port, err := net.LookupPort("tcp", portStr)
	if err != nil {
		panic(err)
	}

	buf := []byte{socks5Version, socks5CommandConnect, 0x00, socks5AddressTypeIPv4}
	buf = append(buf, ip...)
	buf = binary.BigEndian.AppendUint16(buf, uint16(port))
	return buf
}

func TestSocks5Inbound_RejectsWhenAuthVersionIsNotV1(t *testing.T) {
	inbound := NewSocks5Inbound(Socks5InboundConfig{
		ProxyToken:  "tok",
		AuthVersion: string(config.AuthVersionLegacyV0),
	})

	clientConn, reader, done := startSocks5Session(t, inbound)
	defer clientConn.Close()

	reply := readExactly(t, reader, 2)
	if reply[0] != socks5Version || reply[1] != socks5MethodNoAcceptable {
		t.Fatalf("reply: got %v, want [5 255]", reply)
	}

	_ = clientConn.Close()
	<-done
}

func TestSocks5Inbound_EmptyTokenPrefersUserPass(t *testing.T) {
	inbound := NewSocks5Inbound(Socks5InboundConfig{
		AuthVersion: string(config.AuthVersionV1),
	})

	clientConn, reader, done := startSocks5Session(t, inbound)
	defer clientConn.Close()

	writeAll(t, clientConn, []byte{socks5Version, 2, socks5MethodNoAuth, socks5MethodUserPass})
	reply := readExactly(t, reader, 2)
	if reply[1] != socks5MethodUserPass {
		t.Fatalf("selected method: got %d, want %d", reply[1], socks5MethodUserPass)
	}

	_ = clientConn.Close()
	<-done
}

func TestSocks5Inbound_EmptyTokenFallsBackToNoAuth(t *testing.T) {
	inbound := NewSocks5Inbound(Socks5InboundConfig{
		AuthVersion: string(config.AuthVersionV1),
	})

	clientConn, reader, done := startSocks5Session(t, inbound)
	defer clientConn.Close()

	writeAll(t, clientConn, []byte{socks5Version, 1, socks5MethodNoAuth})
	reply := readExactly(t, reader, 2)
	if reply[1] != socks5MethodNoAuth {
		t.Fatalf("selected method: got %d, want %d", reply[1], socks5MethodNoAuth)
	}

	_ = clientConn.Close()
	<-done
}

func TestSocks5Inbound_EmptyTokenUserPassAllowsAnyPassword(t *testing.T) {
	inbound := NewSocks5Inbound(Socks5InboundConfig{
		AuthVersion: string(config.AuthVersionV1),
	})

	clientConn, reader, done := startSocks5Session(t, inbound)
	defer clientConn.Close()

	writeAll(t, clientConn, []byte{socks5Version, 1, socks5MethodUserPass})
	if got := readExactly(t, reader, 2); got[1] != socks5MethodUserPass {
		t.Fatalf("selected method: got %d, want %d", got[1], socks5MethodUserPass)
	}

	writeAll(t, clientConn, socks5UserPassPacket("plat.acct", "any-password"))
	authReply := readExactly(t, reader, 2)
	if authReply[0] != socks5UserPassVersion || authReply[1] != socks5UserPassStatusSuccess {
		t.Fatalf("auth reply: got %v, want success", authReply)
	}

	_ = clientConn.Close()
	<-done
}

func TestSocks5Inbound_UserPassAuthFailureUsesRFC1929Failure(t *testing.T) {
	emitter := newMockEventEmitter()
	inbound := NewSocks5Inbound(Socks5InboundConfig{
		ProxyToken:  "tok",
		AuthVersion: string(config.AuthVersionV1),
		Events:      emitter,
	})

	clientConn, reader, done := startSocks5Session(t, inbound)
	defer clientConn.Close()

	writeAll(t, clientConn, []byte{socks5Version, 1, socks5MethodUserPass})
	if got := readExactly(t, reader, 2); got[1] != socks5MethodUserPass {
		t.Fatalf("selected method: got %d, want %d", got[1], socks5MethodUserPass)
	}

	writeAll(t, clientConn, socks5UserPassPacket("plat.acct", "wrong"))
	authReply := readExactly(t, reader, 2)
	if authReply[0] != socks5UserPassVersion || authReply[1] != socks5UserPassStatusFailure {
		t.Fatalf("auth reply: got %v, want failure", authReply)
	}

	_ = clientConn.Close()
	<-done

	select {
	case ev := <-emitter.logCh:
		t.Fatalf("unexpected request log event on auth failure: %+v", ev)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestSocks5Inbound_CONNECTSuccess_LogsProxyType3(t *testing.T) {
	env := newProxyE2EEnv(t)
	emitter := newMockEventEmitter()
	health := &mockHealthRecorder{}

	targetLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen target: %v", err)
	}
	defer targetLn.Close()

	targetDone := make(chan struct{})
	go func() {
		defer close(targetDone)
		conn, err := targetLn.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = io.Copy(conn, conn)
	}()

	setProxyE2EOutboundDialFunc(t, env, func(ctx context.Context, network string, _ M.Socksaddr) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, targetLn.Addr().String())
	})

	inbound := NewSocks5Inbound(Socks5InboundConfig{
		ProxyToken:  "tok",
		AuthVersion: string(config.AuthVersionV1),
		Router:      env.router,
		Pool:        env.pool,
		Health:      health,
		Events:      emitter,
	})

	clientConn, reader, done := startSocks5Session(t, inbound)
	defer clientConn.Close()

	writeAll(t, clientConn, []byte{socks5Version, 1, socks5MethodUserPass})
	if got := readExactly(t, reader, 2); got[1] != socks5MethodUserPass {
		t.Fatalf("selected method: got %d, want %d", got[1], socks5MethodUserPass)
	}

	writeAll(t, clientConn, socks5UserPassPacket("plat.acct", "tok"))
	if got := readExactly(t, reader, 2); got[1] != socks5UserPassStatusSuccess {
		t.Fatalf("auth status: got %d, want %d", got[1], socks5UserPassStatusSuccess)
	}

	targetAddr := targetLn.Addr().String()
	writeAll(t, clientConn, socks5ConnectIPv4Packet(targetAddr))
	reply := readExactly(t, reader, 10)
	if reply[0] != socks5Version || reply[1] != socks5ReplySucceeded {
		t.Fatalf("connect reply: got %v, want success", reply)
	}

	const payload = "ping-through-socks5"
	writeAll(t, clientConn, []byte(payload))
	echo := readExactly(t, reader, len(payload))
	if string(echo) != payload {
		t.Fatalf("echo payload: got %q, want %q", string(echo), payload)
	}

	_ = clientConn.Close()
	<-done
	<-targetDone

	select {
	case logEv := <-emitter.logCh:
		if logEv.ProxyType != ProxyTypeSocks5Forward {
			t.Fatalf("ProxyType: got %d, want %d", logEv.ProxyType, ProxyTypeSocks5Forward)
		}
		if logEv.TargetHost != targetAddr {
			t.Fatalf("TargetHost: got %q, want %q", logEv.TargetHost, targetAddr)
		}
		if logEv.TargetURL != "" {
			t.Fatalf("TargetURL: got %q, want empty", logEv.TargetURL)
		}
		if logEv.HTTPMethod != "" {
			t.Fatalf("HTTPMethod: got %q, want empty", logEv.HTTPMethod)
		}
		if logEv.HTTPStatus != 0 {
			t.Fatalf("HTTPStatus: got %d, want 0", logEv.HTTPStatus)
		}
		if logEv.Account != "acct" {
			t.Fatalf("Account: got %q, want %q", logEv.Account, "acct")
		}
		if !logEv.NetOK {
			t.Fatal("NetOK: got false, want true")
		}
		if logEv.EgressBytes != int64(len(payload)) {
			t.Fatalf("EgressBytes: got %d, want %d", logEv.EgressBytes, len(payload))
		}
		if logEv.IngressBytes != int64(len(payload)) {
			t.Fatalf("IngressBytes: got %d, want %d", logEv.IngressBytes, len(payload))
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("expected SOCKS5 request log event")
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if health.resultCalls.Load() > 0 {
			if health.lastSuccess.Load() != 1 {
				t.Fatalf("RecordResult lastSuccess: got %d, want 1", health.lastSuccess.Load())
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("expected RecordResult call for SOCKS5 CONNECT success")
}

func TestSocks5Inbound_CONNECTOneWayTrafficStillLogsSuccess(t *testing.T) {
	env := newProxyE2EEnv(t)
	emitter := newMockEventEmitter()
	health := &mockHealthRecorder{}

	targetLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen target: %v", err)
	}
	defer targetLn.Close()

	const payload = "server-push-only"
	targetDone := make(chan struct{})
	go func() {
		defer close(targetDone)
		conn, err := targetLn.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = conn.Write([]byte(payload))
	}()

	setProxyE2EOutboundDialFunc(t, env, func(ctx context.Context, network string, _ M.Socksaddr) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, targetLn.Addr().String())
	})

	inbound := NewSocks5Inbound(Socks5InboundConfig{
		ProxyToken:  "tok",
		AuthVersion: string(config.AuthVersionV1),
		Router:      env.router,
		Pool:        env.pool,
		Health:      health,
		Events:      emitter,
	})

	clientConn, reader, done := startSocks5Session(t, inbound)
	defer clientConn.Close()

	writeAll(t, clientConn, []byte{socks5Version, 1, socks5MethodUserPass})
	if got := readExactly(t, reader, 2); got[1] != socks5MethodUserPass {
		t.Fatalf("selected method: got %d, want %d", got[1], socks5MethodUserPass)
	}

	writeAll(t, clientConn, socks5UserPassPacket("plat.acct", "tok"))
	if got := readExactly(t, reader, 2); got[1] != socks5UserPassStatusSuccess {
		t.Fatalf("auth status: got %d, want %d", got[1], socks5UserPassStatusSuccess)
	}

	targetAddr := targetLn.Addr().String()
	writeAll(t, clientConn, socks5ConnectIPv4Packet(targetAddr))
	reply := readExactly(t, reader, 10)
	if reply[0] != socks5Version || reply[1] != socks5ReplySucceeded {
		t.Fatalf("connect reply: got %v, want success", reply)
	}

	gotPayload := readExactly(t, reader, len(payload))
	if string(gotPayload) != payload {
		t.Fatalf("payload: got %q, want %q", string(gotPayload), payload)
	}

	_ = clientConn.Close()
	<-done
	<-targetDone

	select {
	case logEv := <-emitter.logCh:
		if !logEv.NetOK {
			t.Fatal("one-way SOCKS5 CONNECT should log net_ok=true")
		}
		if logEv.ResinError != "" {
			t.Fatalf("ResinError: got %q, want empty", logEv.ResinError)
		}
		if logEv.UpstreamStage != "" {
			t.Fatalf("UpstreamStage: got %q, want empty", logEv.UpstreamStage)
		}
		if logEv.IngressBytes != int64(len(payload)) {
			t.Fatalf("IngressBytes: got %d, want %d", logEv.IngressBytes, len(payload))
		}
		if logEv.EgressBytes != 0 {
			t.Fatalf("EgressBytes: got %d, want 0", logEv.EgressBytes)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("expected SOCKS5 request log event")
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if health.resultCalls.Load() > 0 {
			if health.lastSuccess.Load() != 1 {
				t.Fatalf("RecordResult lastSuccess: got %d, want 1", health.lastSuccess.Load())
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("expected RecordResult call for one-way SOCKS5 CONNECT success")
}

func TestSocks5Inbound_CONNECTResponseWriteFailureDoesNotPenalizeNode(t *testing.T) {
	env := newProxyE2EEnv(t)
	emitter := newMockEventEmitter()
	health := &mockHealthRecorder{}

	upstreamConn, upstreamPeer := net.Pipe()
	defer upstreamPeer.Close()

	setProxyE2EOutboundDialFunc(t, env, func(context.Context, string, M.Socksaddr) (net.Conn, error) {
		return upstreamConn, nil
	})

	inbound := NewSocks5Inbound(Socks5InboundConfig{
		ProxyToken:  "tok",
		AuthVersion: string(config.AuthVersionV1),
		Router:      env.router,
		Pool:        env.pool,
		Health:      health,
		Events:      emitter,
	})

	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	done := make(chan struct{})
	failingConn := &failOnWriteConn{Conn: serverConn, failAt: 3}
	go func() {
		defer close(done)
		inbound.ServeConn(failingConn)
	}()

	reader := bufio.NewReader(clientConn)
	writeAll(t, clientConn, []byte{socks5Version, 1, socks5MethodUserPass})
	if got := readExactly(t, reader, 2); got[1] != socks5MethodUserPass {
		t.Fatalf("selected method: got %d, want %d", got[1], socks5MethodUserPass)
	}

	writeAll(t, clientConn, socks5UserPassPacket("plat.acct", "tok"))
	if got := readExactly(t, reader, 2); got[1] != socks5UserPassStatusSuccess {
		t.Fatalf("auth status: got %d, want %d", got[1], socks5UserPassStatusSuccess)
	}

	writeAll(t, clientConn, socks5ConnectIPv4Packet("127.0.0.1:443"))

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("ServeConn should return after success reply write failure")
	}

	select {
	case logEv := <-emitter.logCh:
		if logEv.NetOK {
			t.Fatal("SOCKS5 success reply write failure should log net_ok=false")
		}
		if logEv.ResinError != ErrUpstreamRequestFailed.ResinError {
			t.Fatalf("ResinError: got %q, want %q", logEv.ResinError, ErrUpstreamRequestFailed.ResinError)
		}
		if logEv.UpstreamStage != "socks5_connect_response_write" {
			t.Fatalf("UpstreamStage: got %q, want %q", logEv.UpstreamStage, "socks5_connect_response_write")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("expected SOCKS5 log event")
	}

	time.Sleep(50 * time.Millisecond)
	if health.resultCalls.Load() != 0 {
		t.Fatalf("SOCKS5 success reply write failure should not record health result, got %d calls", health.resultCalls.Load())
	}
}

func TestSocks5Inbound_CONNECTDialFailure_ReturnsGeneralFailure(t *testing.T) {
	env := newProxyE2EEnv(t)
	emitter := newMockEventEmitter()
	health := &mockHealthRecorder{}

	setProxyE2EOutboundDialFunc(t, env, func(context.Context, string, M.Socksaddr) (net.Conn, error) {
		return nil, genericErr{}
	})

	inbound := NewSocks5Inbound(Socks5InboundConfig{
		ProxyToken:  "tok",
		AuthVersion: string(config.AuthVersionV1),
		Router:      env.router,
		Pool:        env.pool,
		Health:      health,
		Events:      emitter,
	})

	clientConn, reader, done := startSocks5Session(t, inbound)
	defer clientConn.Close()

	writeAll(t, clientConn, []byte{socks5Version, 1, socks5MethodUserPass})
	if got := readExactly(t, reader, 2); got[1] != socks5MethodUserPass {
		t.Fatalf("selected method: got %d, want %d", got[1], socks5MethodUserPass)
	}

	writeAll(t, clientConn, socks5UserPassPacket("plat.acct", "tok"))
	if got := readExactly(t, reader, 2); got[1] != socks5UserPassStatusSuccess {
		t.Fatalf("auth status: got %d, want %d", got[1], socks5UserPassStatusSuccess)
	}

	const targetAddr = "127.0.0.1:443"
	writeAll(t, clientConn, socks5ConnectIPv4Packet(targetAddr))
	reply := readExactly(t, reader, 10)
	if reply[0] != socks5Version || reply[1] != socks5ReplyGeneralFailure {
		t.Fatalf("connect reply: got %v, want general failure", reply)
	}

	_ = clientConn.Close()
	<-done

	select {
	case logEv := <-emitter.logCh:
		if logEv.ProxyType != ProxyTypeSocks5Forward {
			t.Fatalf("ProxyType: got %d, want %d", logEv.ProxyType, ProxyTypeSocks5Forward)
		}
		if logEv.TargetHost != targetAddr {
			t.Fatalf("TargetHost: got %q, want %q", logEv.TargetHost, targetAddr)
		}
		if logEv.HTTPStatus != 0 {
			t.Fatalf("HTTPStatus: got %d, want 0", logEv.HTTPStatus)
		}
		if logEv.ResinError != ErrUpstreamConnectFailed.ResinError {
			t.Fatalf("ResinError: got %q, want %q", logEv.ResinError, ErrUpstreamConnectFailed.ResinError)
		}
		if logEv.UpstreamStage != "connect_dial" {
			t.Fatalf("UpstreamStage: got %q, want %q", logEv.UpstreamStage, "connect_dial")
		}
		if logEv.NetOK {
			t.Fatal("NetOK: got true, want false")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("expected SOCKS5 failure log event")
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if health.resultCalls.Load() > 0 {
			if health.lastSuccess.Load() != 0 {
				t.Fatalf("RecordResult lastSuccess: got %d, want 0", health.lastSuccess.Load())
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("expected RecordResult call for SOCKS5 CONNECT failure")
}

func TestSocks5Inbound_ClientCloseBeforeSuccessReplyDoesNotCancelPrepareDial(t *testing.T) {
	env := newProxyE2EEnv(t)
	emitter := newMockEventEmitter()
	health := &mockHealthRecorder{}
	dialRelease := make(chan struct{})
	upstreamConn, upstreamPeer := net.Pipe()
	defer upstreamPeer.Close()

	setProxyE2EOutboundDialFunc(t, env, func(_ context.Context, _ string, _ M.Socksaddr) (net.Conn, error) {
		<-dialRelease
		return upstreamConn, nil
	})

	inbound := NewSocks5Inbound(Socks5InboundConfig{
		ProxyToken:  "tok",
		AuthVersion: string(config.AuthVersionV1),
		Router:      env.router,
		Pool:        env.pool,
		Health:      health,
		Events:      emitter,
	})

	clientConn, reader, done := startSocks5Session(t, inbound)

	writeAll(t, clientConn, []byte{socks5Version, 1, socks5MethodUserPass})
	if got := readExactly(t, reader, 2); got[1] != socks5MethodUserPass {
		t.Fatalf("selected method: got %d, want %d", got[1], socks5MethodUserPass)
	}
	writeAll(t, clientConn, socks5UserPassPacket("plat.acct", "tok"))
	if got := readExactly(t, reader, 2); got[1] != socks5UserPassStatusSuccess {
		t.Fatalf("auth status: got %d, want %d", got[1], socks5UserPassStatusSuccess)
	}
	writeAll(t, clientConn, socks5ConnectIPv4Packet("127.0.0.1:443"))
	_ = clientConn.Close()
	close(dialRelease)

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("ServeConn should return after client close and delayed dial release")
	}

	select {
	case logEv := <-emitter.logCh:
		if logEv.NetOK {
			t.Fatal("client-closed-before-success SOCKS5 CONNECT should log net_ok=false")
		}
		if logEv.ProxyType != ProxyTypeSocks5Forward {
			t.Fatalf("ProxyType: got %d, want %d", logEv.ProxyType, ProxyTypeSocks5Forward)
		}
		if logEv.ResinError != ErrUpstreamRequestFailed.ResinError {
			t.Fatalf("ResinError: got %q, want %q", logEv.ResinError, ErrUpstreamRequestFailed.ResinError)
		}
		if logEv.UpstreamStage != "socks5_connect_response_write" {
			t.Fatalf("UpstreamStage: got %q, want %q", logEv.UpstreamStage, "socks5_connect_response_write")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("expected SOCKS5 log event")
	}

	time.Sleep(50 * time.Millisecond)
	if health.resultCalls.Load() != 0 {
		t.Fatalf("client-closed-before-success SOCKS5 CONNECT should not record health result, got %d calls", health.resultCalls.Load())
	}
}

func TestSocks5Inbound_EarlyPayloadBeforeSuccessReplyFlowsAfterSuccess(t *testing.T) {
	env := newProxyE2EEnv(t)
	emitter := newMockEventEmitter()
	health := &mockHealthRecorder{}

	targetLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen target: %v", err)
	}
	defer targetLn.Close()

	const payload = "early-payload-before-success"
	targetErrCh := make(chan error, 1)
	targetDone := make(chan struct{})
	go func() {
		defer close(targetDone)
		conn, err := targetLn.Accept()
		if err != nil {
			targetErrCh <- err
			return
		}
		defer conn.Close()
		gotPayload := make([]byte, len(payload))
		if _, err := io.ReadFull(conn, gotPayload); err != nil {
			targetErrCh <- err
			return
		}
		if string(gotPayload) != payload {
			targetErrCh <- errors.New("target payload mismatch")
			return
		}
		if _, err := conn.Write(gotPayload); err != nil {
			targetErrCh <- err
			return
		}
		targetErrCh <- nil
	}()

	setProxyE2EOutboundDialFunc(t, env, func(ctx context.Context, network string, _ M.Socksaddr) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, targetLn.Addr().String())
	})

	inbound := NewSocks5Inbound(Socks5InboundConfig{
		ProxyToken:  "tok",
		AuthVersion: string(config.AuthVersionV1),
		Router:      env.router,
		Pool:        env.pool,
		Health:      health,
		Events:      emitter,
	})

	clientConn, reader, done := startSocks5Session(t, inbound)
	defer clientConn.Close()

	writeAll(t, clientConn, []byte{socks5Version, 1, socks5MethodUserPass})
	if got := readExactly(t, reader, 2); got[1] != socks5MethodUserPass {
		t.Fatalf("selected method: got %d, want %d", got[1], socks5MethodUserPass)
	}
	writeAll(t, clientConn, socks5UserPassPacket("plat.acct", "tok"))
	if got := readExactly(t, reader, 2); got[1] != socks5UserPassStatusSuccess {
		t.Fatalf("auth status: got %d, want %d", got[1], socks5UserPassStatusSuccess)
	}

	targetAddr := targetLn.Addr().String()
	writeAll(t, clientConn, append(socks5ConnectIPv4Packet(targetAddr), []byte(payload)...))

	reply := readExactly(t, reader, 10)
	if reply[0] != socks5Version || reply[1] != socks5ReplySucceeded {
		t.Fatalf("reply: got %v, want success", reply)
	}
	echo := readExactly(t, reader, len(payload))
	if string(echo) != payload {
		t.Fatalf("echo payload: got %q, want %q", string(echo), payload)
	}

	_ = clientConn.Close()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("ServeConn should return after relaying early payload")
	}
	<-targetDone
	if err := <-targetErrCh; err != nil {
		t.Fatalf("target handling failed: %v", err)
	}

	select {
	case logEv := <-emitter.logCh:
		if logEv.ProxyType != ProxyTypeSocks5Forward {
			t.Fatalf("ProxyType: got %d, want %d", logEv.ProxyType, ProxyTypeSocks5Forward)
		}
		if logEv.TargetHost != targetAddr {
			t.Fatalf("TargetHost: got %q, want %q", logEv.TargetHost, targetAddr)
		}
		if logEv.ResinError != "" {
			t.Fatalf("ResinError: got %q, want empty", logEv.ResinError)
		}
		if logEv.UpstreamStage != "" {
			t.Fatalf("UpstreamStage: got %q, want empty", logEv.UpstreamStage)
		}
		if !logEv.NetOK {
			t.Fatal("early-payload SOCKS5 CONNECT should log net_ok=true")
		}
		if logEv.EgressBytes != int64(len(payload)) {
			t.Fatalf("EgressBytes: got %d, want %d", logEv.EgressBytes, len(payload))
		}
		if logEv.IngressBytes != int64(len(payload)) {
			t.Fatalf("IngressBytes: got %d, want %d", logEv.IngressBytes, len(payload))
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("expected SOCKS5 success log event")
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if health.resultCalls.Load() > 0 {
			if health.lastSuccess.Load() != 1 {
				t.Fatalf("RecordResult lastSuccess: got %d, want 1", health.lastSuccess.Load())
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("expected RecordResult call for early-payload SOCKS5 CONNECT success")
}

func TestSocks5Inbound_BaseContextCancelCancelsPrepareDial(t *testing.T) {
	env := newProxyE2EEnv(t)
	emitter := newMockEventEmitter()
	health := &mockHealthRecorder{}

	setProxyE2EOutboundDialFunc(t, env, func(ctx context.Context, _ string, _ M.Socksaddr) (net.Conn, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})

	inbound := NewSocks5Inbound(Socks5InboundConfig{
		ProxyToken:  "tok",
		AuthVersion: string(config.AuthVersionV1),
		Router:      env.router,
		Pool:        env.pool,
		Health:      health,
		Events:      emitter,
	})

	clientConn, serverConn := net.Pipe()
	done := make(chan struct{})
	baseCtx, cancelBase := context.WithCancel(context.Background())
	go func() {
		defer close(done)
		inbound.ServeConnContext(baseCtx, serverConn)
	}()
	reader := bufio.NewReader(clientConn)

	writeAll(t, clientConn, []byte{socks5Version, 1, socks5MethodUserPass})
	if got := readExactly(t, reader, 2); got[1] != socks5MethodUserPass {
		t.Fatalf("selected method: got %d, want %d", got[1], socks5MethodUserPass)
	}
	writeAll(t, clientConn, socks5UserPassPacket("plat.acct", "tok"))
	if got := readExactly(t, reader, 2); got[1] != socks5UserPassStatusSuccess {
		t.Fatalf("auth status: got %d, want %d", got[1], socks5UserPassStatusSuccess)
	}
	writeAll(t, clientConn, socks5ConnectIPv4Packet("127.0.0.1:443"))
	cancelBase()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("ServeConnContext should return after base context cancel")
	}

	select {
	case logEv := <-emitter.logCh:
		if !logEv.NetOK {
			t.Fatal("base-context-canceled SOCKS5 CONNECT should log net_ok=true")
		}
		if logEv.ProxyType != ProxyTypeSocks5Forward {
			t.Fatalf("ProxyType: got %d, want %d", logEv.ProxyType, ProxyTypeSocks5Forward)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("expected SOCKS5 log event")
	}

	time.Sleep(50 * time.Millisecond)
	if health.resultCalls.Load() != 0 {
		t.Fatalf("base-context-canceled SOCKS5 CONNECT should not record health result, got %d calls", health.resultCalls.Load())
	}

	_ = clientConn.Close()
}

func TestSocks5Inbound_BaseContextCancelInterruptsHandshake(t *testing.T) {
	inbound := NewSocks5Inbound(Socks5InboundConfig{
		ProxyToken:  "tok",
		AuthVersion: string(config.AuthVersionV1),
	})

	clientConn, serverConn := net.Pipe()
	done := make(chan struct{})
	baseCtx, cancelBase := context.WithCancel(context.Background())
	go func() {
		defer close(done)
		inbound.ServeConnContext(baseCtx, serverConn)
	}()

	writeAll(t, clientConn, []byte{socks5Version, 1})
	cancelBase()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("ServeConnContext should return after base context cancel interrupts handshake")
	}

	_ = clientConn.Close()
}

func TestSocks5Inbound_HandshakeTimeoutInterruptsStalledSession(t *testing.T) {
	inbound := NewSocks5Inbound(Socks5InboundConfig{
		ProxyToken:  "tok",
		AuthVersion: string(config.AuthVersionV1),
	})

	prevTimeout := socks5HandshakeTimeout
	socks5HandshakeTimeout = 20 * time.Millisecond
	defer func() {
		socks5HandshakeTimeout = prevTimeout
	}()

	clientConn, serverConn := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		inbound.ServeConn(serverConn)
	}()

	writeAll(t, clientConn, []byte{socks5Version, 1})

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("ServeConn should return after handshake timeout")
	}

	_ = clientConn.Close()
}
