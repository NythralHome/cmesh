# Architecture Decisions

## ADR-0001: Manager/Worker Architecture

Status: accepted

CMesh uses manager nodes and worker nodes. Managers coordinate cluster state and scheduling. Workers contribute bounded resources and execute supported workloads.

This avoids building a fully symmetric peer-to-peer system before the project has reliable compute, benchmark, and job semantics. It still leaves room for decentralization because managers are a role that can be replicated through consensus.

## ADR-0002: Single-Manager Bootstrap, Multi-Manager Design

Status: accepted

The first runnable version may use one manager, but code should be structured as if cluster state can later be replicated through consensus.

The initial consensus implementation can be single-node. Future implementations can provide Raft without rewriting scheduler, membership, or API code.

## ADR-0003: AI Artifact Storage, Not General Ceph

Status: accepted

CMesh will include storage functionality for AI artifacts: models, tokenizer files, job inputs, job outputs, benchmark data, and cache metadata.

CMesh will not attempt to provide block storage, POSIX filesystem semantics, or a full Ceph replacement in V1.

## ADR-0004: CDIP As The Distributed Inference Protocol

Status: accepted

CMesh will define distributed inference through CDIP, the CMesh Distributed Inference Protocol.

The protocol is versioned separately from manager dashboard APIs. CDIP defines roles, message envelopes, stage lifecycle, distributed plan shape, activation frame envelopes, and conformance validation. Manager REST endpoints may expose CDIP messages, but those endpoints are not themselves the protocol.

The first CDIP version is intentionally limited to planning, stage job graph construction, lifecycle messages, and activation frame envelopes. Worker-to-worker activation transport and runtime-specific layer execution can be implemented behind the CDIP contract without changing the high-level protocol semantics.

## ADR-0005: Runtime Adapter Gate For Real Distributed Inference

Status: accepted

CMesh will not claim real cross-machine model execution until a runtime adapter can execute model layer stages and exchange activation frames between workers.

The first target is pipeline layer splitting because it maps to CDIP stage plans and to llama.cpp's documented `layer` multi-GPU split semantics. The current llama.cpp tooling supports splitting work across multiple GPUs visible to one host, but CMesh needs independent workers on separate machines. That gap must be closed behind a runtime adapter rather than by changing the CDIP control plane.

Distributed plans may be feasible from a resource and placement perspective while still reporting `executable_now: false`. The explicit blocker is the missing distributed tensor runtime adapter, not the CDIP protocol itself.
