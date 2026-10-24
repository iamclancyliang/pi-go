# Changelog

Notable, user-visible changes. The git history carries the full account; this
file carries what a person upgrading would want to know.

## Unreleased

### The agent
- A `pi` command: one-shot with `-p`, a session in a terminal, and the mode
  resolved from the terminal itself — a redirected run prints, a terminal
  converses.
- `--mode json`: the run as a machine-readable stream. One JSON object per
  line on stdout — a version line first, then every lifecycle event and every
  piece of the reply as it arrives, all numbered by one sequence so a consumer
  can interleave them. The answer is inside the stream; nothing else is
  written there.
- `--mode rpc`: the same stream, with commands read from stdin. Every command
  carries an id and its response echoes it; responses share the stream's one
  sequence, so a client can put a reply back among the events it caused. A
  prompt runs while stdin keeps being read, so `abort`, `steer` and
  `follow_up` act on it; a second prompt during a run is refused as busy.
  The conversation can be read and reshaped over the channel — `get_tree`,
  `get_entries`, `fork`, `clone`, `switch_session`, `new_session`,
  `set_session_name`, `compact` — the model switched with `set_model`, and
  the session inspected with `get_state`, `get_messages`, `get_session_stats`,
  `get_last_assistant_text` and `get_commands`. Models are listed and cycled
  with `get_available_models` and `cycle_model`, shell commands run with
  `bash` and `abort_bash`, and the thinking level is set, cycled and listed
  with `set_thinking_level`, `cycle_thinking_level` and
  `get_available_thinking_levels`; retrying is switched with `set_auto_retry`
  and a wait cut short with `abort_retry`. Twenty-seven of Pi's commands
  answer; the rest fail with a typed reason that says whether they are
  unknown or not yet built.
- A model call that fails for a reason another attempt might survive — a
  throttle, a server fault — is retried, as Pi retries it: three times, two
  seconds doubling, announced as `auto_retry_start` and `auto_retry_end`.
  Every attempt is billed and in the ledger. An exhausted balance and an
  overflow are never retried. `retry.enabled`, `retry.maxRetries` and
  `retry.baseDelayMs` change it.
- Thinking levels reach OpenAI and OpenRouter as well as DeepSeek. A level the
  model does not offer becomes the nearest one it does, and the run says so;
  without a recorded per-model mapping, `xhigh` and `max` land on `high`.
- Seven built-in tools, ported against the pinned Pi source: `read`, `ls`,
  `find`, `grep`, `write`, `edit`, `bash`. Searches honour `.gitignore`; edits
  match against the original file and refuse ambiguity; bash output keeps the
  end, not the beginning.
- `edit` lands when the model's text differs from the file only by trailing
  whitespace, smart quotes, Unicode dashes, special spaces or compatibility
  forms such as fullwidth punctuation. Exact text is still preferred, and
  every line the edit did not touch keeps its original bytes.
- Three providers: DeepSeek, OpenAI and Qwen. A provider is chosen by
  credential when `--provider` is absent, and one call sends one billed
  request.

### Conversations
- Sessions are recorded without being asked, grouped by the directory they ran
  in. `--continue` carries on, `--resume` reopens by id prefix.
- A conversation is a tree: `/tree` shows its shape and goes back to any point;
  `/fork` and `/clone` copy into a new conversation, leaving the original
  alone. What a branch left behind never reaches the model.
- `/compact` summarises the older part into a structured checkpoint — goal,
  decisions, next steps — and the same summariser recovers automatically when
  a request overflows the model's context. An overflow is recognised from any
  provider that words it the way pi has recorded, not only from the providers
  whose own messages were measured. A single long turn — one question and
  many rounds of tool work — can be compacted too: the cut lands inside it,
  and the part dropped is summarised on its own.

### The session
- Slash commands: `/help`, `/session`, `/name`, `/export`, `/import`, `/copy`,
  `/share` (a secret gist, after asking), `/model` to switch mid-session,
  `/login` and `/logout` for stored credentials, `/settings`, `/trust`,
  `/reload`, `/hotkeys`, `/new`, `/resume`, `/quit`.
- An editing prompt in the terminal: Pi's key assignments, a kill ring, undo,
  and history that keeps the half-written line while you browse.

### Configuration
- Settings in two scopes — global and per-project `.pi-go/settings.json` —
  with the project's read only once the project is trusted, because settings
  include the shell every command runs in.
- Credentials saved by `/login` live in `auth.json`, mode 0600, read on
  demand, never printed.
