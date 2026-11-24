# LLM Chat changelog

Changes to `ext/llm-chat`, newest first. A user-visible change adds its line under `## Unreleased` in the same
commit. Each PocketOne release raises this extension's `version:` when there's something under `## Unreleased` and
renames the section to that version ([Changelogs](../../docs/extensions.md#changelogs)).

## Unreleased

## 0.1.0 (2026-11-24)

### Upgrading

- Replace `$agent.newChat()` with `$llm.chat.new()`, `$agent.chat(id, opts)` with `$llm.chat.open(id, { model: "<provider>/<model>", ...opts })` and `send` with `await sendAsync` (sending is async only), and `$agent.chats.list/forget` with `$llm.chat.list/forget`. The collections are now `_llm_chat_chats` and `_llm_chat_messages`; chats in the old `_agent_chat_*` collections aren't moved. `wait` is gone.

### Added

- `enrich` on a chat's options or a send (llm.chat/v1): the best passages from the named search buckets go after the question and are saved with it, so later turns send the same thing; the answer's `passages` lists them.

### Changed

- Agent Chat is now LLM Chat (`ext/llm-chat`, contract `llm.chat/v1`), answered by the LLM extension's `llm.text/v1`. In page code: `$llm.chat.new()`, `await $llm.chat.open(id, { model }).sendAsync(text)`, `$llm.chat.list()`, `$llm.chat.forget()`. Every send names its `model` (on the chat or the send) and may set `reasoning`; summaries use the same model with reasoning off, and a failed summary no longer fails the send.

## 0.0.2 (2026-11-18)

### Added

- New standard extension: stored conversations with the app's language model (agent.chat/v1). In `pb_hooks`: `$agent.newChat()`, `$agent.chat(id).send(text)` (and `.async`, and streaming with `onText`), `history()`, `title()`, `meta()`, `forget()`, `$agent.chats.list({meta})` and `$agent.chats.forget({meta})`. Chats aren't tied to users; the app's own `meta` finds them. Older messages fold into a summary, and the built-in model keeps each chat's state between turns.

