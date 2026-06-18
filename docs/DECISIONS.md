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
