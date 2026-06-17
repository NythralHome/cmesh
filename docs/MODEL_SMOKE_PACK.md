# CMesh Model Smoke Pack

This pack defines the first models to use when validating local inference on CMesh. The models are intentionally split by risk and hardware size so a release can be tested progressively.

## Tier 0: Runtime and UI Smoke

### Qwen2.5 0.5B Instruct

- ID: `qwen2.5-0.5b-instruct-q4-k-m`
- Required RAM: 2 GB
- Required disk: 1 GB
- Purpose: fastest install/generate/delete validation.
- Expected behavior: answers simple prompts, but quality is limited.
- Required pass:
  - install progress visible in Worker app
  - appears in `Installed Models`
  - one short chat response succeeds
  - delete removes the model and frees storage

## Tier 1: Small Useful Chat

### Phi-3.5 Mini Instruct

- ID: `phi-3.5-mini-instruct-q4-k-m`
- Required RAM: 5 GB
- Required disk: 3 GB
- Purpose: stronger small-model UX test.
- Expected behavior: better instruction following than tiny smoke models.

### Gemma 3 4B IT

- ID: `gemma-3-4b-it-q4-k-m`
- Required RAM: 6 GB
- Required disk: 4 GB
- Purpose: modern small Gemma test.
- Expected behavior: reasonable chat quality, but may need prompt adapter tuning.

## Tier 2: Baseline 7B Models

### Mistral 7B Instruct v0.3

- ID: `mistral-7b-instruct-v0.3-q4-k-m`
- Required RAM: 8 GB
- Required disk: 5 GB
- Purpose: reliable general chat baseline.

### Qwen2.5 7B Instruct

- ID: `qwen2.5-7b-instruct-q4-k-m`
- Required RAM: 8 GB
- Required disk: 5 GB
- Purpose: stronger default chat candidate for normal high-memory laptops/desktops.

### Qwen2.5 Coder 7B Instruct

- ID: `qwen2.5-coder-7b-instruct-q4-k-m`
- Required RAM: 8 GB
- Required disk: 5 GB
- Purpose: developer prompt validation.

## Tier 3: 48 GB Mac / High-Memory Worker

### Qwen2.5 14B Instruct

- ID: `qwen2.5-14b-instruct-q4-k-m`
- Required RAM: 16 GB
- Required disk: 10 GB
- Purpose: first serious large-local-model test.

### Mistral Small 24B Instruct 2501

- ID: `mistral-small-24b-instruct-2501-q4-k-m`
- Required RAM: 26 GB
- Required disk: 15 GB
- Purpose: high-quality general-purpose test on large workers.

### Gemma 3 27B IT

- ID: `gemma-3-27b-it-q4-k-m`
- Required RAM: 30 GB
- Required disk: 18 GB
- Purpose: large Gemma test and prompt adapter validation.

## Tier 4: Upper-End Experimental

### Qwen2.5 32B Instruct

- ID: `qwen2.5-32b-instruct-q4-k-m`
- Required RAM: 34 GB
- Required disk: 21 GB
- Purpose: upper-end instruct test for 48 GB workers.

### Qwen2.5 Coder 32B Instruct

- ID: `qwen2.5-coder-32b-instruct-q4-k-m`
- Required RAM: 34 GB
- Required disk: 21 GB
- Purpose: serious local developer prompt test.

### DeepSeek R1 Distill Qwen 32B

- ID: `deepseek-r1-distill-qwen-32b-q4-k-m`
- Required RAM: 34 GB
- Required disk: 21 GB
- Purpose: experimental reasoning test.
- Expected behavior: slower responses and more adapter sensitivity.

## Standard Test Prompts

Run these prompts in order for each candidate model:

1. `Reply in one short Ukrainian sentence: what is CMesh?`
2. `My name is Sergiy. Remember this for the current conversation.`
3. `What is my name?`
4. `Write a three-step checklist for testing a local AI worker.`
5. `Explain why a model can be installed but not ready to generate.`

## Pass Criteria

- Install job succeeds and shows progress in the Worker app.
- Worker reports installed model path and size.
- Manager shows the model under `Installed Models`.
- Chat response is non-empty and cleaned from template tokens.
- Conversation context works for prompt 2 and 3.
- Delete job succeeds and the model disappears after heartbeat refresh.
- CMesh model storage usage decreases after delete.
