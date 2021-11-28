package main

import (
	"context"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	pb "github.com/devMYC/raft/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
)

const (
	port = ":8080"
)

type Server struct {
	cm    *ConsensusModule
	peers map[int]pb.RpcClient
	pb.UnimplementedRpcServer
}

func (s *Server) RequestVote(ctx context.Context, in *pb.RequestVoteArgs) (*pb.RequestVoteResult, error) {
	s.cm.mu.Lock()
	defer s.cm.mu.Unlock()

	resp := &pb.RequestVoteResult{
		Term:        int32(s.cm.state.currentTerm),
		VoteGranted: false,
	}

	term := int(in.Term)
	candidateId := int(in.CandidateId)

	if term < s.cm.state.currentTerm {
		return resp, nil
	}

	entry, i := s.cm.state.getLastLogEntry()

	if term > s.cm.state.currentTerm {
		s.cm.becomeFollower(term, s.peers)
		resp.Term = in.Term
	}

	if term == s.cm.state.currentTerm &&
		(s.cm.state.votedFor == -1 || s.cm.state.votedFor == candidateId) &&
		(entry == nil || in.LastLogTerm > entry.Term || in.LastLogTerm == entry.Term && int(in.LastLogIndex) >= i) {
		s.cm.latestUpdateAt = time.Now()
		resp.VoteGranted = true
		s.cm.state.votedFor = candidateId
	}

	return resp, nil
}

func (s *Server) AppendEntries(ctx context.Context, in *pb.AppendEntriesArgs) (*pb.AppendEntriesResult, error) {
	s.cm.mu.Lock()
	defer s.cm.mu.Unlock()

	term := int(in.Term)
	resp := &pb.AppendEntriesResult{
		Term:    int32(s.cm.state.currentTerm),
		Success: false,
	}

	if term < s.cm.state.currentTerm {
		return resp, nil
	}

	if term > s.cm.state.currentTerm || s.cm.role == Candidate {
		s.cm.becomeFollower(term, s.peers)
	} else {
		s.cm.latestUpdateAt = time.Now()
	}

	_, idx := s.cm.state.getLastLogEntry()

	if in.PrevLogIndex >= 0 && (in.PrevLogIndex > int32(idx) || s.cm.state.log[in.PrevLogIndex].Term != in.PrevLogTerm) {
		return resp, nil
	}

	resp.Success = true
	i := int(in.PrevLogIndex + 1)
	j := 0

	for ; i < len(s.cm.state.log) && j < len(in.Entries); i, j = i+1, j+1 {
		if s.cm.state.log[i].Term != in.Entries[j].Term {
			break
		}
	}

	leaderCommit := int(in.LeaderCommit)

	if j < len(in.Entries) {
		s.cm.state.log = append(s.cm.state.log[:i], in.Entries[j:]...)
	}

	if leaderCommit > s.cm.state.commitIndex {
		s.cm.state.commitIndex = Min(leaderCommit, len(s.cm.state.log)-1)
		for i, entry := range s.cm.state.log[s.cm.state.lastApplied+1 : s.cm.state.commitIndex+1] {
			log.Printf("[AppendEntries] applying log entry=%+v to state machine\n", *entry)
			s.cm.state.lastApplied = i
		}
	}

	return resp, nil
}

func (s *Server) ClientRequest(ctx context.Context, in *pb.ClientRequestArgs) (*pb.ClientRequestResult, error) {
	s.cm.mu.Lock()
	defer s.cm.mu.Unlock()

	resp := &pb.ClientRequestResult{
		IsLeader: false,
	}

	if s.cm.role != Leader {
		return resp, nil
	}

	resp.IsLeader = true
	s.cm.state.log = append(s.cm.state.log, &pb.LogEntry{
		Idx:  int32(len(s.cm.state.log)),
		Term: int32(s.cm.state.currentTerm),
		Cmd:  in.Cmd,
	})

	log.Printf("[ClientRequest] new command=%s appended\n", in.Cmd)

	return resp, nil
}

func main() {
	args := os.Args[1:]
	peerIds := strings.Split(args[1], ",")

	id, err := strconv.Atoi(args[0])
	if err != nil {
		log.Fatalf("Invalid node ID '%s'.\n", args[0])
	}

	peers := make(map[int]pb.RpcClient)
	peerIdsNum := make([]int, 0, len(peerIds))
	var cm *ConsensusModule
	var wg sync.WaitGroup
	wg.Add(len(peerIds))

	for _, s := range peerIds {
		peerId, err := strconv.Atoi(s)
		if err != nil {
			log.Fatalf("Invalid peer ID '%s'.\n", s)
		}
		peerIdsNum = append(peerIdsNum, peerId)
		peerAddr := "node" + s + port
		go func(n int, addr string) {
			time.Sleep(3 * time.Second)
			log.Printf("Peer Address: %s\n", addr)
			conn, err := grpc.Dial(addr, grpc.WithInsecure())
			if err != nil {
				log.Fatalf("Failed to dial peer '%s': %v\n", peerAddr, err)
			}
			for {
				time.Sleep(time.Second)
				if conn.GetState() == connectivity.Ready {
					log.Printf("Connection to node %d %s\n", n, conn.GetState().String())
					wg.Done()
					break
				}
			}
			cm.mu.Lock()
			defer cm.mu.Unlock()
			peers[peerId] = pb.NewRpcClient(conn)
		}(peerId, peerAddr)
	}

	lis, err := net.Listen("tcp", port)
	if err != nil {
		log.Fatalf("Failed to listen: %v\n", err)
	}

	cm = NewConsensusModule(id, peerIdsNum)
	s := grpc.NewServer()
	pb.RegisterRpcServer(s, &Server{
		cm:    cm,
		peers: peers,
	})

	log.Printf("server listening at: %v\n", lis.Addr())

	go func() {
		wg.Wait()
		cm.prepareElection(0, peers)
	}()

	if err := s.Serve(lis); err != nil {
		log.Fatalf("failed to serve: %v\n", err)
	}
}
