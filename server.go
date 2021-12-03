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

// Server handles RPC requests between nodes in a cluster to update
// state of consensus module. Requests from client will be processed
// if the current server is the leader.
type Server struct {
	cm    *ConsensusModule
	peers map[int]pb.RpcClient
	pb.UnimplementedRpcServer
}

// RequestVote RPC is received when a node becomes candidate and tries
// to collect votes from other servers to be elected as the new leader.
func (s *Server) RequestVote(ctx context.Context, in *pb.RequestVoteArgs) (*pb.RequestVoteResult, error) {
	s.cm.mu.Lock()
	defer s.cm.mu.Unlock()

	resp := &pb.RequestVoteResult{
		Term:        int32(s.cm.state.currentTerm),
		VoteGranted: false,
	}

	term := int(in.Term)
	candidateID := int(in.CandidateId)

	if term < s.cm.state.currentTerm {
		return resp, nil
	}

	entry, i := s.cm.state.getLastLogEntry()

	if term > s.cm.state.currentTerm {
		// Higher term discovered, fall back to follower.
		s.cm.becomeFollower(term, s.peers)
		resp.Term = in.Term
	}

	if term == s.cm.state.currentTerm &&
		(s.cm.state.votedFor == -1 || s.cm.state.votedFor == candidateID) &&
		(entry == nil || in.LastLogTerm > entry.Term || in.LastLogTerm == entry.Term && int(in.LastLogIndex) >= i) {
		// If we haven't voted for any candidate, or the requester is the one we voted for.
		// Then compare our own log with the candidate's log,
		// grant vote only if candidate's log is as up-to-date as ours.
		s.cm.latestUpdateAt = time.Now() // update time to avoid election timeout
		resp.VoteGranted = true
		s.cm.state.votedFor = candidateID
	}

	return resp, nil
}

// AppendEntries RPC replicates log entries from leader node to other
// nodes in the cluster. This RPC also serves as heartbeat check when
// the log entry list in the request is empty. This heartbeat will
// prevent followers becoming candidate and start new elections.
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
		// If higher term is discovered, or we're still collecting votes.
		// Then fall back to follower.
		s.cm.becomeFollower(term, s.peers)
	} else {
		s.cm.latestUpdateAt = time.Now() // update time to avoid election timeout
	}

	_, idx := s.cm.state.getLastLogEntry()

	if in.PrevLogIndex >= 0 && (in.PrevLogIndex > int32(idx) || s.cm.state.log[in.PrevLogIndex].Term != in.PrevLogTerm) {
		// Our log does not contain an entry at PrevLogIndex,
		// or the log entry at PrevLogIndex has a different term than PrevLogTerm.
		return resp, nil
	}

	resp.Success = true
	i := int(in.PrevLogIndex + 1) // index pointing to entries in our log
	j := 0                        // index pointing to entries in request

	for ; i < len(s.cm.state.log) && j < len(in.Entries); i, j = i+1, j+1 {
		if s.cm.state.log[i].Term != in.Entries[j].Term {
			// Found first entry that does not match
			break
		}
	}

	leaderCommit := int(in.LeaderCommit)

	if j < len(in.Entries) {
		// Delete all log entries starting from the first unmatched one
		// from our log sequence, and append all remaining entries in request.
		s.cm.state.log = append(s.cm.state.log[:i], in.Entries[j:]...)
	}

	if leaderCommit > s.cm.state.commitIndex {
		s.cm.state.commitIndex = Min(leaderCommit, len(s.cm.state.log)-1)
		// Apply commited log entries from the one after the latest applied one.
		for _, entry := range s.cm.state.log[s.cm.state.lastApplied+1 : s.cm.state.commitIndex+1] {
			log.Printf("[AppendEntries] applying log entry=%+v to state machine\n", *entry)
			s.cm.state.lastApplied = int(entry.Idx)
		}
	}

	return resp, nil
}

// ClientRequest RPC appends new log entry containing the `command`
// to be applied to the state machine only if the server is the leader.
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
	peerIDStrs := strings.Split(args[1], ",")

	id, err := strconv.Atoi(args[0])
	if err != nil {
		log.Fatalf("Invalid node ID '%s'.\n", args[0])
	}

	peers := make(map[int]pb.RpcClient)
	peerIds := make([]int, 0, len(peerIDStrs))
	var cm *ConsensusModule
	var wg sync.WaitGroup
	wg.Add(len(peerIDStrs))

	for _, s := range peerIDStrs {
		peerID, err := strconv.Atoi(s)
		if err != nil {
			log.Fatalf("Invalid peer ID '%s'.\n", s)
		}
		peerIds = append(peerIds, peerID)
		peerAddr := "node" + s + port
		go func() {
			time.Sleep(3 * time.Second)
			log.Printf("Peer Address: %s\n", peerAddr)
			conn, err := grpc.Dial(peerAddr, grpc.WithInsecure())
			if err != nil {
				log.Fatalf("Failed to dial peer '%s': %v\n", peerAddr, err)
			}
			for {
				time.Sleep(time.Second)
				if conn.GetState() == connectivity.Ready {
					log.Printf("Connection to node %d %s\n", peerID, conn.GetState().String())
					wg.Done()
					break
				}
			}
			cm.mu.Lock()
			defer cm.mu.Unlock()
			peers[peerID] = pb.NewRpcClient(conn)
		}()
	}

	lis, err := net.Listen("tcp", port)
	if err != nil {
		log.Fatalf("Failed to listen: %v\n", err)
	}

	cm = NewConsensusModule(id, peerIds)
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
