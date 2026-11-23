"""连接已启动的本地网关，验证实际 OpenAI SDK 与真实上游。"""
from openai import OpenAI


def main():
    with OpenAI(api_key="local", base_url="http://127.0.0.1:8176/v1", max_retries=0, timeout=60) as client:
        request = dict(model="muse-spark-1.3-contributor", max_completion_tokens=2048, reasoning_effort="low", messages=[{"role": "user", "content": "Write a Python function add(a,b). Only code."}], extra_headers={"X-Claude-Code-Session-Id": "chat-live-sdk-check"})
        normal = client.chat.completions.create(**request)
        assert normal.choices[0].message.content
        print("JSON:", normal.choices[0].finish_reason, normal.choices[0].message.content, flush=True)
        chunks = list(client.chat.completions.create(**request, stream=True, stream_options={"include_usage": True}))
        text = "".join(chunk.choices[0].delta.content or "" for chunk in chunks if chunk.choices)
        assert text and chunks[-1].choices == [] and chunks[-1].usage
        print("SSE:", text, "usage:", chunks[-1].usage.total_tokens, flush=True)
        tools = [{"type": "function", "function": {"name": "echo", "description": "Return a short string unchanged.", "parameters": {"type": "object", "properties": {"text": {"type": "string"}}, "required": ["text"]}}}]
        messages = [{"role": "user", "content": "Call echo with text hello."}]
        response = client.chat.completions.create(**{**request, "messages": messages}, tools=tools, tool_choice="auto")
        assistant = response.choices[0].message
        assert assistant.tool_calls and assistant.tool_calls[0].function.name == "echo"
        history = messages + [assistant.model_dump(exclude_none=True)] + [{"role": "tool", "tool_call_id": call.id, "content": call.function.arguments} for call in assistant.tool_calls]
        followup = client.chat.completions.create(**{**request, "messages": history}, tools=tools, tool_choice="auto")
        assert followup.choices[0].message.content
        print("TOOL HISTORY:", followup.choices[0].finish_reason, followup.choices[0].message.content, flush=True)
        streamed_calls = {}
        for chunk in client.chat.completions.create(**{**request, "messages": messages}, tools=tools, tool_choice="auto", stream=True):
            for choice in chunk.choices:
                for call in choice.delta.tool_calls or []:
                    value = streamed_calls.setdefault(call.index, {"id": "", "name": "", "arguments": ""})
                    value["id"] += call.id or ""
                    if call.function:
                        value["name"] += call.function.name or ""
                        value["arguments"] += call.function.arguments or ""
        assert streamed_calls and streamed_calls[0]["name"] == "echo"
        import json
        assert json.loads(streamed_calls[0]["arguments"])["text"] == "hello"
        print("SSE TOOL:", streamed_calls, flush=True)


if __name__ == "__main__":
    main()
