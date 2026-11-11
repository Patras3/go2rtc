package rtsp

import (
	"bufio"
	"net"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/AlexxIT/go2rtc/internal/streams"
	"github.com/AlexxIT/go2rtc/pkg/core"
	pkg "github.com/AlexxIT/go2rtc/pkg/rtsp"
	"github.com/AlexxIT/go2rtc/pkg/tcp"
)

// A client may repeat DESCRIBE on one RTSP TCP connection. The consumer
// registry must still have one entry and close must remove that entry.
func TestRepeatedDescribeDoesNotLeakQVRConsumer(t *testing.T) {
	name := "repeated_describe_test"
	s, err := streams.New(name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { streams.Delete(name) })
	media := &core.Media{Kind: core.KindVideo, Direction: core.DirectionRecvonly, Codecs: []*core.Codec{{Name: core.CodecH264, ClockRate: 90000, PayloadType: 96}}}
	s.AddProducer(&sessionTestProducer{Connection: core.Connection{Medias: []*core.Media{media}}})

	server, client := net.Pipe()
	done := make(chan struct{})
	go func() { tcpHandler(pkg.NewServer(server)); close(done) }()
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
	seq := 0
	requestRaw := func(method, path, transport string) *tcp.Response {
		t.Helper()
		seq++
		u, err := url.Parse("rtsp://localhost/" + name + path)
		if err != nil {
			t.Fatal(err)
		}
		r := &tcp.Request{Method: method, URL: u, Proto: pkg.ProtoRTSP, Header: map[string][]string{"CSeq": {strconv.Itoa(seq)}}}
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
		return res
	}
	request := func(method, path, transport string) *tcp.Response {
		t.Helper()
		res := requestRaw(method, path, transport)
		if !strings.HasPrefix(res.Status, "200") {
			t.Fatalf("%s response: %s", method, res.Status)
		}
		return res
	}

	request(pkg.MethodDescribe, "?video", "")
	waitConsumerCount(t, s, 1)
	describe := request(pkg.MethodDescribe, "?video", "")
	if got := strings.Count(string(describe.Body), "trackID="); got != 1 {
		t.Fatalf("repeated DESCRIBE advertised %d tracks, want 1", got)
	}
	if got := testConsumerCount(t, s); got != 1 {
		t.Fatalf("repeated DESCRIBE registered %d consumers on one TCP connection, want 1", got)
	}
	// A different stream cannot silently reuse the old consumer on this
	// single-session RTSP connection; the client must open a new connection.
	rejected := requestRaw(pkg.MethodDescribe, "_other?video", "")
	if !strings.HasPrefix(rejected.Status, "400") {
		t.Fatalf("DESCRIBE of a different stream: %s, want 400", rejected.Status)
	}
	if got := testConsumerCount(t, s); got != 1 {
		t.Fatalf("different-stream DESCRIBE changed consumer count to %d", got)
	}
	res := request(pkg.MethodSetup, "/trackID=0", "RTP/AVP/TCP;unicast;interleaved=0-1")
	if got := res.Header.Get("Session"); !strings.Contains(got, ";timeout=86400") {
		t.Fatalf("QVR Session header: %q", got)
	}
	request(pkg.MethodPlay, "", "")
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("RTSP handler stuck after peer close")
	}
	waitConsumerCount(t, s, 0)
}
