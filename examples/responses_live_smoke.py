"""通过本地网关验证原生 Responses SDK 的普通、流式与工具历史。"""
from openai import OpenAI


def main():
    with OpenAI(api_key="local", base_url="http://127.0.0.1:8176/v1", max_retries=0, timeout=60) as client:
        request = dict(model="muse-spark-1.3-contributor", max_output_tokens=2048,
                       reasoning={"effort": "low"}, store=False,
                       include=["reasoning.encrypted_content"],
                       extra_headers={"X-Claude-Code-Session-Id": "responses-live-sdk-check"})
        normal = client.responses.create(**request, input="Write a Python function add(a,b). Only code.")
        assert normal.status == "completed" and normal.output_text
        print("JSON:", normal.status, normal.output_text, flush=True)
        events = list(client.responses.create(**request, input="Write a Python function add(a,b). Only code.", stream=True))
        text = "".join(event.delta for event in events if event.type == "response.output_text.delta")
        assert text and events[-1].type == "response.completed"
        print("SSE:", text, flush=True)
        tools = [{"type": "function", "name": "echo", "description": "Return a short string unchanged.",
                  "parameters": {"type": "object", "properties": {"text": {"type": "string"}}, "required": ["text"]}}]
        history = [{"role": "user", "content": "Call echo with text hello."}]
        result = client.responses.create(**request, input=history, tools=tools, tool_choice="auto")
        calls = [item for item in result.output if item.type == "function_call"]
        assert calls and calls[0].name == "echo"
        history += [item.model_dump(exclude_none=True) for item in result.output]
        history += [{"type": "function_call_output", "call_id": call.call_id, "output": call.arguments} for call in calls]
        followup = client.responses.create(**request, input=history, tools=tools, tool_choice="auto")
        assert followup.status == "completed" and followup.output_text
        print("TOOL HISTORY:", followup.output_text, flush=True)


if __name__ == "__main__":
    main()
