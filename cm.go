package main

import (
	"context"
	"log"
	"math/rand"
	"sync"
	"time"

	pb "github.com/devMYC/raft/proto"
)

// Role is the current role of the server.
type Role int

// Each server can act as exactly one of the following
// roles under normal operation. Each server starts as
// a Follower.
const (
	Follower Role = iota
	Candidate
	Leader
)

// State of Raft algorithm shown in Fig 2 of Raft paper.
type State struct {
	// persistent state on all servers
	currentTerm int
	votedFor    int
	log         []*pb.LogEntry

	// volatile state on all servers
	commitIndex int
	lastApplied int

	// volatile state on leaders
	nextIndex  map[int]int
	matchIndex map[int]int
}

// InitState initializes Raft state for a server.
func InitState(peerIds []int) *State {
	initState := &State{
		currentTerm: 0,
		votedFor:    -1,
		log:         make([]*pb.LogEntry, 0),
		commitIndex: -1,
		lastApplied: -1,
		nextIndex:   make(map[int]int, 0),
		matchIndex:  make(map[int]int, 0),
	}
	for _, id := range peerIds {
		initState.nextIndex[id] = len(initState.log)
		initState.matchIndex[id] = -1
	}
	return initState
}

func (s *State) getLastLogEntry() (*pb.LogEntry, int) {
	length := len(s.log)
	if length <= 0 {
		return nil, -1
	}
	i := length - 1
	return s.log[i], i
}

// ConsensusModule encapsulates the state and
// other information used to run Raft algorithm.
type ConsensusModule struct {
	// sm *StateMachine
	id             int
	latestUpdateAt time.Time
	mu             *sync.Mutex
	role           Role
	state          *State
}

// NewConsensusModule creates a new consensus module with serverID
// used as seed to prevent livelock of elections.
func NewConsensusModule(id int, peerIds []int) *ConsensusModule {
	rand.Seed(int64(id))
	return &ConsensusModule{
		id:             id,
		latestUpdateAt: time.Now(),
		mu:             &sync.Mutex{},
		role:           Follower,
		state:          InitState(peerIds),
	}
}

func (cm *ConsensusModule) becomeFollower(term int, peers map[int]pb.RpcClient) {
	cm.state.currentTerm = term
	cm.state.votedFor = -1
	cm.role = Follower
	cm.latestUpdateAt = time.Now()
	log.Printf("[cm.becomeFollower] term=%d\n", term)
	go cm.prepareElection(term, peers)
}

func (cm *ConsensusModule) becomeCandidate(peers map[int]pb.RpcClient) {
	cm.role = Candidate
	cm.state.currentTerm++
	currTerm := cm.state.currentTerm
	cm.state.votedFor = cm.id
	cm.latestUpdateAt = time.Now()
	log.Printf("[cm.becomeCandidate] newTerm=%d\n", currTerm)
	cm.runElection(currTerm, peers)
}

func (cm *ConsensusModule) becomeLeader(peers map[int]pb.RpcClient) {
	cm.latestUpdateAt = time.Now()
	cm.role = Leader

	for peerID := range peers {
		// Reinitialize `nextIndex`s and `matchIndex`s of peers.
		cm.state.nextIndex[peerID] = len(cm.state.log)
		cm.state.matchIndex[peerID] = -1
	}

	log.Printf("[cm.becomeLeader] state=%+v\n", cm.state)

	go func() {
		// Send initial empty AEs to other servers to prevent new elections
		cm.sendAE(true, peers)

		for {
			time.Sleep(50 * time.Millisecond)

			cm.mu.Lock()
			if cm.role != Leader {
				cm.mu.Unlock()
				return
			}

			for _, N := cm.state.getLastLogEntry(); N > cm.state.commitIndex; N-- {
				if int(cm.state.log[N].Term) != cm.state.currentTerm {
					continue
				}
				count := 1
				for _, matchIdx := range cm.state.matchIndex {
					if matchIdx >= N {
						count++
					}
					if 2*count > len(peers)+1 {
						// Now it's safe to apply the command at log[N] to the state machine
						log.Printf("[cm.becomeLeader] commitIndex advanced to N=%d from %d\n", N, cm.state.commitIndex)
						for _, entry := range cm.state.log[cm.state.lastApplied+1 : N+1] {
							log.Printf("[cm.becomeLeader] applying log entry=%+v to state machine\n", *entry)
							cm.state.lastApplied = int(entry.Idx)
						}
						cm.state.commitIndex = N
						break
					}
				}
			}
			cm.mu.Unlock()

			cm.sendAE(false, peers)
		}
	}()
}

func (cm *ConsensusModule) prepareElection(termBefore int, peers map[int]pb.RpcClient) {
	// Election timeout 150~300ms as suggested in the Raft paper.
	ms := time.Duration(150+rand.Intn(151)) * time.Millisecond
	cm.mu.Lock()
	cm.latestUpdateAt = time.Now()
	cm.mu.Unlock()
	log.Printf("[cm.prepareElection] next election will start in %v\n", ms)

	for {
		time.Sleep(15 * time.Millisecond)

		cm.mu.Lock()
		if cm.role == Leader || termBefore != cm.state.currentTerm {
			cm.mu.Unlock()
			return
		}

		if cm.latestUpdateAt.Add(ms).Before(time.Now()) {
			log.Printf("[cm.prepareElection] term=%d timed out\n", termBefore)
			cm.becomeCandidate(peers)
			cm.mu.Unlock()
			return
		}
		cm.mu.Unlock()
	}
}

func (cm *ConsensusModule) runElection(currTerm int, peers map[int]pb.RpcClient) {
	log.Printf("[cm.runElection] new election for term=%d starts", currTerm)
	voteCount := 1
	for _, c := range peers {
		go func(rpc pb.RpcClient) {
			cm.mu.Lock()
			entry, i := cm.state.getLastLogEntry()
			cm.mu.Unlock()

			args := pb.RequestVoteArgs{
				Term:         int32(currTerm),
				CandidateId:  int32(cm.id),
				LastLogIndex: int32(i),
				LastLogTerm:  0,
			}

			if entry != nil {
				args.LastLogTerm = entry.Term
			}

			ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
			defer cancel()
			resp, err := rpc.RequestVote(ctx, &args)
			if err != nil {
				return
			}

			cm.mu.Lock()
			defer cm.mu.Unlock()

			if cm.role != Candidate {
				return
			}

			respTerm := int(resp.Term)

			if respTerm > currTerm {
				cm.becomeFollower(respTerm, peers)
			} else if respTerm == currTerm && resp.VoteGranted {
				voteCount++
				if 2*voteCount > len(peers)+1 {
					cm.becomeLeader(peers)
				}
			}
		}(c)
	}

	// In case the votes are split and no new leader is elected.
	go cm.prepareElection(currTerm, peers)
}

func (cm *ConsensusModule) sendAE(init bool, peers map[int]pb.RpcClient) {
	cm.mu.Lock()
	termBefore := cm.state.currentTerm
	cm.mu.Unlock()

	for peerID, c := range peers {
		go func(id int, rpc pb.RpcClient) {
			cm.mu.Lock()
			_, lastLogIdx := cm.state.getLastLogEntry()
			nextIdx := cm.state.nextIndex[id]
			prevLogIdx := nextIdx - 1

			args := pb.AppendEntriesArgs{
				Term:         int32(termBefore),
				LeaderId:     int32(cm.id),
				PrevLogIndex: int32(prevLogIdx),
				PrevLogTerm:  0,
				Entries:      make([]*pb.LogEntry, 0, 0),
				LeaderCommit: int32(cm.state.commitIndex),
			}

			if prevLogIdx >= 0 {
				args.PrevLogTerm = cm.state.log[prevLogIdx].Term
			}

			if !init && lastLogIdx >= nextIdx {
				args.Entries = cm.state.log[nextIdx:]
			}

			cm.mu.Unlock()

			ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
			defer cancel()
			resp, err := rpc.AppendEntries(ctx, &args)
			if err != nil {
				return
			}

			cm.mu.Lock()
			defer cm.mu.Unlock()

			respTerm := int(resp.Term)
			if respTerm > cm.state.currentTerm {
				cm.becomeFollower(respTerm, peers)
				return
			}

			if init {
				return
			}

			if resp.Success {
				i := nextIdx + len(args.Entries)
				cm.state.nextIndex[id] = i
				cm.state.matchIndex[id] = i - 1
			} else {
				cm.state.nextIndex[id]--
			}

			if len(args.Entries) > 0 {
				log.Printf("[cm.sendAE] state=%+v\n", cm.state)
			}
		}(peerID, c)
	}
}

