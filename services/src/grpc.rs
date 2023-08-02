use tonic::{transport::Server, Request, Response, Status};

use demo::greeter_server::{Greeter, GreeterServer};
use demo::{HelloReply, HelloRequest};

use log::info;
use clap::Parser;

pub mod demo {
    tonic::include_proto!("hello");
}


/// GRPC hello server
#[derive(Parser, Debug)]
#[command(author, version, about, long_about = None)]
struct Args {
    /// IP address to listen on
    #[arg(short, long, default_value_t= String::from("127.0.0.1"))]
    address: String,
    /// TCP port to listen on
    #[arg(short, long, default_value_t = 50051)]
    port: u16,
}

#[derive(Debug, Default)]
pub struct MyGreeter {}

#[tonic::async_trait]
impl Greeter for MyGreeter {
    async fn say_hello(
        &self,
        request: Request<HelloRequest>,
    ) -> Result<Response<HelloReply>, Status> {
        info!("Got a request: {:?}", request);

        let reply = demo::HelloReply {
            message: format!("Hello {}!", request.into_inner().name).into(),
        };

        Ok(Response::new(reply))
    }
}

#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    env_logger::init();
    let args = Args::parse();
    let addr = format!("{}:{}", args.address, args.port);
    let s_addr = addr.parse()?;
    let greeter = MyGreeter::default();
    info!("Start grpc server on: {}", &addr);

    Server::builder()
        .add_service(GreeterServer::new(greeter))
        .serve(s_addr)
        .await?;

    Ok(())
}
