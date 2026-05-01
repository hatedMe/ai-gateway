# Trae CLI API Proxy (Golang Gin)

This is a proxy server implemented in Go using the Gin framework. It converts OpenAI-compatible requests to Trae CLI API requests and wraps the responses back into OpenAI format.

## Setup

1. Install Go (version 1.25.0 or later recommended).
2. Install dependencies:
   ```bash
   go mod tidy
   ```
3. Configure environment variables in `.env`:
   - `TRAE_AUTH`: The `Authorization` header value.
   - `TRAE_EXTRA`: The `Extra` header value (JSON string).
   - `PORT`: The port to run the proxy on (default: 3000).

## Usage

Build and run:
```bash
go run main.go
```

The proxy exposes an endpoint at `POST /v1/chat/completions`.

### Example Request (Streaming)

```bash
curl http://localhost:3000/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "minimax-m2.7",
    "messages": [{"role": "user", "content": "Hello!"}],
    "stream": true
  }'
```

## Mapping Logic

- **Request**: Maps OpenAI `model`, `messages`, and `tools` to Trae's format. The last user message is extracted as `user_input`.
- **Stream**: Handles Trae's SSE events (`output`, `done`) and transforms them into OpenAI-compatible data chunks.
- **Non-Stream**: Accumulates Trae's stream output into a single OpenAI response object, including `reasoning_content` and `usage`.
