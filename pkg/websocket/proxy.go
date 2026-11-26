package websocket

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/labstack/echo/v4"
	"github.com/sirupsen/logrus"
)

type Config struct {
	ReadBufferSize    int           `yaml:"readBufferSize"`
	WriteBufferSize   int           `yaml:"writeBufferSize"`
	HandshakeTimeout  time.Duration `yaml:"handshakeTimeout"`
	ReadTimeout       time.Duration `yaml:"readTimeout"` // idle time allowed between messages or pongs
	WriteTimeout      time.Duration `yaml:"writeTimeout"`
	PingPeriod        time.Duration `yaml:"pingPeriod"` // keep-alive pings to both sides
	MaxMessageSize    int64         `yaml:"maxMessageSize"`
	EnableCompression bool          `yaml:"enableCompression"`
}

type Proxy struct {
	config   Config
	logger   *logrus.Logger
	upgrader websocket.Upgrader
}

type Connection struct {
	clientConn *websocket.Conn
	serverConn *websocket.Conn
	proxy      *Proxy
	target     string
	done       chan struct{}
	once       sync.Once
}

func NewProxy(config Config, logger *logrus.Logger) *Proxy {
	if config.ReadBufferSize == 0 {
		config.ReadBufferSize = 4096
	}
	if config.WriteBufferSize == 0 {
		config.WriteBufferSize = 4096
	}
	if config.HandshakeTimeout == 0 {
		config.HandshakeTimeout = 10 * time.Second
	}
	if config.ReadTimeout == 0 {
		config.ReadTimeout = 60 * time.Second
	}
	if config.WriteTimeout == 0 {
		config.WriteTimeout = 10 * time.Second
	}
	if config.PingPeriod == 0 || config.PingPeriod >= config.ReadTimeout {
		config.PingPeriod = config.ReadTimeout * 9 / 10
	}
	if config.MaxMessageSize == 0 {
		config.MaxMessageSize = 512 * 1024
	}

	upgrader := websocket.Upgrader{
		ReadBufferSize:   config.ReadBufferSize,
		WriteBufferSize:  config.WriteBufferSize,
		HandshakeTimeout: config.HandshakeTimeout,
		// The client's Origin is forwarded to the upstream, which accepted the same handshake
		// before the client is upgraded, so the origin decision belongs to the upstream.
		CheckOrigin:       func(r *http.Request) bool { return true },
		EnableCompression: config.EnableCompression,
	}

	return &Proxy{
		config:   config,
		logger:   logger,
		upgrader: upgrader,
	}
}

// handshakeHeaders are set by the websocket library itself and must not be copied.
var handshakeHeaders = map[string]bool{
	"Upgrade":                  true,
	"Connection":               true,
	"Sec-Websocket-Key":        true,
	"Sec-Websocket-Version":    true,
	"Sec-Websocket-Extensions": true,
	"Sec-Websocket-Protocol":   true,
	"Host":                     true,
	"Content-Length":           true,
	"Keep-Alive":               true,
	"Proxy-Connection":         true,
	"Te":                       true,
	"Trailer":                  true,
	"Transfer-Encoding":        true,
}

// ProxyWebSocket proxies the client's WebSocket handshake to targetURL (http(s) or ws(s)),
// forwarding the client's headers.
func (p *Proxy) ProxyWebSocket(c echo.Context, targetURL string) error {
	return p.Proxy(c, targetURL, nil)
}

// Proxy connects to the upstream first, with the client's headers (minus handshake headers)
// plus extra, and only then upgrades the client, so an upstream refusal reaches the client as an
// HTTP status instead of a socket that closes at once. Messages are then relayed both ways
// until either side closes.
func (p *Proxy) Proxy(c echo.Context, targetURL string, extra http.Header) error {
	req := c.Request()
	target, err := url.Parse(targetURL)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadGateway, "Invalid WebSocket target")
	}
	switch target.Scheme {
	case "https", "wss":
		target.Scheme = "wss"
	default:
		target.Scheme = "ws"
	}

	header := http.Header{}
	for k, vals := range req.Header {
		if !handshakeHeaders[http.CanonicalHeaderKey(k)] {
			header[k] = append([]string(nil), vals...)
		}
	}
	for k, vals := range extra {
		header[k] = append([]string(nil), vals...)
	}

	dialer := websocket.Dialer{
		HandshakeTimeout:  p.config.HandshakeTimeout,
		ReadBufferSize:    p.config.ReadBufferSize,
		WriteBufferSize:   p.config.WriteBufferSize,
		Subprotocols:      websocket.Subprotocols(req),
		EnableCompression: p.config.EnableCompression,
		Proxy:             http.ProxyFromEnvironment,
	}
	serverConn, resp, err := dialer.DialContext(req.Context(), target.String(), header)
	if err != nil {
		p.logger.WithError(err).WithField("target", target.Redacted()).Warn("WebSocket upstream handshake failed")
		if resp != nil {
			_ = resp.Body.Close()
			if resp.StatusCode >= 400 {
				return echo.NewHTTPError(resp.StatusCode, http.StatusText(resp.StatusCode))
			}
		}
		return echo.NewHTTPError(http.StatusBadGateway, "WebSocket upstream unavailable")
	}

	upgradeHeader := http.Header{}
	if proto := serverConn.Subprotocol(); proto != "" {
		upgradeHeader.Set("Sec-WebSocket-Protocol", proto)
	}
	clientConn, err := p.upgrader.Upgrade(c.Response(), req, upgradeHeader)
	if err != nil {
		_ = serverConn.Close()
		p.logger.WithError(err).Debug("Failed to upgrade client connection")
		return nil // the upgrader already wrote the HTTP error
	}

	conn := &Connection{
		clientConn: clientConn,
		serverConn: serverConn,
		proxy:      p,
		target:     target.Redacted(),
		done:       make(chan struct{}),
	}
	clientConn.SetReadLimit(p.config.MaxMessageSize)
	serverConn.SetReadLimit(p.config.MaxMessageSize)

	go conn.pump(clientConn, serverConn, "client")
	go conn.pump(serverConn, clientConn, "server")
	go conn.keepAlive()

	<-conn.done
	return nil
}

// pump relays messages from src to dst. The read deadline is extended after every message and
// every pong (it was set once, so busy connections were dropped after ReadTimeout).
func (c *Connection) pump(src, dst *websocket.Conn, from string) {
	defer c.close()

	extend := func() error { return src.SetReadDeadline(time.Now().Add(c.proxy.config.ReadTimeout)) }
	if err := extend(); err != nil {
		return
	}
	src.SetPongHandler(func(string) error { return extend() })

	for {
		messageType, data, err := src.ReadMessage()
		if err != nil {
			var closeErr *websocket.CloseError
			if errors.As(err, &closeErr) {
				// Pass the close code on to the other side.
				msg := websocket.FormatCloseMessage(closeErr.Code, closeErr.Text)
				_ = dst.WriteControl(websocket.CloseMessage, msg, time.Now().Add(c.proxy.config.WriteTimeout))
			} else if !errors.Is(err, websocket.ErrReadLimit) {
				c.proxy.logger.WithError(err).WithField("from", from).Debug("WebSocket read ended")
			}
			return
		}
		if err := extend(); err != nil {
			return
		}
		if err := dst.SetWriteDeadline(time.Now().Add(c.proxy.config.WriteTimeout)); err != nil {
			return
		}
		if err := dst.WriteMessage(messageType, data); err != nil {
			c.proxy.logger.WithError(err).WithField("from", from).Debug("WebSocket write failed")
			return
		}
	}
}

// keepAlive pings both sides so idle connections stay open and dead peers are noticed.
// WriteControl may be called concurrently with the pumps' writes.
func (c *Connection) keepAlive() {
	ticker := time.NewTicker(c.proxy.config.PingPeriod)
	defer ticker.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-ticker.C:
			deadline := time.Now().Add(c.proxy.config.WriteTimeout)
			if c.clientConn.WriteControl(websocket.PingMessage, nil, deadline) != nil ||
				c.serverConn.WriteControl(websocket.PingMessage, nil, deadline) != nil {
				c.close()
				return
			}
		}
	}
}

func (c *Connection) close() {
	c.once.Do(func() {
		if err := c.clientConn.Close(); err != nil {
			c.proxy.logger.WithError(err).Debug("Error closing client connection")
		}
		if err := c.serverConn.Close(); err != nil {
			c.proxy.logger.WithError(err).Debug("Error closing server connection")
		}
		close(c.done)
	})
}

func (p *Proxy) HandleWebSocketUpgrade(c echo.Context) error {
	targetURL, ok := c.Get("target_url").(string)
	if !ok || targetURL == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "No target URL specified for WebSocket")
	}
	return p.ProxyWebSocket(c, targetURL)
}

func WebSocketMiddleware(proxy *Proxy) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if IsUpgradeRequest(c.Request()) {
				targetURL, ok := c.Get("target_url").(string)
				if !ok || targetURL == "" {
					return echo.NewHTTPError(http.StatusBadRequest, "No target URL specified for WebSocket")
				}
				return proxy.ProxyWebSocket(c, targetURL)
			}
			return next(c)
		}
	}
}

// IsUpgradeRequest reports whether r asks to switch to the WebSocket protocol. Connection is a
// token list ("keep-alive, Upgrade" is common), and both values are case-insensitive.
func IsUpgradeRequest(r *http.Request) bool {
	if !strings.EqualFold(strings.TrimSpace(r.Header.Get("Upgrade")), "websocket") {
		return false
	}
	for _, v := range r.Header.Values("Connection") {
		for _, token := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(token), "upgrade") {
				return true
			}
		}
	}
	return false
}

// String describes the connection for logs.
func (c *Connection) String() string {
	return fmt.Sprintf("websocket proxy to %s", c.target)
}
