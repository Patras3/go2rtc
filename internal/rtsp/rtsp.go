package rtsp

import (
	"errors"
	"io"
	"net"
	"net/url"
	"strings"

	"github.com/AlexxIT/go2rtc/internal/app"
	"github.com/AlexxIT/go2rtc/internal/streams"
	"github.com/AlexxIT/go2rtc/pkg/core"
	"github.com/AlexxIT/go2rtc/pkg/rtsp"
	"github.com/AlexxIT/go2rtc/pkg/tcp"
	"github.com/rs/zerolog"
)

func Init() {
	var conf struct {
		Mod struct {
			Listen       string `yaml:"listen" json:"listen"`
			Username     string `yaml:"username" json:"-"`
			Password     string `yaml:"password" json:"-"`
			DefaultQuery string `yaml:"default_query" json:"default_query"`
			PacketSize   uint16 `yaml:"pkt_size" json:"pkt_size,omitempty"`
		} `yaml:"rtsp"`
	}

	// default config
	conf.Mod.Listen = ":8554"
	conf.Mod.DefaultQuery = "video&audio"

	app.LoadConfig(&conf)
	app.Info["rtsp"] = conf.Mod

	log = app.GetLogger("rtsp")

	// RTSP client support
	streams.HandleFunc("rtsp", rtspHandler)
	streams.HandleFunc("rtsps", rtspHandler)
	streams.HandleFunc("rtspx", rtspHandler)

	// RTSP server support
	address := conf.Mod.Listen
	if address == "" {
		return
	}

	ln, err := net.Listen("tcp", address)
	if err != nil {
		log.Error().Err(err).Msg("[rtsp] listen")
		return
	}

	_, Port, _ = net.SplitHostPort(address)

	log.Info().Str("addr", address).Msg("[rtsp] listen")

	if query, err := url.ParseQuery(conf.Mod.DefaultQuery); err == nil {
		defaultMedias = ParseQuery(query)
	}

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}

			c := rtsp.NewServer(conn)
			c.PacketSize = conf.Mod.PacketSize
			// skip check auth for localhost
			if conf.Mod.Username != "" && !conn.RemoteAddr().(*net.TCPAddr).IP.IsLoopback() {
				c.Auth(conf.Mod.Username, conf.Mod.Password)
			}
			go tcpHandler(c)
		}
	}()
}

type Handler func(conn *rtsp.Conn) bool

func HandleFunc(handler Handler) {
	handlers = append(handlers, handler)
}

var Port string

// internal

var log zerolog.Logger
var handlers []Handler
var defaultMedias []*core.Media

func rtspHandler(rawURL string) (core.Producer, error) {
	// Support both query string (?timeout=100) for QVR and hash (#timeout=100) for Frigate
	// Parse standard query string first (before #)
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}

	// Get query parameters from both sources
	query := parsedURL.Query()

	// Also support hash-based parameters (go2rtc/Frigate style)
	rawURL, rawQuery, _ := strings.Cut(rawURL, "#")
	if rawQuery != "" {
		hashQuery := streams.ParseQuery(rawQuery)
		// Merge hash parameters into query (hash takes precedence)
		for key, values := range hashQuery {
			for _, value := range values {
				query.Set(key, value)
			}
		}
	}

	conn := rtsp.NewClient(rawURL)
	conn.Backchannel = true
	conn.UserAgent = app.UserAgent

	// Apply parameters from merged query
	if len(query) > 0 {
		conn.Backchannel = query.Get("backchannel") == "1"
		conn.Media = query.Get("media")
		conn.Timeout = core.Atoi(query.Get("timeout"))
		conn.Transport = query.Get("transport")

		// Log timeout parsing for debugging
		if timeoutStr := query.Get("timeout"); timeoutStr != "" {
			log.Warn().
				Str("url", rawURL).
				Int("timeout", conn.Timeout).
				Msg("[QVR-CLIENT] Timeout parameter parsed from producer URL")
		} else {
			log.Warn().
				Str("url", rawURL).
				Msg("[QVR-CLIENT] No timeout parameter in producer URL")
		}
	}

	// Always register a listener for QVR debug messages
	conn.Listen(func(msg any) {
		switch msg := msg.(type) {
		case string:
			// Log QVR-specific messages at WARN level for visibility
			if strings.HasPrefix(msg, "[QVR-") || strings.HasPrefix(msg, "[RTSP]") {
				log.Warn().Msgf("%s", msg)
			} else if log.Trace().Enabled() {
				log.Trace().Msgf("[rtsp] client msg: %s", msg)
			}
		case *tcp.Request:
			if log.Trace().Enabled() {
				log.Trace().Msgf("[rtsp] client request:\n%s", msg)
			}
		case *tcp.Response:
			if log.Trace().Enabled() {
				log.Trace().Msgf("[rtsp] client response:\n%s", msg)
			}
		}
	})

	if err := conn.Dial(); err != nil {
		return nil, err
	}

	if err := conn.Describe(); err != nil {
		if !conn.Backchannel {
			return nil, err
		}
		log.Trace().Msgf("[rtsp] describe (backchannel=%t) err: %v", conn.Backchannel, err)

		// second try without backchannel, we need to reconnect
		conn.Backchannel = false
		if err = conn.Dial(); err != nil {
			return nil, err
		}
		if err = conn.Describe(); err != nil {
			return nil, err
		}
	}

	return conn, nil
}

func tcpHandler(conn *rtsp.Conn) {
	var name string
	var closer func()

	trace := log.Trace().Enabled()
	level := zerolog.WarnLevel

	conn.Listen(func(msg any) {
		if trace {
			switch msg := msg.(type) {
			case *tcp.Request:
				log.Trace().Msgf("[rtsp] server request:\n%s", msg)
			case *tcp.Response:
				log.Trace().Msgf("[rtsp] server response:\n%s", msg)
			}
		}

		switch msg {
		case rtsp.MethodDescribe:
			if len(conn.URL.Path) == 0 {
				log.Warn().Msg("[rtsp] server empty URL on DESCRIBE")
				return
			}

			name = conn.URL.Path[1:]

			stream := streams.Get(name)
			if stream == nil {
				return
			}

			log.Debug().Str("stream", name).Msg("[rtsp] new consumer")

			conn.SessionName = app.UserAgent

			query := conn.URL.Query()

			log.Warn().
				Str("stream", name).
				Str("url", conn.URL.String()).
				Str("query", conn.URL.RawQuery).
				Msg("[QVR-DEBUG] DESCRIBE request received")

			// Support timeout parameter for QVR and other RTSP clients
			if s := query.Get("timeout"); s != "" {
				conn.Timeout = core.Atoi(s)
				log.Warn().
					Int("timeout", conn.Timeout).
					Str("stream", name).
					Msg("[QVR-FIX] Timeout parameter parsed from URL query string")
			} else {
				log.Warn().
					Str("stream", name).
					Msg("[QVR-WARN] No timeout parameter in URL - using defaults")
			}

			conn.Medias = ParseQuery(query)
			if conn.Medias == nil {
				for _, media := range defaultMedias {
					conn.Medias = append(conn.Medias, media.Clone())
				}
			}

			if query.Get("backchannel") == "1" {
				conn.Medias = append(conn.Medias, &core.Media{
					Kind:      core.KindAudio,
					Direction: core.DirectionRecvonly,
					Codecs: []*core.Codec{
						{Name: core.CodecOpus, ClockRate: 48000, Channels: 2},
						{Name: core.CodecPCM, ClockRate: 16000},
						{Name: core.CodecPCMA, ClockRate: 16000},
						{Name: core.CodecPCMU, ClockRate: 16000},
						{Name: core.CodecPCM, ClockRate: 8000},
						{Name: core.CodecPCMA, ClockRate: 8000},
						{Name: core.CodecPCMU, ClockRate: 8000},
					},
				})
			}

			if s := query.Get("pkt_size"); s != "" {
				conn.PacketSize = uint16(core.Atoi(s))
			}

			// param name like ffmpeg style https://ffmpeg.org/ffmpeg-protocols.html
			if s := query.Get("log_level"); s != "" {
				if lvl, err := zerolog.ParseLevel(s); err == nil {
					level = lvl
				}
			}

			// will help to protect looping requests to same source
			conn.Connection.Source = query.Get("source")

			if err := stream.AddConsumer(conn); err != nil {
				log.WithLevel(level).Err(err).Str("stream", name).Msg("[rtsp]")
				return
			}

			closer = func() {
				stream.RemoveConsumer(conn)
			}

		case rtsp.MethodAnnounce:
			if len(conn.URL.Path) == 0 {
				log.Warn().Msg("[rtsp] server empty URL on ANNOUNCE")
				return
			}

			name = conn.URL.Path[1:]

			stream := streams.Get(name)
			if stream == nil {
				return
			}

			query := conn.URL.Query()
			if s := query.Get("timeout"); s != "" {
				conn.Timeout = core.Atoi(s)
			}

			log.Debug().Str("stream", name).Msg("[rtsp] new producer")

			stream.AddProducer(conn)

			closer = func() {
				stream.RemoveProducer(conn)
			}
		}
	})

	if err := conn.Accept(); err != nil {
		if errors.Is(err, rtsp.FailedAuth) {
			log.Warn().Str("remote_addr", conn.Connection.RemoteAddr).Msg("[rtsp] failed authentication")
		} else if err != io.EOF {
			log.WithLevel(level).Err(err).Caller().Send()
		}
		if closer != nil {
			closer()
		}
		_ = conn.Close()
		return
	}

	for _, handler := range handlers {
		if handler(conn) {
			return
		}
	}

	if closer != nil {
		if err := conn.Handle(); err != nil {
			log.Debug().Err(err).Msg("[rtsp] handle")
		}

		closer()

		log.Debug().Str("stream", name).Msg("[rtsp] disconnect")
	}

	_ = conn.Close()
}

func ParseQuery(query map[string][]string) []*core.Media {
	if v := query["mp4"]; v != nil {
		return []*core.Media{
			{
				Kind:      core.KindVideo,
				Direction: core.DirectionSendonly,
				Codecs: []*core.Codec{
					{Name: core.CodecH264},
					{Name: core.CodecH265},
				},
			},
			{
				Kind:      core.KindAudio,
				Direction: core.DirectionSendonly,
				Codecs: []*core.Codec{
					{Name: core.CodecAAC},
				},
			},
		}
	}

	return core.ParseQuery(query)
}
