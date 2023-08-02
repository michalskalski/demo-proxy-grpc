# demo-proxy-grpc


This repository include development shell (using [nix develop](https://nixos.org/manual/nix/stable/command-ref/new-cli/nix3-develop.html)) in which it is easy to test how [apiserver-network-proxy](https://github.com/kubernetes-sigs/apiserver-network-proxy) components works and how to reuse them in own code.

[![asciicast](https://asciinema.org/a/600285.svg)](https://asciinema.org/a/600285?speed=1.6)
To run development shell locally first [nix](https://nixos.org/download.html) have to be installed locally. Even the included [flake.nix](flake.nix) provides reproducible builds, to properly simulate environments it depends on presence of [network namespaces](https://man7.org/linux/man-pages/man7/network_namespaces.7.html) and iptables which are available on linux environments.

[clients/](clients) dir contains example go implementation of grpc/tcp clients which dials into proxy server and pass [net.Conn](https://pkg.go.dev/net#Conn) interface to private ns endpoint connection constructor. This [article](https://tilde.town/~hut8/post/grpc-connections/) in my opinion is a good description of tunnel approach. Clients will be available in development shell as `proxy-grpc` and `proxy-tcp`.

[services/](services) dir contains example rust implementation of grpc and tcp servers. They will be available in development shell as `endpoint-grpc` and `endpoint-tcp`.

## Run demo

```
┌─────────────────────────────┐                                                           ┌─────────────────────────────┐
│                             │                                                           │                             │
│           ┌────────────┐    │                                                           │ ┌───────────┐               │
│           │proxy-server├────┼────────────────────tunnel (grpc / http)───────────────────┼─┤proxy-agent├──────┐        │
│           └───▲───▲────┘    │                                                           │ └─────────┬─┘      │        │
│               │   │         │    ┌──────────────────────┐   ┌──────────────────────┐    │           │        │        │
│               │   │         │    │                      │   │                      │    │           │ ┌──────▼──────┐ │
│ ┌───────────┐ │   │         │    │            10.10.10.1│   │10.10.10.2            │    │           │ │grpc-endpoint│ │
│ │grpc-client├─┘   │   x─────┼────┼────x            x────┼───┼─────x           x────┼────┼────x      │ └─────────────┘ │
│ └───────────┘     │  2.2.2.2│    │2.2.2.1               │   │           192.168.1.1│    │192.168.1.2│                 │
│                   │         │    │                      │   │                      │    │           │ ┌─────────────┐ │
│ ┌───────────┐     │         │    │    netns rtr-pub     │   │    netns rtr-priv    │    │           └─►http-endpoint│ │
│ │http-client├─────┘         │    └──────────────────────┘   └──────────────────────┘    │             └─────────────┘ │
│ └───────────┘               │                                                           │                             │
│                             │                                                           │                             │
│         netns public        │                                                           │         netns private       │
└─────────────────────────────┘                                                           └─────────────────────────────┘
```

Clone repo and enter directory
```shell
$ git clone https://github.com/michalskalski/demo-proxy-grpc
$ cd demo-proxy-grpc 
```
Enter dev shell (install [nix](https://nixos.org/download.html) if you haven't done that yet)
```shell
$ nix develop
```
or if you using [direnv](https://direnv.net/) and [direnv-nix](https://github.com/nix-community/nix-direnv)
```shell
$ direnv allow
```
Create setup consisting of public and private network namespaces (and two extra ns simulating internet connections)
```shell
prepare.sh -r
```
it will create four network namespaces
```
$ ip netns ls
public
private
rtr-pub
rtr-priv
```
verify that you can connect from private namespace to public
```shell
$ in_ns.sh private ping -c 1 2.2.2.2
```
but not from public to private
```shell
in_ns.sh public ping -c 1 192.168.1.2
```
start proxy server in public ns
```shell
in_ns.sh public proxy-server $PROXY_SERVER_CERTS --mode http-connect
```
run proxy agent in private ns, it will connect to proxy server and create a tunnel which will allow communication from public to private
```shell
in_ns.sh private proxy-agent $PROXY_AGENT_CERTS --agent-id demo --proxy-server-host 2.2.2.2
```
run grpc endpoint in private ns
```shell
in_ns.sh private endpoint-grpc -a 192.168.1.2 -p 4001
```
and try to reach it from public using proxy server
```shell
in_ns.sh public proxy-grpc $PROXY_CLIENT_CERTS --request-host 192.168.1.2 --request-port 4001 --request-client-name demo
```
run tcp endpoint in private ns
```shell
in_ns.sh private endpoint-tcp -a 192.168.1.2 -p 8080
```
and try to reach it from public using proxy server
```shell
in_ns.sh public proxy-http $PROXY_CLIENT_CERTS --request-endpoint http://192.168.1.2:8080/ok
```
run dnsmasq in private ns to test requesting endpoint by it local dns name
```shell
in_ns.sh private dnsmasq -d -q -a 192.168.1.2 --host-record=local-server.svc,192.168.1.2
```
verify it resolves in private ns
```shell
in_ns.sh private nslookup local-server.svc
```
but not in public ns
```shell
in_ns.sh public nslookup local-server.svc
```
because name resolution happen on agent end it still should be possible to request endpoint by dns name from public ns
```shell
in_ns.sh public proxy-http $PROXY_CLIENT_CERTS --request-endpoint http://local-server.svc:8080/ok
```

once you finished you can run cleanup
```shell
prepare.sh -c
```
