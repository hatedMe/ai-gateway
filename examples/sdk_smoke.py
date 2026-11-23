"""本地 SDK 冒烟：先编译 bin/gateway.exe，再安装 anthropic 并运行本脚本。"""
import json
import os
from pathlib import Path
import socket
import subprocess
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

from anthropic import Anthropic


class Upstream(BaseHTTPRequestHandler):
    requests = []

    def log_message(self, *args):
        pass

    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        self.requests.append(body)
        if self.path.endswith("/input_tokens"):
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(b'{"object":"response.input_tokens","input_tokens":17}')
            return
        tool = next((t for t in body.get("tools", []) if t["type"] == "function"), None)
        output = [
            {"id": "rs_test", "type": "reasoning", "summary": [{"type": "summary_text", "text": "brief reasoning"}], "encrypted_content": "opaque"},
            {"id": "m_test", "type": "message", "role": "assistant", "content": [{"type": "output_text", "text": "hello STOP hidden"}]},
        ]
        if tool:
            output.append({"id": "fc_test", "type": "function_call", "call_id": "call_test", "name": tool["name"], "arguments": '{"q":"test"}'})
        response = {"id": "resp_test", "model": "mock-model", "status": "completed", "output": output, "usage": {"input_tokens": 20, "output_tokens": 10}}
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream" if body.get("stream") else "application/json")
        self.end_headers()
        if body.get("stream"):
            for event in [{"type": "response.created", "response": {"id": "resp_test", "status": "in_progress"}}, {"type": "response.completed", "response": response}]:
                self.wfile.write(("data: " + json.dumps(event) + "\n\n").encode())
        else:
            self.wfile.write(json.dumps(response).encode())


def main():
    upstream = ThreadingHTTPServer(("127.0.0.1", 0), Upstream)
    threading.Thread(target=upstream.serve_forever, daemon=True).start()
    with socket.socket() as reservation:
        reservation.bind(("127.0.0.1", 0))
        port = reservation.getsockname()[1]
    env = os.environ.copy()
    env.update(OPENAI_API_KEY="mock-key", OPENAI_BASE_URL=f"http://127.0.0.1:{upstream.server_port}/v1", OPENAI_MODEL="mock-model", GATEWAY_ADDR=f"127.0.0.1:{port}", GATEWAY_API_KEY="local-key", OPENAI_MODEL_ALIASES='{"client-model":"mock-model"}', OPENAI_REASONING_EFFORTS="", OPENAI_MODEL_REASONING_EFFORTS="")
    binary = Path(__file__).resolve().parents[1] / "bin" / "gateway.exe"
    process = subprocess.Popen([str(binary)], env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, creationflags=getattr(subprocess, "CREATE_NO_WINDOW", 0))
    try:
        for _ in range(100):
            try:
                with socket.create_connection(("127.0.0.1", port), timeout=0.1):
                    break
            except OSError:
                if process.poll() is not None:
                    raise RuntimeError("网关启动失败")
                time.sleep(0.05)
        with Anthropic(api_key="local-key", base_url=f"http://127.0.0.1:{port}", max_retries=0) as client:
            request = dict(model="client-model", max_tokens=2048, thinking={"type": "enabled", "budget_tokens": 1024}, messages=[{"role": "user", "content": "hello"}])
            assert client.models.list().data[0].id == "client-model"
            assert client.models.retrieve("client-model").id == "client-model"
            assert client.messages.count_tokens(model="client-model", messages=request["messages"]).input_tokens == 17
            normal = client.messages.create(**request, stop_sequences=["STOP"])
            assert normal.stop_reason == "stop_sequence"
            assert normal.content[0].type == "thinking"
            assert normal.content[1].text == "hello "
            with client.messages.stream(**request, stop_sequences=["STOP"]) as stream:
                streamed = stream.get_final_message()
            assert streamed.stop_reason == "stop_sequence"
            assert streamed.content[1].text == "hello "
            original_name = "mcp__" + "long_tool_name_" * 8
            tools = [{"name": original_name, "input_schema": {"type": "object", "properties": {"q": {"type": "string"}}}}]
            called = client.messages.create(**request, tools=tools)
            assert called.content[-1].name == original_name
            with client.messages.stream(**request, tools=tools) as stream:
                tool_stream = stream.get_final_message()
            assert tool_stream.content[-1].input == {"q": "test"}
            history = request["messages"] + [{"role": "assistant", "content": [b.model_dump(exclude_none=True) for b in called.content]}, {"role": "user", "content": [{"type": "tool_result", "tool_use_id": "call_test", "content": "ok"}]}]
            client.messages.create(**{**request, "messages": history}, tools=tools)
            assert any(i["type"] == "function_call_output" for i in Upstream.requests[-1]["input"])
            assert any(i["type"] == "reasoning" and i["encrypted_content"] == "opaque" for i in Upstream.requests[-1]["input"])
        print("SDK smoke passed: models/count/JSON/SSE/thinking/stop/tools/history")
    finally:
        process.terminate()
        process.wait(timeout=5)
        upstream.shutdown()
        upstream.server_close()


if __name__ == "__main__":
    main()
