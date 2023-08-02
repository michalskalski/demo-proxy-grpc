{
   inputs = {
       nixpkgs.url = "github:nixos/nixpkgs/nixos-unstable";
       flake-utils.url = "github:numtide/flake-utils";
       rust-overlay.url = "github:oxalica/rust-overlay";
       crane = {
         url = "github:ipetkov/crane";
         inputs.nixpkgs.follows = "nixpkgs";
       };

     };
     outputs = { self, nixpkgs, flake-utils, rust-overlay, crane, ... }:
         flake-utils.lib.eachDefaultSystem (system:
             let
                overlays = [ (import rust-overlay) ];
                pkgs = import nixpkgs { inherit system overlays; };
                rustVersion = pkgs.rust-bin.stable.latest.default;
                rustToolchain = pkgs.rust-bin.stable.latest.default.override {
                    extensions = [ "rust-src" ];
                };


                prepare-script = pkgs.writeShellApplication {
                      name = "prepare.sh";
                      text = "${builtins.readFile ./scripts/prepare.sh}";
                      checkPhase = "";
                };

                in-ns-script = pkgs.writeShellApplication {
                      name = "in_ns.sh";
                      text = "${builtins.readFile ./scripts/in_ns.sh}";
                      checkPhase = "";
                };

                proxy-clients = pkgs.buildGoModule {
                     pname = "proxy-clients";
                     version = "latest";
                     src = ./.;
                     vendorSha256 = "sha256-+8L5DJgCrJDtLNeN1zijkcXi17bvdDVtJgy+CVUqeaQ=";
                     postBuild = ''
                        mv $GOPATH/bin/http $GOPATH/bin/proxy-http
                        mv $GOPATH/bin/grpc $GOPATH/bin/proxy-grpc
                     '';

                };
                apiserver-network-proxy = pkgs.buildGoModule {
                  pname = "apiserver-network-proxy";
                  name = "apiserver-network-proxy";
                  src = pkgs.fetchFromGitHub {
                    owner = "kubernetes-sigs";
                    repo = "apiserver-network-proxy";
                    rev = "v0.1.3";
                    sha256 = "sha256-DYyqGjpRrnRE/vxLqT0M9ht+K58Rcc7MGlYIvjn2E2M=";
                  };
                  doCheck = false;
                  vendorSha256 = "sha256-3xPl6fibtmWzuf2awoLwGYzkHEPE18tCXdtOFnzxT8c=";
                  subPackages = [ "./cmd/agent" "./cmd/server" ];
                  postBuild = ''
                     mv $GOPATH/bin/agent $GOPATH/bin/proxy-agent
                     mv $GOPATH/bin/server $GOPATH/bin/proxy-server
                  '';
                };
                protoc-gen-go-grpc = pkgs.buildGoModule {
                  name = "protoc-gen-go-grpc";
                  src = pkgs.fetchFromGitHub {
                    owner = "grpc";
                    repo = "grpc-go";
                    rev = "v1.36.0";
                    sha256 = "sha256-sUDeWY/yMyijbKsXDBwBXLShXTAZ4445I4hpP7bTndQ=";
                  };
                  doCheck = false;
                  vendorSha256 = "sha256-KHd9zmNsmXmc2+NNtTnw/CSkmGwcBVYNrpEUmIoZi5Q=";
                  modRoot = "./cmd/protoc-gen-go-grpc";
                };

                protoc-gen-go = pkgs.buildGoModule {
                  name = "protoc-gen-go";
                  src = pkgs.fetchFromGitHub {
                    owner = "protocolbuffers";
                    repo = "protobuf-go";
                    rev = "v1.27.1";
                    sha256 = "sha256-wkUvMsoJP38KMD5b3Fz65R1cnpeTtDcVqgE7tNlZXys=";
                  };
                  doCheck = false; vendorSha256 = null;
                  modRoot = "./cmd/protoc-gen-go";
                };

                # react and keep proto files
                protoFilter = path: _type: builtins.match ".*proto$" path != null;
                rustFilter = path: type:
                  (protoFilter path type) || (craneLib.filterCargoSources path type);

                craneLib = crane.lib.${system};
                proxy-services = craneLib.buildPackage {
                  src = pkgs.lib.cleanSourceWith {
                     src = craneLib.path ./.; # The original, unfiltered source
                     filter = rustFilter;
                  };
                  cargoLock = ./services/Cargo.lock;
                  cargoToml = ./services/Cargo.toml;
                  # Use a postUnpack hook to jump into our nested directory. This will work
                  # regardless of what the unpacked source is named (i.e. will avoid hashes
                  # when using the root path of a flake).
                  #
                  # The unpackPhase sets `$sourceRoot` to the directory that was unpacked
                  # but unfortunately `postUnpack` runs before the directory is actually
                  # changed so we'll do two things:
                  # 1. Jump into the directory we want (replace `nested` with your directory)
                  # 2. Overwrite the variable so when the default build scripts run they don't
                  # end up changing to a different directory again
                  postUnpack = ''
                    cd $sourceRoot/services
                    sourceRoot="."
                  '';
                  buildInputs = [
                    pkgs.protobuf
                  ];
                };

              in {
                  packages.default = proxy-services;
                  devShell = pkgs.mkShell {
                      buildInputs =
                          [
                            pkgs.dnsmasq
                            pkgs.pkg-config
                            pkgs.openssl
                            pkgs.protobuf
                            pkgs.gnumake
                            pkgs.nix
                            pkgs.go_1_20
                            pkgs.gopls
                            pkgs.gotools
                            pkgs.go-tools
                            pkgs.easyrsa
                            pkgs.cfssl
                            pkgs.curl
                            pkgs.grpcurl
                            pkgs.rust-analyzer-unwrapped
                            in-ns-script
                            prepare-script
                            rustToolchain
                            proxy-clients
                            apiserver-network-proxy
                            protoc-gen-go
                            protoc-gen-go-grpc
                            proxy-services
                          ];
                      RUST_SRC_PATH = "${rustToolchain}/lib/rustlib/src/rust/library";
                      RUST_LOG = "info";
                      DEMO_CERTS_DIR = "/tmp/demo-proxy-certs";
                      PROXY_SERVER_CERTS = "--server-ca-cert=$DEMO_CERTS_DIR/certs/frontend/issued/ca.crt --server-cert=$DEMO_CERTS_DIR/certs/frontend/issued/proxy-frontend.crt --server-key=$DEMO_CERTS_DIR/certs/frontend/private/proxy-frontend.key --cluster-ca-cert=$DEMO_CERTS_DIR/certs/agent/issued/ca.crt --cluster-cert=$DEMO_CERTS_DIR/certs/agent/issued/proxy-frontend.crt --cluster-key=$DEMO_CERTS_DIR/certs/agent/private/proxy-frontend.key";
                      PROXY_AGENT_CERTS = "--ca-cert=$DEMO_CERTS_DIR/certs/agent/issued/ca.crt --agent-cert=$DEMO_CERTS_DIR/certs/agent/issued/proxy-agent.crt --agent-key=$DEMO_CERTS_DIR/certs/agent/private/proxy-agent.key";
                      PROXY_CLIENT_CERTS = "--ca-cert=$DEMO_CERTS_DIR/certs/frontend/issued/ca.crt --client-cert=$DEMO_CERTS_DIR/certs/frontend/issued/proxy-client.crt --client-key=$DEMO_CERTS_DIR/certs/frontend/private/proxy-client.key";
                      nix_shell_ps1="[grpc-proxy-demo]";
                      SUDO_PRESERVE = "--preserve-env=PATH --preserve-env=RUST_LOG --preserve-env=RUST_SRC_PATH --preserve-env=DEMO_CERTS_DIR --preserve-env=PROXY_SERVER_CERTS --preserve-env=PROXY_AGENT_CERTS --preserve-env=PROXY_CLIENT_CERTS";
                      shellHook = ''
                        # aliases not recognized by direnv
                        #alias sudo_env='sudo --preserve-env=PATH --preserve-env=RUST_LOG --preserve-env=RUST_SRC_PATH --preserve-env=DEMO_CERTS_DIR --preserve-env=PROXY_SERVER_CERTS --preserve-env=PROXY_AGENT_CERTS --preserve-env=PROXY_CLIENT_CERTS env "$@"'
                        #alias in_public_ns='sudo_env ip netns exec public "$@"'
                        #alias in_private_ns='sudo_env ip netns exec private "$@"'
                        export PROXY_SERVER_CERTS="--server-ca-cert=$DEMO_CERTS_DIR/certs/frontend/issued/ca.crt --server-cert=$DEMO_CERTS_DIR/certs/frontend/issued/proxy-frontend.crt --server-key=$DEMO_CERTS_DIR/certs/frontend/private/proxy-frontend.key --cluster-ca-cert=$DEMO_CERTS_DIR/certs/agent/issued/ca.crt --cluster-cert=$DEMO_CERTS_DIR/certs/agent/issued/proxy-frontend.crt --cluster-key=$DEMO_CERTS_DIR/certs/agent/private/proxy-frontend.key"
                        export PROXY_AGENT_CERTS="--ca-cert=$DEMO_CERTS_DIR/certs/agent/issued/ca.crt --agent-cert=$DEMO_CERTS_DIR/certs/agent/issued/proxy-agent.crt --agent-key=$DEMO_CERTS_DIR/certs/agent/private/proxy-agent.key"
                        export PROXY_CLIENT_CERTS="--ca-cert=$DEMO_CERTS_DIR/certs/frontend/issued/ca.crt --client-cert=$DEMO_CERTS_DIR/certs/frontend/issued/proxy-client.crt --client-key=$DEMO_CERTS_DIR/certs/frontend/private/proxy-client.key"
                      '';
                    };
              });
}
