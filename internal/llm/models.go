package llm

// HardwareProfile defines recommended model settings for a target machine.
type HardwareProfile struct {
    Name            string `yaml:"name"`
    DefaultModel    string `yaml:"default_model"`
    FallbackModel   string `yaml:"fallback_model"`
    MaxContextLen   int    `yaml:"max_context_length"`
    MaxParallelReqs int    `yaml:"max_parallel_requests"`
    Notes           string `yaml:"notes"`
}

var HardwareProfiles = map[string]HardwareProfile{
    "m5_pro_24gb": {
        Name: "Apple M5 Pro (24GB)", DefaultModel: "qwen3:14b", FallbackModel: "qwen3:8b",
        MaxContextLen: 8192, MaxParallelReqs: 2,
        Notes: "UMA ~16-18GB usable. 14B Q4 fits comfortably.",
    },
    "linux_1080ti": {
        Name: "Linux AMD + GTX 1080 Ti (11GB VRAM)", DefaultModel: "qwen3:8b", FallbackModel: "qwen3:4b",
        MaxContextLen: 4096, MaxParallelReqs: 1,
        Notes: "Pascal arch. 8B Q4 ~5GB weights + ~2GB KV = ~7GB. Stay under 11GB.",
    },
}
