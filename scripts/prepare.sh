#!/bin/bash
run=
clean=
debug=

while getopts rcd opt; do
  case $opt in
        r) run="true" ;;
        c) clean="true" ;;
	d) debug="true" ;;
  esac
done

shift "$(( OPTIND - 1 ))"

certsdir=${DEMO_CERTS_DIR:-/tmp/demo-proxy-certs}
nsrtri="rtr-priv"
nsrtre="rtr-pub"
intersub="10.10.10."
nspriv="private"
privsub="192.168.1."
nspub="public"
pubsub="2.2.2."
ips="sudo ip"
riexc="${ips} netns exec ${nsrtri}"
reexc="${ips} netns exec ${nsrtre}"

if [ "$clean" = "true" ] && [ "$run" = "true" ]; then
  echo "Only -r (run) or -c (clean) could be defined at the same time"
  exit 1
fi

if [ "$clean" = "" ] && [ "$run" = "" ]; then
  echo "Must define action: -r (run) or -c (clean)"
  exit 1
fi

if [ "$debug" = "true" ]; then
  set -x
  exec 3>&1
else 
  exec 3>&1 &>/dev/null
fi

function showDiagram {
cat <<- EOF
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
EOF
}

function prepCerts {
  local PROXY_SERVER_IP=$1
  mkdir -p $certsdir
  pushd $certsdir
  # set up easy-rsa
  mkdir -p certs/ca/frontend
  mkdir -p certs/ca/agent
  # create the client <-> server-proxy connection certs
  pushd certs/ca/frontend
  easyrsa init-pki
  easyrsa --batch "--req-cn=127.0.0.1@$(date +%s)" build-ca nopass
  easyrsa --batch --subject-alt-name="DNS:kubernetes,DNS:localhost,IP:127.0.0.1" build-server-full "proxy-frontend" nopass
  easyrsa --batch build-client-full proxy-client nopass
  echo '{"signing":{"default":{"expiry":"43800h","usages":["signing","key encipherment","client auth"]}}}' > "ca-config.json"
  echo '{"CN":"proxy","names":[{"O":"system:nodes"}],"hosts":[""],"key":{"algo":"rsa","size":2048}}' | cfssl gencert -ca=pki/ca.crt -ca-key=pki/private/ca.key -config=ca-config.json - | cfssljson -bare proxy
  popd
  mkdir -p certs/frontend
  cp -r certs/ca/frontend/pki/private certs/frontend
  cp -r certs/ca/frontend/pki/issued certs/frontend
  cp certs/ca/frontend/pki/ca.crt certs/frontend/issued
  # create the agent <-> server-proxy connection certs
  pushd certs/ca/agent
  easyrsa init-pki
  easyrsa --batch "--req-cn=${PROXY_SERVER_IP}@$(date +%s)" build-ca nopass
  easyrsa --batch --subject-alt-name="DNS:kubernetes,DNS:localhost,IP:${PROXY_SERVER_IP}" build-server-full "proxy-frontend" nopass
  easyrsa --batch build-client-full proxy-agent nopass
  echo '{"signing":{"default":{"expiry":"43800h","usages":["signing","key encipherment","agent auth"]}}}' > "ca-config.json"
  echo '{"CN":"proxy","names":[{"O":"system:nodes"}],"hosts":[""],"key":{"algo":"rsa","size":2048}}' | cfssl gencert -ca=pki/ca.crt -ca-key=pki/private/ca.key -config=ca-config.json - | cfssljson -bare proxy
  popd
  mkdir -p certs/agent
  cp -r certs/ca/agent/pki/private certs/agent
  cp -r certs/ca/agent/pki/issued certs/agent
  cp certs/ca/agent/pki/ca.crt certs/agent/issued
  popd
}

function prepNS {
  $ips netns add ${1}
  local nexc="${ips} netns exec ${1}"
  $nexc ip link add ${1}1 type veth peer name rtr-${1}
  $nexc ip addr add "${2}2/27" dev ${1}1
  $nexc ip link set dev lo up
  $nexc ip link set dev ${1}1 up
  $nexc ip route add default via ${2}1

  $nexc ip link set rtr-${1} netns ${3}
  local rexc="${ips} netns exec ${3}"
  $rexc ip addr add "${2}1/27" dev rtr-${1}
  $rexc ip link set dev rtr-${1} up
}

function routerNS {
  # "router" namespaces
  $ips netns add $nsrtri
  $ips netns add $nsrtre
  $riexc ip link add ${nspriv}-ext type veth peer name ${nspub}-ext
  $riexc ip link set ${nspub}-ext netns $nsrtre
}

function nsNameServer {
  # dnsmasq for private
  sudo mkdir -p /etc/netns/private
  echo "nameserver ${privsub}2" | sudo tee /etc/netns/private/resolv.conf
}

function iptableRules {
  $riexc ip addr add "${intersub}2/27" dev ${nspriv}-ext
  $riexc ip link set lo up
  $riexc ip link set ${nspriv}-ext up
  $riexc iptables -P INPUT DROP
  $riexc iptables -P FORWARD DROP
  $riexc iptables -A FORWARD -i ${nspriv}-ext -o rtr-${nspriv} -m state --state ESTABLISHED,RELATED -j ACCEPT
  $riexc iptables -A FORWARD -i rtr-${nspriv} -o ${nspriv}-ext -j ACCEPT
  $riexc iptables -A FORWARD -j REJECT
  $riexc iptables -t nat -A POSTROUTING -s ${privsub}0/27 -o ${nspriv}-ext -j MASQUERADE
  $riexc ip route add default via ${intersub}1
  $riexc sysctl -w net.ipv4.ip_forward=1
  
  $reexc ip addr add "${intersub}1/27" dev ${nspub}-ext
  $reexc ip link set lo up
  $reexc ip link set ${nspub}-ext up
  $reexc iptables -P INPUT DROP
  $reexc iptables -P FORWARD DROP
  $reexc iptables -A FORWARD -i ${nspub}-ext -o rtr-${nspub} -d ${pubsub}2/32 -j ACCEPT
  $reexc iptables -A FORWARD -i rtr-${nspub} -o ${nspub}-ext -j ACCEPT
  $reexc ip route add default via ${intersub}2
  $reexc sysctl -w net.ipv4.ip_forward=1
}

function showCertsVars {
  echo "PROXY_SERVER_CERTS='$PROXY_SERVER_CERTS'"
  echo "PROXY_AGENT_CERTS='$PROXY_AGENT_CERTS'"
  echo "PROXY_CLIENT_CERTS='$PROXY_CLIENT_CERTS'"
}

if [ "$run" = "true" ]; then
  routerNS
  nsNameServer
  prepNS $nspriv $privsub $nsrtri
  prepNS $nspub $pubsub $nsrtre
  iptableRules
  prepCerts ${pubsub}2
  showCertsVars >&3
  showDiagram >&3
fi


if [ "$clean" = "true" ]; then
  $ips netns del $nspriv
  $ips netns del $nspub
  $ips netns del $nsrtri
  $ips netns del $nsrtre
  rm -rf $certsdir
  sudo rm -rf /etc/netns/private
fi

