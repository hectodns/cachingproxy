package cachingproxy

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/coredns/coredns/core/dnsserver"
	"github.com/coredns/coredns/plugin"
	"github.com/coredns/coredns/plugin/metrics"
	"github.com/coredns/coredns/plugin/pkg/parse"
	pkgtls "github.com/coredns/coredns/plugin/pkg/tls"
	"github.com/coredns/coredns/plugin/pkg/transport"

	"github.com/caddyserver/caddy"
)

func setup(c *caddy.Controller) error {
	f, err := parseForward(c)
	if err != nil {
		return plugin.Error("forward", err)
	}
	if f.Len() > max {
		return plugin.Error("forward", fmt.Errorf("more than %d TOs configured: %d", max, f.Len()))
	}

	dnsserver.GetConfig(c).AddPlugin(func(next plugin.Handler) plugin.Handler {
		f.Next = next
		return f
	})

	c.OnStartup(func() error {
		metrics.MustRegister(c, RequestCount, RcodeCount, RequestDuration, HealthcheckFailureCount, SocketGauge, MaxConcurrentRejectCount)
		return f.OnStartup()
	})

	c.OnShutdown(func() error {
		return f.OnShutdown()
	})

	return nil
}

// OnStartup starts a goroutines for all proxies.
func (f *Forward) OnStartup() (err error) {
	for _, p := range f.proxies {
		p.start(f.ProbeTimeout)
	}
	return nil
}

// OnShutdown stops all configured proxies.
func (f *Forward) OnShutdown() error {
	for _, p := range f.proxies {
		p.stop()
	}
	return nil
}

func parseForward(c *caddy.Controller) (*Forward, error) {
	var (
		f   *Forward
		err error
		i   int
	)
	for c.Next() {
		if i > 0 {
			return nil, plugin.ErrOnce
		}
		i++
		f, err = parseStanza(c)
		if err != nil {
			return nil, err
		}
	}
	return f, nil
}

func parseStanza(c *caddy.Controller) (*Forward, error) {
	f := New()

	if !c.Args(&f.from) {
		return f, c.ArgErr()
	}
	f.from = plugin.Host(f.from).Normalize()

	to := c.RemainingArgs()
	if len(to) == 0 {
		return f, c.ArgErr()
	}

	toHosts, err := parse.HostPortOrFile(to...)
	if err != nil {
		return f, err
	}

	transports := make([]string, len(toHosts))
	allowedTrans := map[string]bool{"dns": true, "tls": true}
	for i, host := range toHosts {
		trans, h := parse.Transport(host)

		if !allowedTrans[trans] {
			return f, fmt.Errorf("'%s' is not supported as a destination protocol in forward: %s", trans, host)
		}
		p := NewProxy(h, trans)
		f.proxies = append(f.proxies, p)
		transports[i] = trans
	}

	for c.NextBlock() {
		if err := parseBlock(c, f); err != nil {
			return f, err
		}
	}

	if f.TLSServerName != "" {
		f.TLSConfig.ServerName = f.TLSServerName
	}
	for i := range f.proxies {
		// Only set this for proxies that need it.
		if transports[i] == transport.TLS {
			f.proxies[i].SetTLSConfig(f.TLSConfig)
		}
		f.proxies[i].SetExpire(f.ExpireTimeout)
		f.proxies[i].SetRecursionDesired(f.opts.hcRecursionDesired)
	}

	return f, nil
}

func parseBlock(c *caddy.Controller, f *Forward) error {
	switch c.Val() {
	case "except":
		ignore := c.RemainingArgs()
		if len(ignore) == 0 {
			return c.ArgErr()
		}
		for i := 0; i < len(ignore); i++ {
			ignore[i] = plugin.Host(ignore[i]).Normalize()
		}
		f.Ignored = ignore
	case "max_fails":
		if !c.NextArg() {
			return c.ArgErr()
		}
		n, err := strconv.Atoi(c.Val())
		if err != nil {
			return err
		}
		if n < 0 {
			return fmt.Errorf("max_fails can't be negative: %d", n)
		}
		f.MaxFails = uint32(n)
	case "health_check":
		if !c.NextArg() {
			return c.ArgErr()
		}
		dur, err := time.ParseDuration(c.Val())
		if err != nil {
			return err
		}
		if dur < 0 {
			return fmt.Errorf("health_check can't be negative: %d", dur)
		}
		f.ProbeTimeout = dur

		for c.NextArg() {
			switch hcOpts := c.Val(); hcOpts {
			case "no_rec":
				f.opts.hcRecursionDesired = false
			default:
				return fmt.Errorf("health_check: unknown option %s", hcOpts)
			}
		}

	case "force_tcp":
		if c.NextArg() {
			return c.ArgErr()
		}
		f.opts.forceTCP = true
	case "prefer_udp":
		if c.NextArg() {
			return c.ArgErr()
		}
		f.opts.preferUDP = true
	case "tls":
		args := c.RemainingArgs()
		if len(args) > 3 {
			return c.ArgErr()
		}

		tlsConfig, err := pkgtls.NewTLSConfigFromArgs(args...)
		if err != nil {
			return err
		}
		f.TLSConfig = tlsConfig
	case "tls_servername":
		if !c.NextArg() {
			return c.ArgErr()
		}
		f.TLSServerName = c.Val()
	case "expire":
		if !c.NextArg() {
			return c.ArgErr()
		}
		dur, err := time.ParseDuration(c.Val())
		if err != nil {
			return err
		}
		if dur < 0 {
			return fmt.Errorf("expire can't be negative: %s", dur)
		}
		f.ExpireTimeout = dur
	case "policy":
		if !c.NextArg() {
			return c.ArgErr()
		}
		switch x := c.Val(); x {
		case "random":
			f.Policy = new(RandomPolicy)
		case "round_robin":
			f.Policy = new(RoundRobinPolicy)
		case "sequential":
			f.Policy = new(SequentialPolicy)
		default:
			return c.Errf("unknown policy '%s'", x)
		}
	case "max_concurrent":
		if !c.NextArg() {
			return c.ArgErr()
		}
		n, err := strconv.Atoi(c.Val())
		if err != nil {
			return err
		}
		if n < 0 {
			return fmt.Errorf("max_concurrent can't be negative: %d", n)
		}
		f.ErrLimitExceeded = errors.New("concurrent queries exceeded maximum " + c.Val())
		f.MaxConcurrent = int64(n)

	default:
		return c.Errf("unknown property '%s'", c.Val())
	}

	return nil
}

const max = 15 // Maximum number of upstreams.
