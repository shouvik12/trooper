from openai import OpenAI

client = OpenAI(
    api_key="dummy",
    base_url="http://localhost:3000/v1",
)

messages = []
session_id = "chat-demo"

print("🪖 Trooper Chat")
print("Commands: 'local' = next message stays on your machine, 'quit' = exit")
print("-" * 60)

while True:
    user_input = input("\nYou: ").strip()

    if user_input.lower() == "quit":
        break

    force_local = False
    if user_input.lower() == "local":
        force_local = True
        user_input = input("🔒 (local mode) You: ").strip()

    messages.append({"role": "user", "content": user_input})

    response = client.chat.completions.create(
        model="claude-sonnet-4-6",
        max_tokens=1024,
        messages=messages,
        extra_headers={"X-Session-ID": session_id},
        extra_body={"x_force_local": True} if force_local else {}
    )

    if response.choices:
        reply = response.choices[0].message.content
    else:
        reply = response.content[0]['text']

    messages.append({"role": "assistant", "content": reply})

    provider = "🔒 Ollama (local)" if force_local else "🤖 Claude (cloud)"
    print(f"\n{provider}: {reply}")
