The servers in the cluster communicate with each other via `grpc` calls.
Make sure [protobuf compiler](https://grpc.io/docs/protoc-installation/) is installed.

Also, we need Go plugins for the protobuf compiler

```console
$ go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.26
$ go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.1
```

Update `PATH` so `protoc` can find the plugins

```console
$ export PATH="$PATH:$(go env GOPATH)/bin"
```

Generate gRPC stubs from `.proto` definition file using the following command

```console
$ protoc --go_out=. --go_opt=paths=source_relative \
    --go-grpc_out=. --go-grpc_opt=paths=source_relative \
    proto/raft.proto
```

The `docker-compose.yml` file will create a RAFT cluster with 3 nodes.

```
$ docker-compose build
$ docker-compose up -d
```

After a leader is elected, we can then send requests to the leader node
using `client/client.go`.

__NOTE:__ the `X` in a port shown in the commands should be replaced by the node's ID.
(i.e. 8081 for node1)

```
$ go run client.go -server <dockerHostIP:808X> -command <string>
```

There's also an http server running in each container that can be queried to modify
`iptables` rules.

```
$ curl -i http://dockerHostIP:909X/block    # isolate a node from the cluster
$ curl -i http://dockerHostIP:909X/unblock  # reconnect a node with the cluster
```

To stop the containers and clean up everything, run

```
$ docker-compose down
$ make clean
```

