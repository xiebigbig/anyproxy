package anyproxy

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"sort"

	"github.com/wzshiming/cmux"
	"golang.org/x/sync/errgroup"
)

type Dialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}

type ListenConfig interface {
	Listen(ctx context.Context, network, address string) (net.Listener, error)
}

type AnyProxy struct {
	proxies map[string]*Host
	conf    *Config
}

type Logger interface {
	Println(v ...interface{})
}

func NewAnyProxy(ctx context.Context, addrs []string, conf *Config) (*AnyProxy, error) {
	if conf == nil {
		conf = &Config{}
	} else {
		c := *conf
		conf = &c
	}
	if conf.ListenConfig == nil {
		conf.ListenConfig = &net.ListenConfig{}
	}

	proxies := map[string]*Host{}
	users := map[string][]*url.Userinfo{}
	for _, addr := range addrs {
		u, err := url.Parse(addr)
		if err != nil {
			return nil, err
		}

		unique := fmt.Sprintf("%s://%s", u.Scheme, u.Host)
		if u.User == nil {
			users[unique] = nil
		} else {
			if user, ok := users[unique]; !ok || user != nil {
				users[unique] = append(users[unique], u.User)
			}
		}
		hostconf := *conf
		hostconf.RawQueries = append(hostconf.RawQueries, u.RawQuery)
		hostconf.Users = append(users[unique], hostconf.Users...)

		s, patterns, err := NewServeConn(ctx, u.Scheme, u.Host, &hostconf)
		if err != nil {
			return nil, err
		}
		mux, ok := proxies[u.Host]
		if !ok {
			mux = &Host{
				cmux: cmux.NewCMux(),
				conf: conf,
			}
		}

		if p, ok := s.(proxyURLs); ok {
			mux.proxies = append(mux.proxies, p.ProxyURLs()...)
		} else if p, ok := s.(proxyURL); ok {
			mux.proxies = append(mux.proxies, p.ProxyURL())
		}
		if patterns == nil {
			err = mux.cmux.NotFound(s)
			if err != nil {
				return nil, err
			}
		} else {
			err = mux.cmux.HandlePrefix(s, patterns...)
		}
		proxies[u.Host] = mux
	}
	proxy := &AnyProxy{
		conf:    conf,
		proxies: proxies,
	}
	return proxy, nil
}

func (a *AnyProxy) Match(addr string) *Host {
	return a.proxies[addr]
}

func (a *AnyProxy) Hosts() []string {
	hosts := make([]string, 0, len(a.proxies))
	for proxy := range a.proxies {
		hosts = append(hosts, proxy)
	}
	sort.Strings(hosts)
	return hosts
}

func (a *AnyProxy) ListenAndServe(network, address string) error {
	host := a.Match(address)
	if host == nil {
		return fmt.Errorf("not match address %q", address)
	}
	return host.ListenAndServe(network, address)
}

func (a *AnyProxy) Run(ctx context.Context) error {
	group, ctx := errgroup.WithContext(ctx)
	for _, address := range a.Hosts() {
		address := address
		host := a.Match(address)
		if host == nil {
			return fmt.Errorf("not match address %q", address)
		}
		listener, err := a.conf.ListenConfig.Listen(ctx, "tcp", address)
		if err != nil {
			return err
		}
		group.Go(func() error {
			for {
				conn, err := listener.Accept()
				if err != nil {
					return err
				}
				go host.ServeConn(conn)
			}
		})
	}
	return group.Wait()
}

type Host struct {
	cmux    *cmux.CMux
	proxies []string
	conf    *Config
}

func (h *Host) ProxyURLs() []string {
	return h.proxies
}

func (h *Host) ServeConn(conn net.Conn) {
	h.cmux.ServeConn(conn)
}

func (h *Host) ListenAndServe(network, address string) error {
	listener, err := h.conf.ListenConfig.Listen(context.Background(), network, address)
	if err != nil {
		return err
	}
	return h.Serve(listener)
}

func (h *Host) Serve(listener net.Listener) error {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return err
		}
		go h.ServeConn(conn)
	}
}
