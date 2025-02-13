package rtsp

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/AlexxIT/go2rtc/pkg/core"
	"github.com/AlexxIT/go2rtc/pkg/tcp"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
)

type Conn struct {
	core.Connection
	core.Listener

	Backchannel bool
	Media       string
	OnClose     func() error
	PacketSize  uint16
	SessionName string
	Timeout     int
	Transport   string // custom transport support, ex. RTSP over WebSocket

	URL *url.URL

	auth      *tcp.Auth
	conn      net.Conn
	keepalive int
	mode      core.Mode
	playOK    bool
	reader    *bufio.Reader
	sequence  int
	session   string
	uri       string

	state   State
	stateMu sync.Mutex
}

// Hardcode total timeout for reads/writes to 24h
const HardcodedTimeout = 24 * time.Hour

const (
	ProtoRTSP      = "RTSP/1.0"
	MethodOptions  = "OPTIONS"
	MethodSetup    = "SETUP"
	MethodTeardown = "TEARDOWN"
	MethodDescribe = "DESCRIBE"
	MethodPlay     = "PLAY"
	MethodPause    = "PAUSE"
	MethodAnnounce = "ANNOUNCE"
	MethodRecord   = "RECORD"
)

type State byte

func (s State) String() string {
	switch s {
	case StateNone:
		return "NONE"
	case StateConn:
		return "CONN"
	case StateSetup:
		return MethodSetup
	case StatePlay:
		return MethodPlay
	}
	return strconv.Itoa(int(s))
}

const (
	StateNone State = iota
	StateConn
	StateSetup
	StatePlay
)

func (c *Conn) Handle() error {
	// Always use 24h for read deadlines
	timeout := HardcodedTimeout

	var keepaliveDT time.Duration
	var keepaliveTS time.Time

	switch c.mode {
	case core.ModeActiveProducer:
		if c.keepalive > 5 {
			keepaliveDT = time.Duration(c.keepalive-5) * time.Second
		} else {
			keepaliveDT = 25 * time.Second
		}
		keepaliveTS = time.Now().Add(keepaliveDT)

	case core.ModePassiveProducer:
		// No specialized logic now; always 24h.
	case core.ModePassiveConsumer:
		// No specialized logic now; always 24h.
	default:
		return fmt.Errorf("wrong RTSP conn mode: %d", c.mode)
	}

	for c.state != StateNone {
		ts := time.Now()

		if err := c.conn.SetReadDeadline(ts.Add(timeout)); err != nil {
			return err
		}

		// Peek 4 bytes to distinguish between '$' (interleaved RTP) or RTSP messages
		buf4, err := c.reader.Peek(4)
		if err != nil {
			return err
		}

		var channelID byte
		var size uint16

		if buf4[0] != '$' {
			switch string(buf4) {
			case "RTSP":
				res, err2 := c.ReadResponse()
				if err2 != nil {
					return err2
				}
				c.Fire(res)
				// For playing backchannel only after OK response on play
				c.playOK = true
				continue

			case "OPTI", "TEAR", "DESC", "SETU", "PLAY", "PAUS", "RECO", "ANNO", "GET_", "SET_":
				req, err2 := c.ReadRequest()
				if err2 != nil {
					return err2
				}
				c.Fire(req)
				if req.Method == MethodOptions {
					res := &tcp.Response{Request: req}
					if err2 = c.WriteResponse(res); err2 != nil {
						return err2
					}
				}
				continue

			default:
				c.Fire("RTSP wrong input")

				for i := 0; ; i++ {
					// search next '$'
					_, err2 := c.reader.ReadBytes('$')
					if err2 != nil {
						return err2
					}
					channelID, err2 = c.reader.ReadByte()
					if err2 != nil {
						return err2
					}
					if channelID >= 20 {
						continue
					}
					buf2 := make([]byte, 2)
					if _, err2 = io.ReadFull(c.reader, buf2); err2 != nil {
						return err2
					}
					size = binary.BigEndian.Uint16(buf2)
					if size <= 1500 {
						break
					}
					if i >= 10 {
						return fmt.Errorf("RTSP wrong input")
					}
				}
			}
		} else {
			channelID = buf4[1]
			size = binary.BigEndian.Uint16(buf4[2:])
			if _, err := c.reader.Discard(4); err != nil {
				return err
			}
		}

		buf := make([]byte, size)
		if _, err := io.ReadFull(c.reader, buf); err != nil {
			return err
		}

		c.Recv += int(size)

		if channelID&1 == 0 {
			// even channel => RTP
			packet := &rtp.Packet{}
			if err := packet.Unmarshal(buf); err != nil {
				return err
			}
			for _, receiver := range c.Receivers {
				if receiver.ID == channelID {
					receiver.WriteRTP(packet)
					break
				}
			}
		} else {
			// odd channel => RTCP
			msg := &RTCP{Channel: channelID}
			if err := msg.Header.Unmarshal(buf); err != nil {
				continue
			}
			var err2 error
			msg.Packets, err2 = rtcp.Unmarshal(buf)
			if err2 != nil {
				continue
			}
			c.Fire(msg)
		}

		// keepalive if needed
		if keepaliveDT != 0 && ts.After(keepaliveTS) {
			req := &tcp.Request{Method: MethodOptions, URL: c.URL}
			if err := c.WriteRequest(req); err != nil {
				return err
			}
			keepaliveTS = ts.Add(keepaliveDT)
		}
	}

	return nil
}

func (c *Conn) WriteRequest(req *tcp.Request) error {
	if req.Proto == "" {
		req.Proto = ProtoRTSP
	}
	if req.Header == nil {
		req.Header = make(map[string][]string)
	}

	c.sequence++
	req.Header["CSeq"] = []string{strconv.Itoa(c.sequence)}
	c.auth.Write(req)

	if c.session != "" {
		req.Header.Set("Session", c.session)
	}
	if req.Body != nil {
		req.Header.Set("Content-Length", strconv.Itoa(len(req.Body)))
	}
	c.Fire(req)

	if err := c.conn.SetWriteDeadline(time.Now().Add(HardcodedTimeout)); err != nil {
		return err
	}
	return req.Write(c.conn)
}

func (c *Conn) ReadRequest() (*tcp.Request, error) {
	if err := c.conn.SetReadDeadline(time.Now().Add(HardcodedTimeout)); err != nil {
		return nil, err
	}
	return tcp.ReadRequest(c.reader)
}

func (c *Conn) WriteResponse(res *tcp.Response) error {
	if res.Proto == "" {
		res.Proto = ProtoRTSP
	}
	if res.Status == "" {
		res.Status = "200 OK"
	}
	if res.Header == nil {
		res.Header = make(map[string][]string)
	}
	if res.Request != nil && res.Request.Header != nil {
		seq := res.Request.Header.Get("CSeq")
		if seq != "" {
			res.Header.Set("CSeq", seq)
		}
	}
	// Always append ;timeout=86400 if there's a session
	if c.session != "" {
		res.Header.Set("Session", c.session+";timeout=86400")
	}
	if res.Body != nil {
		res.Header.Set("Content-Length", strconv.Itoa(len(res.Body)))
	}
	c.Fire(res)

	if err := c.conn.SetWriteDeadline(time.Now().Add(HardcodedTimeout)); err != nil {
		return err
	}
	return res.Write(c.conn)
}

func (c *Conn) ReadResponse() (*tcp.Response, error) {
	if err := c.conn.SetReadDeadline(time.Now().Add(HardcodedTimeout)); err != nil {
		return nil, err
	}
	return tcp.ReadResponse(c.reader)
}
