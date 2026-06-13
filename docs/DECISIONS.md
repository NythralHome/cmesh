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

