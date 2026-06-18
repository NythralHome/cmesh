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
- Capability-aware scheduler placement.
- Worker job polling or assignment.
- Result reporting.
- Retries and failure states.

## Milestone 3: First AI Runtime

- Managed llama.cpp runtime.
- GGUF model catalog.
- Model install, delete, and generate jobs.
- Model cache and installed inventory awareness.
- Model-family prompt adapters.
- Conversation context and model-scoped memory.
- Runtime status reporting in worker heartbeat.

## Milestone 4: Operator Console Hardening

- Model lifecycle polling without full page reload.
- Cancel controls for model operations.
- Scheduler eligibility explanations.
- Worker health inventory.
- Runtime readiness visibility.
- Conversation and memory management.
- First-run flow from worker invite to model chat.
- Documentation updates before alpha release.

## Milestone 5: Storage Plane

- Artifact metadata.
- Content-addressed local cache.
- Peer transfer prototype.
- Replication policy design.

## Milestone 6: Multi-Manager Consensus

- Raft-backed cluster state.
- Leader election.
- Leader forwarding.
- Snapshots and recovery.
- Manager quorum dashboard.

## Milestone 7: Distributed Model Experiments

- Runtime-specific distributed inference proof of concept.
- LAN-first constraints.
- Explicit compatibility and network requirements.
- Manager-relayed activation frame transport.
- Runtime adapter interface for `prepare`, `prefill`, `decode`, `complete`, and `abort`.
- llama.cpp layer-stage feasibility prototype.
- Logical layer shard execution before physical GGUF slicing.
- Distributed correctness and latency benchmark against a single-worker baseline.
