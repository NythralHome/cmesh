# Roadmap

## Milestone 1: Cluster Foundation

- Manager and worker CLI commands.
- Worker join flow with invite token.
- Heartbeats and liveness.
- Resource limit reporting.
- Basic web dashboard.
- CPU, memory, disk, network, and basic AI benchmark records.

## Milestone 2: Job System

- Job submission API.
- Job state machine.
- Scheduler placement.
- Worker job polling or assignment.
- Result reporting.
- Retries and failure states.

## Milestone 3: First AI Runtime

- Ollama or llama.cpp integration.
- One small supported model.
- Prompt API.
- Latency and throughput metrics.
- Model cache awareness.

## Milestone 4: Storage Plane

- Artifact metadata.
- Content-addressed local cache.
- Peer transfer prototype.
- Replication policy design.

## Milestone 5: Multi-Manager Consensus

- Raft-backed cluster state.
- Leader election.
- Leader forwarding.
- Snapshots and recovery.
- Manager quorum dashboard.

## Milestone 6: Distributed Model Experiments

- Runtime-specific distributed inference proof of concept.
- LAN-first constraints.
- Explicit compatibility and network requirements.

