package nats

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	gonats "github.com/nats-io/nats.go"
)

type ClientOptions struct {
	URL            string
	Name           string
	ConnectTimeout time.Duration
	ReconnectWait  time.Duration
	MaxReconnects  int
	Connection     *gonats.Conn
}

type Client struct {
	conn   *gonats.Conn
	owned  bool
	closed bool
}

type natsSubscription struct {
	sub *gonats.Subscription
}

var _ Fabric = (*Client)(nil)
var _ Subscription = (*natsSubscription)(nil)

func NewClient(opts ClientOptions) (*Client, error) {
	if opts.Connection != nil {
		slog.Debug("nats_client: using caller-provided nats connection", "owned", false)
		return &Client{conn: opts.Connection, owned: false}, nil
	}

	url := strings.TrimSpace(opts.URL)
	if url == "" {
		url = gonats.DefaultURL
	}

	natsOpts := []gonats.Option{}
	if name := strings.TrimSpace(opts.Name); name != "" {
		natsOpts = append(natsOpts, gonats.Name(name))
	}
	if opts.ConnectTimeout > 0 {
		natsOpts = append(natsOpts, gonats.Timeout(opts.ConnectTimeout))
	}
	if opts.ReconnectWait > 0 {
		natsOpts = append(natsOpts, gonats.ReconnectWait(opts.ReconnectWait))
	}
	if opts.MaxReconnects > 0 {
		natsOpts = append(natsOpts, gonats.MaxReconnects(opts.MaxReconnects))
	}
	slog.Info(
		"nats_client: connecting",
		"url", url,
		"name", strings.TrimSpace(opts.Name),
		"connect_timeout", opts.ConnectTimeout.String(),
		"reconnect_wait", opts.ReconnectWait.String(),
		"max_reconnects", opts.MaxReconnects,
	)

	conn, err := gonats.Connect(url, natsOpts...)
	if err != nil {
		slog.Error("nats_client: connect failed", "url", url, "error", err)
		return nil, fmt.Errorf("connect nats: %w", err)
	}
	slog.Info("nats_client: connected", "url", url, "client_id", conn.ConnectedServerId())
	return &Client{conn: conn, owned: true}, nil
}

func (c *Client) Publish(ctx context.Context, message Message) error {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if c == nil || c.conn == nil {
		return fmt.Errorf("nats connection is not initialized")
	}
	if strings.TrimSpace(message.Subject) == "" {
		return fmt.Errorf("subject is required")
	}
	slog.Debug("nats_client: publishing message", "subject", message.Subject, "reply", message.Reply, "bytes", len(message.Data))

	msg := &gonats.Msg{
		Subject: message.Subject,
		Reply:   message.Reply,
		Data:    append([]byte(nil), message.Data...),
	}
	if err := c.conn.PublishMsg(msg); err != nil {
		return fmt.Errorf("publish nats message: %w", err)
	}
	return nil
}

func (c *Client) Subscribe(subject string, handler Handler) (Subscription, error) {
	if c == nil || c.conn == nil {
		return nil, fmt.Errorf("nats connection is not initialized")
	}
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return nil, fmt.Errorf("subject is required")
	}
	if handler == nil {
		return nil, fmt.Errorf("handler is required")
	}
	slog.Debug("nats_client: subscribing", "subject", subject)

	sub, err := c.conn.Subscribe(subject, func(msg *gonats.Msg) {
		handler(context.Background(), Message{Subject: msg.Subject, Reply: msg.Reply, Data: append([]byte(nil), msg.Data...)})
	})
	if err != nil {
		return nil, fmt.Errorf("subscribe %q: %w", subject, err)
	}
	return &natsSubscription{sub: sub}, nil
}

func (c *Client) Request(ctx context.Context, subject string, data []byte) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	if c == nil || c.conn == nil {
		return nil, fmt.Errorf("nats connection is not initialized")
	}
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return nil, fmt.Errorf("subject is required")
	}
	slog.Debug("nats_client: request", "subject", subject, "bytes", len(data))

	msg := &gonats.Msg{Subject: subject, Data: append([]byte(nil), data...)}
	response, err := c.conn.RequestMsgWithContext(ctx, msg)
	if err != nil {
		return nil, fmt.Errorf("request nats message: %w", err)
	}
	return append([]byte(nil), response.Data...), nil
}

func (c *Client) Close() error {
	if c == nil || c.conn == nil || c.closed {
		return nil
	}
	c.closed = true
	if c.owned {
		slog.Debug("nats_client: closing owned connection")
		c.conn.Close()
	}
	return nil
}

func (s *natsSubscription) Unsubscribe() {
	if s == nil || s.sub == nil {
		return
	}
	_ = s.sub.Unsubscribe()
}
