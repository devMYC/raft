package main

import (
	"context"
	"flag"
	"log"
	"time"

	pb "github.com/devMYC/raft/proto"
	"google.golang.org/grpc"
)

func main() {
    addr := flag.String("server", "localhost:8080", "server IP:PORT")
    cmd := flag.String("command", "nop", "Command to be executed by raft cluster")
    flag.Parse()

	conn, err := grpc.Dial(*addr, grpc.WithInsecure())
	if err != nil {
		log.Fatalf("failed to connecte: %v\n", err)
	}
	defer conn.Close()

	c := pb.NewRpcClient(conn)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

    resp, err := c.ClientRequest(ctx, &pb.ClientRequestArgs{Cmd: *cmd})
	if err != nil {
		log.Fatalf("failed to send request: %v\n", err)
	}

    log.Printf("received: isLeader=%v", resp.GetIsLeader())
}

