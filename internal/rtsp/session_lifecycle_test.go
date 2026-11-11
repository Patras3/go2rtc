package rtsp

import (
	"bufio"
	"encoding/json"
	"net"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AlexxIT/go2rtc/internal/streams"
	"github.com/AlexxIT/go2rtc/pkg/core"
	pkg "github.com/AlexxIT/go2rtc/pkg/rtsp"
	"github.com/AlexxIT/go2rtc/pkg/tcp"
)

// This source is external: it doesn't reconnect or depend on an actual camera.
type sessionTestProducer struct{ core.Connection }

func (*sessionTestProducer) Start() error { return nil }

type deadlineConn struct {
	net.Conn
	mu              sync.Mutex
	deadline        time.Time
	accelerateAfter time.Duration // test-only: exercise the seven-day expiry without waiting
}

func (c *deadlineConn) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	c.deadline = t
	c.mu.Unlock()
	if c.accelerateAfter > 0 && time.Until(t) > 6*24*time.Hour {
		return c.Conn.SetReadDeadline(time.Now().Add(c.accelerateAfter))
	}
	return c.Conn.SetReadDeadline(t)
}

func (c *deadlineConn) readDeadline() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.deadline
}

func testConsumerCount(t *testing.T, s *streams.Stream) int {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var data struct {
		Consumers []json.RawMessage `json:"consumers"`
	}
	if err := json.Unmarshal(b, &data); err != nil {
		t.Fatal(err)
	}
	return len(data.Consumers)
}

func waitConsumerCount(t *testing.T, s *streams.Stream, want int) {
	t.Helper()
	until := time.Now().Add(3 * time.Second)
	for time.Now().Before(until) {
		if got := testConsumerCount(t, s); got == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("consumer count = %d, want %d", testConsumerCount(t, s), want)
}

func TestQVRConsumerLifecycle(t *testing.T) {
	name := "session_lifecycle_test"
	s, err := streams.New(name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { streams.Delete(name) })
	media := &core.Media{Kind: core.KindVideo, Direction: core.DirectionRecvonly, Codecs: []*core.Codec{{Name: core.CodecH264, ClockRate: 90000, PayloadType: 96}}}
	prod := &sessionTestProducer{Connection: core.Connection{Medias: []*core.Media{media}}}
	s.AddProducer(prod)

	server, client := net.Pipe()
	tracked := &deadlineConn{Conn: server}
	done := make(chan struct{})
	go func() { tcpHandler(pkg.NewServer(tracked)); close(done) }()
	t.Cleanup(func() {
		client.Close()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("RTSP handler did not exit")
		}
	})
	client.SetDeadline(time.Now().Add(3 * time.Second))
	rd := bufio.NewReader(client)
	request := func(method, path, transport string) *tcp.Response {
		t.Helper()
		u, err := url.Parse("rtsp://localhost/" + name + path)
		if err != nil {
			t.Fatal(err)
		}
		r := &tcp.Request{Method: method, URL: u, Proto: pkg.ProtoRTSP, Header: map[string][]string{"CSeq": {"1"}}}
		if transport != "" {
			r.Header.Set("Transport", transport)
		}
		if err := r.Write(client); err != nil {
			t.Fatal(err)
		}
		res, err := tcp.ReadResponse(rd)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(res.Status, "200") {
			t.Fatalf("%s: %s", method, res.Status)
		}
		return res
	}

	request(pkg.MethodDescribe, "?video", "")
	waitConsumerCount(t, s, 1)
	res := request(pkg.MethodSetup, "/trackID=0", "RTP/AVP/TCP;unicast;interleaved=0-1")
	if got := res.Header.Get("Session"); !strings.Contains(got, ";timeout=86400") {
		t.Fatalf("QVR Session header: %q", got)
	}
	request(pkg.MethodPlay, "", "")
	// Healthy 24/7 recording must not be removed for lack of RTSP control traffic.
	time.Sleep(30 * time.Millisecond)
	if got := testConsumerCount(t, s); got != 1 {
		t.Fatalf("healthy consumer count = %d", got)
	}
	if d := time.Until(tracked.readDeadline()); d < 6*24*time.Hour || d > 8*24*time.Hour {
		t.Fatalf("read deadline must remain 7 days, got %s", d)
	}
	request(pkg.MethodOptions, "", "")
	if got := testConsumerCount(t, s); got != 1 {
		t.Fatalf("healthy consumer removed by OPTIONS: %d", got)
	}

	// A peer close (EOF in net.Pipe, analogous to a TCP FIN) must remove an
	// abandoned QVR consumer independently of the 7-day inactivity timeout.
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("abandoned session handler stuck")
	}
	waitConsumerCount(t, s, 0)
}

// A silent peer (no FIN/RST/TEARDOWN) leaves a passive consumer registered
// until its read deadline. This models the application-visible half-open case,
// not a missing kernel socket: net.Pipe has no kernel socket to inspect.
func TestSilentQVRPeerExpiresOnlyAtReadDeadline(t *testing.T) {
	name := "silent_session_lifecycle_test"
	s, err := streams.New(name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { streams.Delete(name) })
	media := &core.Media{Kind: core.KindVideo, Direction: core.DirectionRecvonly, Codecs: []*core.Codec{{Name: core.CodecH264, ClockRate: 90000, PayloadType: 96}}}
	s.AddProducer(&sessionTestProducer{Connection: core.Connection{Medias: []*core.Media{media}}})

	server, client := net.Pipe()
	tracked := &deadlineConn{Conn: server, accelerateAfter: 250 * time.Millisecond}
	done := make(chan struct{})
	go func() { tcpHandler(pkg.NewServer(tracked)); close(done) }()
	t.Cleanup(func() {
		client.Close()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("silent peer handler did not exit")
		}
	})
	client.SetDeadline(time.Now().Add(3 * time.Second))
	rd := bufio.NewReader(client)
	request := func(method, path, transport string) *tcp.Response {
		t.Helper()
		u, err := url.Parse("rtsp://localhost/" + name + path)
		if err != nil {
			t.Fatal(err)
		}
		r := &tcp.Request{Method: method, URL: u, Proto: pkg.ProtoRTSP, Header: map[string][]string{"CSeq": {"1"}}}
		if transport != "" {
			r.Header.Set("Transport", transport)
		}
		if err := r.Write(client); err != nil {
			t.Fatal(err)
		}
		res, err := tcp.ReadResponse(rd)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(res.Status, "200") {
			t.Fatalf("%s: %s", method, res.Status)
		}
		return res
	}
	request(pkg.MethodDescribe, "?video", "")
	res := request(pkg.MethodSetup, "/trackID=0", "RTP/AVP/TCP;unicast;interleaved=0-1")
	if got := res.Header.Get("Session"); !strings.Contains(got, ";timeout=86400") {
		t.Fatalf("QVR Session header: %q", got)
	}
	request(pkg.MethodPlay, "", "")
	until := time.Now().Add(3 * time.Second)
	for time.Until(tracked.readDeadline()) < 6*24*time.Hour && time.Now().Before(until) {
		time.Sleep(time.Millisecond)
	}
	if d := time.Until(tracked.readDeadline()); d < 6*24*time.Hour || d > 8*24*time.Hour {
		t.Fatalf("logical read deadline must remain seven days, got %s", d)
	}
	// The peer remains open and idle. No control traffic or EOF can clean it.
	select {
	case <-done:
		t.Fatal("silent peer disconnected before deadline")
	case <-time.After(40 * time.Millisecond):
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("silent peer was not removed at simulated seven-day deadline")
	}
	waitConsumerCount(t, s, 0)
}
