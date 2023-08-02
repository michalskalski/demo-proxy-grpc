package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"time"

	"golang.org/x/net/http2"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"k8s.io/klog/v2"

	"sigs.k8s.io/apiserver-network-proxy/pkg/util"
)

func main() {
	client := &Client{}
	o := newHttpProxyClientOptions()
	command := newHttpProxyClientCommand(client, o)
	flags := command.Flags()
	flags.AddFlagSet(o.Flags())
	local := flag.NewFlagSet(os.Args[0], flag.ExitOnError)
	klog.InitFlags(local)
	err := local.Set("v", "4")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error setting klog flags: %v", err)
	}
	local.VisitAll(func(fl *flag.Flag) {
		fl.Name = util.Normalize(fl.Name)
		flags.AddGoFlag(fl)
	})
	if err := command.Execute(); err != nil {
		klog.Errorf("error: %v\n", err)
		klog.Flush()
		os.Exit(1)
	}
	klog.Flush()
}

type HttpProxyClientOptions struct {
	clientCert      string
	clientKey       string
	caCert          string
	requestEndpoint string
	proxyHost       string
	proxyPort       int
}

func (o *HttpProxyClientOptions) Flags() *pflag.FlagSet {
	flags := pflag.NewFlagSet("proxy", pflag.ContinueOnError)
	flags.StringVar(&o.clientCert, "client-cert", o.clientCert, "If non-empty secure communication with this cert.")
	flags.StringVar(&o.clientKey, "client-key", o.clientKey, "If non-empty secure communication with this key.")
	flags.StringVar(&o.caCert, "ca-cert", o.caCert, "If non-empty the CAs we use to validate clients.")
	flags.StringVar(&o.proxyHost, "proxy-host", o.proxyHost, "The host of the proxy server.")
	flags.IntVar(&o.proxyPort, "proxy-port", o.proxyPort, "The port the proxy server is listening on.")
	flags.StringVar(&o.requestEndpoint, "request-endpoint", o.requestEndpoint, "The http endpoint.")

	return flags
}

func (o *HttpProxyClientOptions) Print() {
	klog.V(1).Infof("ClientCert set to %q.\n", o.clientCert)
	klog.V(1).Infof("ClientKey set to %q.\n", o.clientKey)
	klog.V(1).Infof("CACert set to %q.\n", o.caCert)
	klog.V(1).Infof("RequestEndpoint set to %q.\n", o.requestEndpoint)
	klog.V(1).Infof("ProxyHost set to %q.\n", o.proxyHost)
	klog.V(1).Infof("ProxyPort set to %d.\n", o.proxyPort)
}

func (o *HttpProxyClientOptions) Validate() error {
	if o.clientKey != "" {
		if _, err := os.Stat(o.clientKey); os.IsNotExist(err) {
			return err
		}
		if o.clientCert == "" {
			return fmt.Errorf("cannot have client cert empty when client key is set to %q", o.clientKey)
		}
	}
	if o.clientCert != "" {
		if _, err := os.Stat(o.clientCert); os.IsNotExist(err) {
			return err
		}
		if o.clientKey == "" {
			return fmt.Errorf("cannot have client key empty when client cert is set to %q", o.clientCert)
		}
	}
	if o.caCert != "" {
		if _, err := os.Stat(o.caCert); os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func newHttpProxyClientOptions() *HttpProxyClientOptions {
	o := HttpProxyClientOptions{
		clientCert:      "",
		clientKey:       "",
		caCert:          "",
		proxyHost:       "localhost",
		proxyPort:       8090,
		requestEndpoint: "http://127.0.0.1:8080",
	}
	return &o
}

func newHttpProxyClientCommand(c *Client, o *HttpProxyClientOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:  "proxy-http-client",
		Long: `A proxy client, talking to http endpoint.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			return c.run(o)
		},
	}

	return cmd
}

type Client struct {
}

func (c *Client) run(o *HttpProxyClientOptions) error {
	o.Print()
	if err := o.Validate(); err != nil {
		return fmt.Errorf("failed to validate proxy client options, got %v", err)
	}
	// The apiserver network proxy does not support reusing the same dialer for multiple connections.
	// With HTTP 1.1 Connection reuse, network proxy should support subsequent requests with the original connection
	// as long as the connection has not been closed. This means that this connection/client cannot be shared.
	// golang's default IdleConnTimeout is 90 seconds (https://golang.org/src/net/http/transport.go?s=13608:13676)
	// k/k abides by this default.
	//
	// When running the test-client with a delay parameter greater than 90s (even when no agent restart is done)
	// --test-requests=2 --test-delay=100 the second request will fail 100% of the time because currently network proxy
	// explicitly closes the tunnel when a close request is sent to to the inner TCP connection.
	// We do this as there is no way for us to know whether a tunnel will be reused.
	// So to prevent leaking too many tunnel connections,
	// we explicitly close the tunnel on the first CLOSE_RSP we obtain from the inner TCP connection
	// (https://github.com/kubernetes-sigs/apiserver-network-proxy/blob/master/konnectivity-client/pkg/client/client.go#L137).

	dialer, err := c.getMTLSDialer(o)
	if err != nil {
		return fmt.Errorf("failed to get dialer for client, got %v", err)
	}
	transport := &http.Transport{
		DialContext: dialer,
	}
	err = configureHTTP2Transport(transport)
	if err != nil {
		klog.V(1).ErrorS(err, "error initializing HTTP2 health checking parameters. Using default transport.")
	}
	client := &http.Client{
		Transport: transport,
	}
	defer client.CloseIdleConnections()

	err = c.makeRequest(o, client)
	if err != nil {
		return err
	}

	return nil
}

func readIdleTimeoutSeconds() int {
	ret := 30
	// User can set the readIdleTimeout to 0 to disable the HTTP/2
	// connection health check.
	if s := os.Getenv("HTTP2_READ_IDLE_TIMEOUT_SECONDS"); len(s) > 0 {
		i, err := strconv.Atoi(s)
		if err != nil {
			klog.Warningf("Illegal HTTP2_READ_IDLE_TIMEOUT_SECONDS(%q): %v."+
				" Default value %d is used", s, err, ret)
			return ret
		}
		ret = i
	}
	return ret
}

func pingTimeoutSeconds() int {
	ret := 15
	if s := os.Getenv("HTTP2_PING_TIMEOUT_SECONDS"); len(s) > 0 {
		i, err := strconv.Atoi(s)
		if err != nil {
			klog.Warningf("Illegal HTTP2_PING_TIMEOUT_SECONDS(%q): %v."+
				" Default value %d is used", s, err, ret)
			return ret
		}
		ret = i
	}
	return ret
}

func configureHTTP2Transport(t *http.Transport) error {
	t2, err := http2.ConfigureTransports(t)
	if err != nil {
		return err
	}
	// The following enables the HTTP/2 connection health check added in
	// https://github.com/golang/net/pull/55. The health check detects and
	// closes broken transport layer connections. Without the health check,
	// a broken connection can linger too long, e.g., a broken TCP
	// connection will be closed by the Linux kernel after 13 to 30 minutes
	// by default, which caused
	// https://github.com/kubernetes/client-go/issues/374 and
	// https://github.com/kubernetes/kubernetes/issues/87615.
	t2.ReadIdleTimeout = time.Duration(readIdleTimeoutSeconds()) * time.Second
	t2.PingTimeout = time.Duration(pingTimeoutSeconds()) * time.Second
	return nil
}

func (c *Client) makeRequest(o *HttpProxyClientOptions, client *http.Client) error {
	requestURL := o.requestEndpoint
	request, err := http.NewRequest("GET", requestURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create request %s to send, got %v", requestURL, err)
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("failed to send request to client, got %v", err)
	}
	defer func() {
		err := response.Body.Close()
		if err != nil {
			klog.Errorf("Failed to close connection, got %v", err)
		}
	}() // TODO: proxy server should handle the case where Body isn't closed.

	data, err := io.ReadAll(response.Body)
	if err != nil {
		return fmt.Errorf("failed to read response from client, got %v", err)
	}
	klog.V(4).Infof("HTML Response:\n%s\n", string(data))
	return nil
}

func (c *Client) getMTLSDialer(o *HttpProxyClientOptions) (func(ctx context.Context, network, addr string) (net.Conn, error), error) {
	tlsConfig, err := util.GetClientTLSConfig(o.caCert, o.clientCert, o.clientKey, o.proxyHost, nil)
	if err != nil {
		return nil, err
	}

	var proxyConn net.Conn

	// Setup signal handler
	ch := make(chan os.Signal, 1)
	signal.Notify(ch)

	proxyAddress := fmt.Sprintf("%s:%d", o.proxyHost, o.proxyPort)
	u, err := url.Parse(o.requestEndpoint)
	if err != nil {
		return nil, fmt.Errorf("invalid endpoint address %s: %v", o.requestEndpoint, err)
	}
	host, port, _ := net.SplitHostPort(u.Host)
	requestAddress := fmt.Sprintf("%s:%s", host, port)

	proxyConn, err = tls.Dial("tcp", proxyAddress, tlsConfig)
	if err != nil {
		return nil, fmt.Errorf("dialing proxy %q failed: %v", proxyAddress, err)
	}
	fmt.Fprintf(proxyConn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", requestAddress, "127.0.0.1")
	br := bufio.NewReader(proxyConn)
	res, err := http.ReadResponse(br, nil)
	if err != nil {
		return nil, fmt.Errorf("reading HTTP response from CONNECT to %s via proxy %s failed: %v",
			requestAddress, proxyAddress, err)
	}
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("proxy error from %s while dialing %s: %v", proxyAddress, requestAddress, res.Status)
	}

	// It's safe to discard the bufio.Reader here and return the
	// original TCP conn directly because we only use this for
	// TLS, and in TLS the client speaks first, so we know there's
	// no unbuffered data. But we can double-check.
	if br.Buffered() > 0 {
		return nil, fmt.Errorf("unexpected %d bytes of buffered data from CONNECT proxy %q",
			br.Buffered(), proxyAddress)
	}

	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		return proxyConn, nil
	}, nil
}
