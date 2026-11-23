"""使用环境中的上游配置进行真实联调；启动临时网关，结束后关闭。"""
import json
import os
from pathlib import Path
import socket
import subprocess
import time

from anthropic import Anthropic, APIStatusError


def main():
    if not os.environ.get("OPENAI_API_KEY") or not os.environ.get("OPENAI_MODEL"):
        raise RuntimeError("请设置 OPENAI_API_KEY 和 OPENAI_MODEL")
    with socket.socket() as reservation:
        reservation.bind(("127.0.0.1", 0))
        port = reservation.getsockname()[1]
    env = os.environ.copy()
    env.update(GATEWAY_ADDR=f"127.0.0.1:{port}", GATEWAY_API_KEY="live-smoke-local-key")
    binary = Path(__file__).resolve().parents[1] / "bin" / "gateway.exe"
    process = subprocess.Popen([str(binary)], env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, creationflags=getattr(subprocess, "CREATE_NO_WINDOW", 0))
    failures = 0
    try:
        for _ in range(100):
            try:
                with socket.create_connection(("127.0.0.1", port), timeout=0.1):
                    break
            except OSError:
                if process.poll() is not None:
                    raise RuntimeError("网关启动失败")
                time.sleep(0.05)
        with Anthropic(api_key="live-smoke-local-key", base_url=f"http://127.0.0.1:{port}", max_retries=0, timeout=60) as client:
            request = dict(model=env["OPENAI_MODEL"], max_tokens=256, messages=[{"role": "user", "content": "Reply with exactly OK."}])

            def check(name, action):
                nonlocal failures
                try:
                    value = action()
                    print(json.dumps({"test": name, "ok": True, "result": value}, ensure_ascii=True), flush=True)
                except Exception as error:
                    failures += 1
                    detail = error.body if isinstance(error, APIStatusError) else str(error)
                    # Never print credentials even if an upstream echoes them.
                    safe = json.dumps(detail, ensure_ascii=True).replace(env["OPENAI_API_KEY"], "[REDACTED]")
                    print(json.dumps({"test": name, "ok": False, "status": getattr(error, "status_code", None), "error": safe}), flush=True)

            def summarize(message):
                return {"stop_reason": message.stop_reason, "blocks": [b.type for b in message.content], "text": "".join(b.text for b in message.content if b.type == "text")[:500], "usage": message.usage.model_dump()}

            check("普通响应", lambda: summarize(client.messages.create(**request)))

            def streamed(**overrides):
                with client.messages.stream(**{**request, **overrides}) as stream:
                    return summarize(stream.get_final_message())

            check("流式响应", streamed)
            check("thinking流式", lambda: streamed(max_tokens=2048, thinking={"type": "enabled", "budget_tokens": 1024}, messages=[{"role": "user", "content": "Calculate 17*23. Give a short answer."}]))

            def tools():
                definitions = [{"name": "echo", "description": "Echo a short string", "input_schema": {"type": "object", "properties": {"text": {"type": "string"}}, "required": ["text"]}}]
                message = client.messages.create(**{**request, "messages": [{"role": "user", "content": "Use echo with text hello."}]}, tools=definitions, tool_choice={"type": "tool", "name": "echo"})
                calls = [b for b in message.content if b.type == "tool_use"]
                if not calls:
                    raise RuntimeError("模型没有返回要求的工具调用: " + str(summarize(message)))
                history = [{"role": "user", "content": "Use echo with text hello."}, {"role": "assistant", "content": [b.model_dump(exclude_none=True) for b in message.content]}, {"role": "user", "content": [{"type": "tool_result", "tool_use_id": b.id, "content": json.dumps(b.input)} for b in calls]}]
                followup = client.messages.create(**{**request, "messages": history}, tools=definitions, tool_choice={"type": "none"})
                return {"calls": [{"name": b.name, "input": b.input} for b in calls], "followup": summarize(followup)}

            check("工具调用闭环", tools)
            check("输入token计数", lambda: client.messages.count_tokens(model=request["model"], messages=request["messages"]).input_tokens)
    finally:
        process.terminate()
        process.wait(timeout=5)
    if failures:
        raise SystemExit(1)


if __name__ == "__main__":
    main()
