package ai

// generatedModelsJSON is produced from upstream pi-ai model metadata.
// Provider scope in this port: openai, openai-codex, kimi-coding, zai.
var generatedModelsJSON = `
{
  "kimi-coding": {
    "k2p5": {
      "api": "anthropic-messages",
      "baseUrl": "https://api.kimi.com/coding",
      "contextWindow": 262144,
      "cost": {
        "cacheRead": 0,
        "cacheWrite": 0,
        "input": 0,
        "output": 0
      },
      "id": "k2p5",
      "input": [
        "text",
        "image"
      ],
      "maxTokens": 32768,
      "name": "Kimi K2.5",
      "provider": "kimi-coding",
      "reasoning": true
    },
    "kimi-k2-thinking": {
      "api": "anthropic-messages",
      "baseUrl": "https://api.kimi.com/coding",
      "contextWindow": 262144,
      "cost": {
        "cacheRead": 0,
        "cacheWrite": 0,
        "input": 0,
        "output": 0
      },
      "id": "kimi-k2-thinking",
      "input": [
        "text"
      ],
      "maxTokens": 32768,
      "name": "Kimi K2 Thinking",
      "provider": "kimi-coding",
      "reasoning": true
    }
  },
  "openai": {
    "codex-mini-latest": {
      "api": "openai-responses",
      "baseUrl": "https://api.openai.com/v1",
      "contextWindow": 200000,
      "cost": {
        "cacheRead": 0.375,
        "cacheWrite": 0,
        "input": 1.5,
        "output": 6
      },
      "id": "codex-mini-latest",
      "input": [
        "text"
      ],
      "maxTokens": 100000,
      "name": "Codex Mini",
      "provider": "openai",
      "reasoning": true
    },
    "gpt-4": {
      "api": "openai-responses",
      "baseUrl": "https://api.openai.com/v1",
      "contextWindow": 8192,
      "cost": {
        "cacheRead": 0,
        "cacheWrite": 0,
        "input": 30,
        "output": 60
      },
      "id": "gpt-4",
      "input": [
        "text"
      ],
      "maxTokens": 8192,
      "name": "GPT-4",
      "provider": "openai",
      "reasoning": false
    },
    "gpt-4-turbo": {
      "api": "openai-responses",
      "baseUrl": "https://api.openai.com/v1",
      "contextWindow": 128000,
      "cost": {
        "cacheRead": 0,
        "cacheWrite": 0,
        "input": 10,
        "output": 30
      },
      "id": "gpt-4-turbo",
      "input": [
        "text",
        "image"
      ],
      "maxTokens": 4096,
      "name": "GPT-4 Turbo",
      "provider": "openai",
      "reasoning": false
    },
    "gpt-4.1": {
      "api": "openai-responses",
      "baseUrl": "https://api.openai.com/v1",
      "contextWindow": 1047576,
      "cost": {
        "cacheRead": 0.5,
        "cacheWrite": 0,
        "input": 2,
        "output": 8
      },
      "id": "gpt-4.1",
      "input": [
        "text",
        "image"
      ],
      "maxTokens": 32768,
      "name": "GPT-4.1",
      "provider": "openai",
      "reasoning": false
    },
    "gpt-4.1-mini": {
      "api": "openai-responses",
      "baseUrl": "https://api.openai.com/v1",
      "contextWindow": 1047576,
      "cost": {
        "cacheRead": 0.1,
        "cacheWrite": 0,
        "input": 0.4,
        "output": 1.6
      },
      "id": "gpt-4.1-mini",
      "input": [
        "text",
        "image"
      ],
      "maxTokens": 32768,
      "name": "GPT-4.1 mini",
      "provider": "openai",
      "reasoning": false
    },
    "gpt-4.1-nano": {
      "api": "openai-responses",
      "baseUrl": "https://api.openai.com/v1",
      "contextWindow": 1047576,
      "cost": {
        "cacheRead": 0.03,
        "cacheWrite": 0,
        "input": 0.1,
        "output": 0.4
      },
      "id": "gpt-4.1-nano",
      "input": [
        "text",
        "image"
      ],
      "maxTokens": 32768,
      "name": "GPT-4.1 nano",
      "provider": "openai",
      "reasoning": false
    },
    "gpt-4o": {
      "api": "openai-responses",
      "baseUrl": "https://api.openai.com/v1",
      "contextWindow": 128000,
      "cost": {
        "cacheRead": 1.25,
        "cacheWrite": 0,
        "input": 2.5,
        "output": 10
      },
      "id": "gpt-4o",
      "input": [
        "text",
        "image"
      ],
      "maxTokens": 16384,
      "name": "GPT-4o",
      "provider": "openai",
      "reasoning": false
    },
    "gpt-4o-2024-05-13": {
      "api": "openai-responses",
      "baseUrl": "https://api.openai.com/v1",
      "contextWindow": 128000,
      "cost": {
        "cacheRead": 0,
        "cacheWrite": 0,
        "input": 5,
        "output": 15
      },
      "id": "gpt-4o-2024-05-13",
      "input": [
        "text",
        "image"
      ],
      "maxTokens": 4096,
      "name": "GPT-4o (2024-05-13)",
      "provider": "openai",
      "reasoning": false
    },
    "gpt-4o-2024-08-06": {
      "api": "openai-responses",
      "baseUrl": "https://api.openai.com/v1",
      "contextWindow": 128000,
      "cost": {
        "cacheRead": 1.25,
        "cacheWrite": 0,
        "input": 2.5,
        "output": 10
      },
      "id": "gpt-4o-2024-08-06",
      "input": [
        "text",
        "image"
      ],
      "maxTokens": 16384,
      "name": "GPT-4o (2024-08-06)",
      "provider": "openai",
      "reasoning": false
    },
    "gpt-4o-2024-11-20": {
      "api": "openai-responses",
      "baseUrl": "https://api.openai.com/v1",
      "contextWindow": 128000,
      "cost": {
        "cacheRead": 1.25,
        "cacheWrite": 0,
        "input": 2.5,
        "output": 10
      },
      "id": "gpt-4o-2024-11-20",
      "input": [
        "text",
        "image"
      ],
      "maxTokens": 16384,
      "name": "GPT-4o (2024-11-20)",
      "provider": "openai",
      "reasoning": false
    },
    "gpt-4o-mini": {
      "api": "openai-responses",
      "baseUrl": "https://api.openai.com/v1",
      "contextWindow": 128000,
      "cost": {
        "cacheRead": 0.08,
        "cacheWrite": 0,
        "input": 0.15,
        "output": 0.6
      },
      "id": "gpt-4o-mini",
      "input": [
        "text",
        "image"
      ],
      "maxTokens": 16384,
      "name": "GPT-4o mini",
      "provider": "openai",
      "reasoning": false
    },
    "gpt-5": {
      "api": "openai-responses",
      "baseUrl": "https://api.openai.com/v1",
      "contextWindow": 400000,
      "cost": {
        "cacheRead": 0.125,
        "cacheWrite": 0,
        "input": 1.25,
        "output": 10
      },
      "id": "gpt-5",
      "input": [
        "text",
        "image"
      ],
      "maxTokens": 128000,
      "name": "GPT-5",
      "provider": "openai",
      "reasoning": true
    },
    "gpt-5-chat-latest": {
      "api": "openai-responses",
      "baseUrl": "https://api.openai.com/v1",
      "contextWindow": 128000,
      "cost": {
        "cacheRead": 0.125,
        "cacheWrite": 0,
        "input": 1.25,
        "output": 10
      },
      "id": "gpt-5-chat-latest",
      "input": [
        "text",
        "image"
      ],
      "maxTokens": 16384,
      "name": "GPT-5 Chat Latest",
      "provider": "openai",
      "reasoning": false
    },
    "gpt-5-codex": {
      "api": "openai-responses",
      "baseUrl": "https://api.openai.com/v1",
      "contextWindow": 400000,
      "cost": {
        "cacheRead": 0.125,
        "cacheWrite": 0,
        "input": 1.25,
        "output": 10
      },
      "id": "gpt-5-codex",
      "input": [
        "text",
        "image"
      ],
      "maxTokens": 128000,
      "name": "GPT-5-Codex",
      "provider": "openai",
      "reasoning": true
    },
    "gpt-5-mini": {
      "api": "openai-responses",
      "baseUrl": "https://api.openai.com/v1",
      "contextWindow": 400000,
      "cost": {
        "cacheRead": 0.025,
        "cacheWrite": 0,
        "input": 0.25,
        "output": 2
      },
      "id": "gpt-5-mini",
      "input": [
        "text",
        "image"
      ],
      "maxTokens": 128000,
      "name": "GPT-5 Mini",
      "provider": "openai",
      "reasoning": true
    },
    "gpt-5-nano": {
      "api": "openai-responses",
      "baseUrl": "https://api.openai.com/v1",
      "contextWindow": 400000,
      "cost": {
        "cacheRead": 0.005,
        "cacheWrite": 0,
        "input": 0.05,
        "output": 0.4
      },
      "id": "gpt-5-nano",
      "input": [
        "text",
        "image"
      ],
      "maxTokens": 128000,
      "name": "GPT-5 Nano",
      "provider": "openai",
      "reasoning": true
    },
    "gpt-5-pro": {
      "api": "openai-responses",
      "baseUrl": "https://api.openai.com/v1",
      "contextWindow": 400000,
      "cost": {
        "cacheRead": 0,
        "cacheWrite": 0,
        "input": 15,
        "output": 120
      },
      "id": "gpt-5-pro",
      "input": [
        "text",
        "image"
      ],
      "maxTokens": 272000,
      "name": "GPT-5 Pro",
      "provider": "openai",
      "reasoning": true
    },
    "gpt-5.1": {
      "api": "openai-responses",
      "baseUrl": "https://api.openai.com/v1",
      "contextWindow": 400000,
      "cost": {
        "cacheRead": 0.13,
        "cacheWrite": 0,
        "input": 1.25,
        "output": 10
      },
      "id": "gpt-5.1",
      "input": [
        "text",
        "image"
      ],
      "maxTokens": 128000,
      "name": "GPT-5.1",
      "provider": "openai",
      "reasoning": true
    },
    "gpt-5.1-chat-latest": {
      "api": "openai-responses",
      "baseUrl": "https://api.openai.com/v1",
      "contextWindow": 128000,
      "cost": {
        "cacheRead": 0.125,
        "cacheWrite": 0,
        "input": 1.25,
        "output": 10
      },
      "id": "gpt-5.1-chat-latest",
      "input": [
        "text",
        "image"
      ],
      "maxTokens": 16384,
      "name": "GPT-5.1 Chat",
      "provider": "openai",
      "reasoning": true
    },
    "gpt-5.1-codex": {
      "api": "openai-responses",
      "baseUrl": "https://api.openai.com/v1",
      "contextWindow": 400000,
      "cost": {
        "cacheRead": 0.125,
        "cacheWrite": 0,
        "input": 1.25,
        "output": 10
      },
      "id": "gpt-5.1-codex",
      "input": [
        "text",
        "image"
      ],
      "maxTokens": 128000,
      "name": "GPT-5.1 Codex",
      "provider": "openai",
      "reasoning": true
    },
    "gpt-5.1-codex-max": {
      "api": "openai-responses",
      "baseUrl": "https://api.openai.com/v1",
      "contextWindow": 400000,
      "cost": {
        "cacheRead": 0.125,
        "cacheWrite": 0,
        "input": 1.25,
        "output": 10
      },
      "id": "gpt-5.1-codex-max",
      "input": [
        "text",
        "image"
      ],
      "maxTokens": 128000,
      "name": "GPT-5.1 Codex Max",
      "provider": "openai",
      "reasoning": true
    },
    "gpt-5.1-codex-mini": {
      "api": "openai-responses",
      "baseUrl": "https://api.openai.com/v1",
      "contextWindow": 400000,
      "cost": {
        "cacheRead": 0.025,
        "cacheWrite": 0,
        "input": 0.25,
        "output": 2
      },
      "id": "gpt-5.1-codex-mini",
      "input": [
        "text",
        "image"
      ],
      "maxTokens": 128000,
      "name": "GPT-5.1 Codex mini",
      "provider": "openai",
      "reasoning": true
    },
    "gpt-5.2": {
      "api": "openai-responses",
      "baseUrl": "https://api.openai.com/v1",
      "contextWindow": 400000,
      "cost": {
        "cacheRead": 0.175,
        "cacheWrite": 0,
        "input": 1.75,
        "output": 14
      },
      "id": "gpt-5.2",
      "input": [
        "text",
        "image"
      ],
      "maxTokens": 128000,
      "name": "GPT-5.2",
      "provider": "openai",
      "reasoning": true
    },
    "gpt-5.2-chat-latest": {
      "api": "openai-responses",
      "baseUrl": "https://api.openai.com/v1",
      "contextWindow": 128000,
      "cost": {
        "cacheRead": 0.175,
        "cacheWrite": 0,
        "input": 1.75,
        "output": 14
      },
      "id": "gpt-5.2-chat-latest",
      "input": [
        "text",
        "image"
      ],
      "maxTokens": 16384,
      "name": "GPT-5.2 Chat",
      "provider": "openai",
      "reasoning": true
    },
    "gpt-5.2-codex": {
      "api": "openai-responses",
      "baseUrl": "https://api.openai.com/v1",
      "contextWindow": 400000,
      "cost": {
        "cacheRead": 0.175,
        "cacheWrite": 0,
        "input": 1.75,
        "output": 14
      },
      "id": "gpt-5.2-codex",
      "input": [
        "text",
        "image"
      ],
      "maxTokens": 128000,
      "name": "GPT-5.2 Codex",
      "provider": "openai",
      "reasoning": true
    },
    "gpt-5.2-pro": {
      "api": "openai-responses",
      "baseUrl": "https://api.openai.com/v1",
      "contextWindow": 400000,
      "cost": {
        "cacheRead": 0,
        "cacheWrite": 0,
        "input": 21,
        "output": 168
      },
      "id": "gpt-5.2-pro",
      "input": [
        "text",
        "image"
      ],
      "maxTokens": 128000,
      "name": "GPT-5.2 Pro",
      "provider": "openai",
      "reasoning": true
    },
    "gpt-5.3-codex": {
      "api": "openai-responses",
      "baseUrl": "https://api.openai.com/v1",
      "contextWindow": 400000,
      "cost": {
        "cacheRead": 0.175,
        "cacheWrite": 0,
        "input": 1.75,
        "output": 14
      },
      "id": "gpt-5.3-codex",
      "input": [
        "text",
        "image"
      ],
      "maxTokens": 128000,
      "name": "GPT-5.3 Codex",
      "provider": "openai",
      "reasoning": true
    },
    "gpt-5.3-codex-spark": {
      "api": "openai-responses",
      "baseUrl": "https://api.openai.com/v1",
      "contextWindow": 128000,
      "cost": {
        "cacheRead": 0.175,
        "cacheWrite": 0,
        "input": 1.75,
        "output": 14
      },
      "id": "gpt-5.3-codex-spark",
      "input": [
        "text",
        "image"
      ],
      "maxTokens": 32000,
      "name": "GPT-5.3 Codex Spark",
      "provider": "openai",
      "reasoning": true
    },
    "o1": {
      "api": "openai-responses",
      "baseUrl": "https://api.openai.com/v1",
      "contextWindow": 200000,
      "cost": {
        "cacheRead": 7.5,
        "cacheWrite": 0,
        "input": 15,
        "output": 60
      },
      "id": "o1",
      "input": [
        "text",
        "image"
      ],
      "maxTokens": 100000,
      "name": "o1",
      "provider": "openai",
      "reasoning": true
    },
    "o1-pro": {
      "api": "openai-responses",
      "baseUrl": "https://api.openai.com/v1",
      "contextWindow": 200000,
      "cost": {
        "cacheRead": 0,
        "cacheWrite": 0,
        "input": 150,
        "output": 600
      },
      "id": "o1-pro",
      "input": [
        "text",
        "image"
      ],
      "maxTokens": 100000,
      "name": "o1-pro",
      "provider": "openai",
      "reasoning": true
    },
    "o3": {
      "api": "openai-responses",
      "baseUrl": "https://api.openai.com/v1",
      "contextWindow": 200000,
      "cost": {
        "cacheRead": 0.5,
        "cacheWrite": 0,
        "input": 2,
        "output": 8
      },
      "id": "o3",
      "input": [
        "text",
        "image"
      ],
      "maxTokens": 100000,
      "name": "o3",
      "provider": "openai",
      "reasoning": true
    },
    "o3-deep-research": {
      "api": "openai-responses",
      "baseUrl": "https://api.openai.com/v1",
      "contextWindow": 200000,
      "cost": {
        "cacheRead": 2.5,
        "cacheWrite": 0,
        "input": 10,
        "output": 40
      },
      "id": "o3-deep-research",
      "input": [
        "text",
        "image"
      ],
      "maxTokens": 100000,
      "name": "o3-deep-research",
      "provider": "openai",
      "reasoning": true
    },
    "o3-mini": {
      "api": "openai-responses",
      "baseUrl": "https://api.openai.com/v1",
      "contextWindow": 200000,
      "cost": {
        "cacheRead": 0.55,
        "cacheWrite": 0,
        "input": 1.1,
        "output": 4.4
      },
      "id": "o3-mini",
      "input": [
        "text"
      ],
      "maxTokens": 100000,
      "name": "o3-mini",
      "provider": "openai",
      "reasoning": true
    },
    "o3-pro": {
      "api": "openai-responses",
      "baseUrl": "https://api.openai.com/v1",
      "contextWindow": 200000,
      "cost": {
        "cacheRead": 0,
        "cacheWrite": 0,
        "input": 20,
        "output": 80
      },
      "id": "o3-pro",
      "input": [
        "text",
        "image"
      ],
      "maxTokens": 100000,
      "name": "o3-pro",
      "provider": "openai",
      "reasoning": true
    },
    "o4-mini": {
      "api": "openai-responses",
      "baseUrl": "https://api.openai.com/v1",
      "contextWindow": 200000,
      "cost": {
        "cacheRead": 0.28,
        "cacheWrite": 0,
        "input": 1.1,
        "output": 4.4
      },
      "id": "o4-mini",
      "input": [
        "text",
        "image"
      ],
      "maxTokens": 100000,
      "name": "o4-mini",
      "provider": "openai",
      "reasoning": true
    },
    "o4-mini-deep-research": {
      "api": "openai-responses",
      "baseUrl": "https://api.openai.com/v1",
      "contextWindow": 200000,
      "cost": {
        "cacheRead": 0.5,
        "cacheWrite": 0,
        "input": 2,
        "output": 8
      },
      "id": "o4-mini-deep-research",
      "input": [
        "text",
        "image"
      ],
      "maxTokens": 100000,
      "name": "o4-mini-deep-research",
      "provider": "openai",
      "reasoning": true
    }
  },
  "openai-codex": {
    "gpt-5.1": {
      "api": "openai-codex-responses",
      "baseUrl": "https://chatgpt.com/backend-api",
      "contextWindow": 272000,
      "cost": {
        "cacheRead": 0.125,
        "cacheWrite": 0,
        "input": 1.25,
        "output": 10
      },
      "id": "gpt-5.1",
      "input": [
        "text",
        "image"
      ],
      "maxTokens": 128000,
      "name": "GPT-5.1",
      "provider": "openai-codex",
      "reasoning": true
    },
    "gpt-5.1-codex-max": {
      "api": "openai-codex-responses",
      "baseUrl": "https://chatgpt.com/backend-api",
      "contextWindow": 272000,
      "cost": {
        "cacheRead": 0.125,
        "cacheWrite": 0,
        "input": 1.25,
        "output": 10
      },
      "id": "gpt-5.1-codex-max",
      "input": [
        "text",
        "image"
      ],
      "maxTokens": 128000,
      "name": "GPT-5.1 Codex Max",
      "provider": "openai-codex",
      "reasoning": true
    },
    "gpt-5.1-codex-mini": {
      "api": "openai-codex-responses",
      "baseUrl": "https://chatgpt.com/backend-api",
      "contextWindow": 272000,
      "cost": {
        "cacheRead": 0.025,
        "cacheWrite": 0,
        "input": 0.25,
        "output": 2
      },
      "id": "gpt-5.1-codex-mini",
      "input": [
        "text",
        "image"
      ],
      "maxTokens": 128000,
      "name": "GPT-5.1 Codex Mini",
      "provider": "openai-codex",
      "reasoning": true
    },
    "gpt-5.2": {
      "api": "openai-codex-responses",
      "baseUrl": "https://chatgpt.com/backend-api",
      "contextWindow": 272000,
      "cost": {
        "cacheRead": 0.175,
        "cacheWrite": 0,
        "input": 1.75,
        "output": 14
      },
      "id": "gpt-5.2",
      "input": [
        "text",
        "image"
      ],
      "maxTokens": 128000,
      "name": "GPT-5.2",
      "provider": "openai-codex",
      "reasoning": true
    },
    "gpt-5.2-codex": {
      "api": "openai-codex-responses",
      "baseUrl": "https://chatgpt.com/backend-api",
      "contextWindow": 272000,
      "cost": {
        "cacheRead": 0.175,
        "cacheWrite": 0,
        "input": 1.75,
        "output": 14
      },
      "id": "gpt-5.2-codex",
      "input": [
        "text",
        "image"
      ],
      "maxTokens": 128000,
      "name": "GPT-5.2 Codex",
      "provider": "openai-codex",
      "reasoning": true
    },
    "gpt-5.3-codex": {
      "api": "openai-codex-responses",
      "baseUrl": "https://chatgpt.com/backend-api",
      "contextWindow": 272000,
      "cost": {
        "cacheRead": 0.175,
        "cacheWrite": 0,
        "input": 1.75,
        "output": 14
      },
      "id": "gpt-5.3-codex",
      "input": [
        "text",
        "image"
      ],
      "maxTokens": 128000,
      "name": "GPT-5.3 Codex",
      "provider": "openai-codex",
      "reasoning": true
    },
    "gpt-5.3-codex-spark": {
      "api": "openai-codex-responses",
      "baseUrl": "https://chatgpt.com/backend-api",
      "contextWindow": 128000,
      "cost": {
        "cacheRead": 0,
        "cacheWrite": 0,
        "input": 0,
        "output": 0
      },
      "id": "gpt-5.3-codex-spark",
      "input": [
        "text"
      ],
      "maxTokens": 128000,
      "name": "GPT-5.3 Codex Spark",
      "provider": "openai-codex",
      "reasoning": true
    }
  },
  "zai": {
    "glm-4.5": {
      "api": "openai-completions",
      "baseUrl": "https://api.z.ai/api/coding/paas/v4",
      "compat": {
        "supportsDeveloperRole": false,
        "thinkingFormat": "zai"
      },
      "contextWindow": 131072,
      "cost": {
        "cacheRead": 0.11,
        "cacheWrite": 0,
        "input": 0.6,
        "output": 2.2
      },
      "id": "glm-4.5",
      "input": [
        "text"
      ],
      "maxTokens": 98304,
      "name": "GLM-4.5",
      "provider": "zai",
      "reasoning": true
    },
    "glm-4.5-air": {
      "api": "openai-completions",
      "baseUrl": "https://api.z.ai/api/coding/paas/v4",
      "compat": {
        "supportsDeveloperRole": false,
        "thinkingFormat": "zai"
      },
      "contextWindow": 131072,
      "cost": {
        "cacheRead": 0.03,
        "cacheWrite": 0,
        "input": 0.2,
        "output": 1.1
      },
      "id": "glm-4.5-air",
      "input": [
        "text"
      ],
      "maxTokens": 98304,
      "name": "GLM-4.5-Air",
      "provider": "zai",
      "reasoning": true
    },
    "glm-4.5-flash": {
      "api": "openai-completions",
      "baseUrl": "https://api.z.ai/api/coding/paas/v4",
      "compat": {
        "supportsDeveloperRole": false,
        "thinkingFormat": "zai"
      },
      "contextWindow": 131072,
      "cost": {
        "cacheRead": 0,
        "cacheWrite": 0,
        "input": 0,
        "output": 0
      },
      "id": "glm-4.5-flash",
      "input": [
        "text"
      ],
      "maxTokens": 98304,
      "name": "GLM-4.5-Flash",
      "provider": "zai",
      "reasoning": true
    },
    "glm-4.5v": {
      "api": "openai-completions",
      "baseUrl": "https://api.z.ai/api/coding/paas/v4",
      "compat": {
        "supportsDeveloperRole": false,
        "thinkingFormat": "zai"
      },
      "contextWindow": 64000,
      "cost": {
        "cacheRead": 0,
        "cacheWrite": 0,
        "input": 0.6,
        "output": 1.8
      },
      "id": "glm-4.5v",
      "input": [
        "text",
        "image"
      ],
      "maxTokens": 16384,
      "name": "GLM-4.5V",
      "provider": "zai",
      "reasoning": true
    },
    "glm-4.6": {
      "api": "openai-completions",
      "baseUrl": "https://api.z.ai/api/coding/paas/v4",
      "compat": {
        "supportsDeveloperRole": false,
        "thinkingFormat": "zai"
      },
      "contextWindow": 204800,
      "cost": {
        "cacheRead": 0.11,
        "cacheWrite": 0,
        "input": 0.6,
        "output": 2.2
      },
      "id": "glm-4.6",
      "input": [
        "text"
      ],
      "maxTokens": 131072,
      "name": "GLM-4.6",
      "provider": "zai",
      "reasoning": true
    },
    "glm-4.6v": {
      "api": "openai-completions",
      "baseUrl": "https://api.z.ai/api/coding/paas/v4",
      "compat": {
        "supportsDeveloperRole": false,
        "thinkingFormat": "zai"
      },
      "contextWindow": 128000,
      "cost": {
        "cacheRead": 0,
        "cacheWrite": 0,
        "input": 0.3,
        "output": 0.9
      },
      "id": "glm-4.6v",
      "input": [
        "text",
        "image"
      ],
      "maxTokens": 32768,
      "name": "GLM-4.6V",
      "provider": "zai",
      "reasoning": true
    },
    "glm-4.7": {
      "api": "openai-completions",
      "baseUrl": "https://api.z.ai/api/coding/paas/v4",
      "compat": {
        "supportsDeveloperRole": false,
        "thinkingFormat": "zai"
      },
      "contextWindow": 204800,
      "cost": {
        "cacheRead": 0.11,
        "cacheWrite": 0,
        "input": 0.6,
        "output": 2.2
      },
      "id": "glm-4.7",
      "input": [
        "text"
      ],
      "maxTokens": 131072,
      "name": "GLM-4.7",
      "provider": "zai",
      "reasoning": true
    },
    "glm-4.7-flash": {
      "api": "openai-completions",
      "baseUrl": "https://api.z.ai/api/coding/paas/v4",
      "compat": {
        "supportsDeveloperRole": false,
        "thinkingFormat": "zai"
      },
      "contextWindow": 200000,
      "cost": {
        "cacheRead": 0,
        "cacheWrite": 0,
        "input": 0,
        "output": 0
      },
      "id": "glm-4.7-flash",
      "input": [
        "text"
      ],
      "maxTokens": 131072,
      "name": "GLM-4.7-Flash",
      "provider": "zai",
      "reasoning": true
    },
    "glm-5": {
      "api": "openai-completions",
      "baseUrl": "https://api.z.ai/api/coding/paas/v4",
      "compat": {
        "supportsDeveloperRole": false,
        "thinkingFormat": "zai"
      },
      "contextWindow": 204800,
      "cost": {
        "cacheRead": 0.2,
        "cacheWrite": 0,
        "input": 1,
        "output": 3.2
      },
      "id": "glm-5",
      "input": [
        "text"
      ],
      "maxTokens": 131072,
      "name": "GLM-5",
      "provider": "zai",
      "reasoning": true
    }
  }
}
`
