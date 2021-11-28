clean:
	@docker container prune -f --filter label=stage=raft.build
	@docker container prune -f --filter label=stage=raft.final
	@docker image prune -f --filter label=stage=raft.build
	@docker rmi raftnode:test
