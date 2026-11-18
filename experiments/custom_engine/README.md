# Custom Inference Engine — Learning Playground

This directory is your space to experiment with building a custom LLM inference
backend that integrates with blooper's `InferenceEngine` interface.

## Architecture

blooper uses a pluggable engine interface (`internal/llm/engine.go`). The default
implementation talks to Ollama over HTTP. You can replace it with your own.

## Ideas to Explore

### 1. Custom KV Cache Manager
- Implement the `KVManager` interface in `internal/llm/kv_placeholder.go`
- Build prefix caching for shared system prompts across agents
- Implement paged attention buffer pools

### 2. Custom CUDA Inference Server
- Write a C++ or Python server wrapping llama.cpp
- Compile CUDA kernels for Pascal: `nvcc -arch=sm_61`
- Expose a gRPC or HTTP API that matches the `InferenceEngine` interface

### 3. vLLM Integration (Note: Pascal Limitations)
- Official vLLM dropped Pascal (compute capability < 7.0) support
- You may need a community fork or to write custom Triton kernels
- Consider starting with llama.cpp CUDA backend instead

## KV Cache Sizing Formula

```
KV_cache_bytes = 2 × num_layers × num_kv_heads × head_dim × precision_bytes × seq_len
```

### Examples:
| Model | Layers | KV Heads | Head Dim | FP16 Bytes/Token | 4096 ctx |
|-------|--------|----------|----------|-----------------|----------|
| Qwen3 8B  | 32 | 8  | 128 | 131 KB | ~512 MB  |
| Qwen3 14B | 40 | 8  | 128 | 164 KB | ~640 MB  |
| Llama 3.1 8B | 32 | 8 | 128 | 131 KB | ~512 MB |

## Getting Started

1. Read `internal/llm/engine.go` to understand the interface contract
2. Read `internal/llm/kv_placeholder.go` for the KV manager stubs
3. Copy `stub_client.go` as a starting point for your custom engine. It is a library example
   (`package customengine`), so repository-wide builds do not require a `main` entry point.
4. Register your engine in `cmd/blooper/main.go` using a flag or config option
