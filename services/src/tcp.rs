use std::{
    io::{prelude::*, BufReader},
    net::{TcpListener, TcpStream},
};

use log::info;
use clap::Parser;


/// TCP hello server
#[derive(Parser, Debug)]
#[command(author, version, about, long_about = None)]
struct Args {
    /// IP address to listen on
    #[arg(short, long, default_value_t= String::from("127.0.0.1"))]
    address: String,
    /// TCP port to listen on
    #[arg(short, long, default_value_t = 8080)]
    port: u16,
}
fn main() {
    env_logger::init();
    let args = Args::parse();
    let addr = format!("{}:{}", args.address, args.port);
    let listener = TcpListener::bind(&addr).unwrap();

    info!("start http server on {}", &addr);
    info!("available path: http://{}/ok", &addr);

    for stream in listener.incoming() {
        let stream = stream.unwrap();

        handle_connection(stream)
    }
}

fn handle_connection(mut stream: TcpStream) {
    info!("Connection from {}", stream.peer_addr().unwrap());
    let buf_reader = BufReader::new(&mut stream);
    let http_request: Vec<_> = buf_reader
        .lines()
        .map(|result| result.unwrap())
        .take_while(|line| !line.is_empty())
        .collect();

    info!("Request: {:#?}", http_request);

    let request_line = http_request.first().unwrap();
    
    let (status_line, resp) = if request_line == "GET /ok HTTP/1.1" {
        ("HTTP/1.1 200 OK", "<!DOCTYPE html>\n<html>\n<head>\n<title>OK!</title>\n</head>\n<body>\n<p>Everything is ok</p>\n</body>\n</html>")
    } else {
        ("HTTP/1.1 404 NOT FOUND", "<!DOCTYPE html>\n<html>\n<head>\n<title>NOT FOUND!</title>\n</head>\n<body>\n<p>Lost..</p>\n</body>\n</html>")
    };

    let length = resp.len();

    let response =
        format!("{status_line}\r\nContent-Length: {length}\r\n\r\n{resp}");

    stream.write_all(response.as_bytes()).unwrap();
}