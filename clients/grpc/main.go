/*
Copyright 2019 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"google.golang.org/grpc"
	"k8s.io/klog/v2"

	pb "github.com/michalskalski/demo-proxy-grpc/protos"
	"google.golang.org/grpc/credentials/insecure"
	"sigs.k8s.io/apiserver-network-proxy/pkg/util"
)

func main() {
	client := &Client{}
	o := newGrpcProxyClientOptions()
	command := newGrpcProxyClientCommand(client, o)
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

type GrpcProxyClientOptions struct {
	clientCert        string
	clientKey         string
	caCert            string
	requestHost       string
	requestPort       int
	requestClientName string
	proxyHost         string
	proxyPort         int
}

func (o *GrpcProxyClientOptions) Flags() *pflag.FlagSet {
	flags := pflag.NewFlagSet("proxy", pflag.ContinueOnError)
	flags.StringVar(&o.clientCert, "client-cert", o.clientCert, "If non-empty secure communication with this cert.")
	flags.StringVar(&o.clientKey, "client-key", o.clientKey, "If non-empty secure communication with this key.")
	flags.StringVar(&o.caCert, "ca-cert", o.caCert, "If non-empty the CAs we use to validate clients.")
	flags.StringVar(&o.requestHost, "request-host", o.requestHost, "The host of the request server.")
	flags.StringVar(&o.requestClientName, "request-client-name", o.requestClientName, "The name of grpc client")
	flags.IntVar(&o.requestPort, "request-port", o.requestPort, "The port the request server is listening on.")
	flags.StringVar(&o.proxyHost, "proxy-host", o.proxyHost, "The host of the proxy server.")
	flags.IntVar(&o.proxyPort, "proxy-port", o.proxyPort, "The port the proxy server is listening on.")
	return flags
}

func (o *GrpcProxyClientOptions) Print() {
	klog.V(1).Infof("ClientCert set to %q.\n", o.clientCert)
	klog.V(1).Infof("ClientKey set to %q.\n", o.clientKey)
	klog.V(1).Infof("CACert set to %q.\n", o.caCert)
	klog.V(1).Infof("RequestHost set to %q.\n", o.requestHost)
	klog.V(1).Infof("RequestPort set to %d.\n", o.requestPort)
	klog.V(1).Infof("RequestClientName set to %s.\n", o.requestClientName)
	klog.V(1).Infof("ProxyHost set to %q.\n", o.proxyHost)
	klog.V(1).Infof("ProxyPort set to %d.\n", o.proxyPort)
}

func (o *GrpcProxyClientOptions) Validate() error {
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

func newGrpcProxyClientOptions() *GrpcProxyClientOptions {
	o := GrpcProxyClientOptions{
		clientCert:        "",
		clientKey:         "",
		caCert:            "",
		requestHost:       "localhost",
		requestPort:       8000,
		requestClientName: "test-client",
		proxyHost:         "localhost",
		proxyPort:         8090,
	}
	return &o
}

func newGrpcProxyClientCommand(c *Client, o *GrpcProxyClientOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:  "proxy-client",
		Long: `A gRPC proxy Client, primarily used to test the Kubernetes gRPC Proxy Server.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			return c.run(o)
		},
	}

	return cmd
}

type Client struct {
}

func (c *Client) run(o *GrpcProxyClientOptions) error {
	o.Print()
	if err := o.Validate(); err != nil {
		return fmt.Errorf("failed to validate proxy client options, got %v", err)
	}

	dialer, err := c.getGrpcMTLSDialer(o)
	if err != nil {
		return fmt.Errorf("failed to get dialer for client, got %v", err)
	}
	grpcConn, err := grpc.Dial(fmt.Sprintf("%s:%d", o.requestHost, o.requestPort),
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return fmt.Errorf("failed to create grpc dialer for remote svc, got %v", err)
	}
	defer grpcConn.Close()
	cg := pb.NewGreeterClient(grpcConn)

	// Contact the server and print out its response.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	r, err := cg.SayHello(ctx, &pb.HelloRequest{Name: o.requestClientName})
	if err != nil {
		return fmt.Errorf("failed to say hello, got %v", err)
	}
	klog.V(1).Infof("Greeting: %s", r.GetMessage())

	return nil
}

func (c *Client) getGrpcMTLSDialer(o *GrpcProxyClientOptions) (func(context.Context, string) (net.Conn, error), error) {
	tlsConfig, err := util.GetClientTLSConfig(o.caCert, o.clientCert, o.clientKey, o.proxyHost, nil)
	if err != nil {
		return nil, err
	}

	var proxyConn net.Conn

	// Setup signal handler
	ch := make(chan os.Signal, 1)
	signal.Notify(ch)

	proxyAddress := fmt.Sprintf("%s:%d", o.proxyHost, o.proxyPort)
	requestAddress := fmt.Sprintf("%s:%d", o.requestHost, o.requestPort)

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

	return func(context.Context, string) (net.Conn, error) {
		return proxyConn, nil
	}, nil
}
