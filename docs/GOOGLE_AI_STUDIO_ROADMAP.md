# Google AI Studio provider and Free Tier slow mode roadmap

- Status: foundation implemented; owner-key live verification pending
- Branch: `feature/google-ai-studio-slow-mode`
- Prepared: 2026-09-03

## Implementation snapshot

The branch now contains the explicit provider, fixed Google compatibility
transport, native model discovery and token counting, configuration/UI wiring,
and the provider-neutral slow-mode coordinator described below. Slow-mode RPD
state is persisted locally, while RPM/TPM use rolling in-memory reservations
and conservative restart behavior.

Mocked contract, backend, frontend, and regression suites pass. Phase 4 remains
intentionally open until the owner supplies a dedicated key and the exact
RPM/TPM/RPD values shown for the selected model in Google AI Studio. The
repository includes an opt-in bounded live smoke test gated by
`YURI_GOOGLE_AI_STUDIO_LIVE=1`; it is skipped in normal CI and requires the key
and model ID through environment variables rather than source or command-line
arguments.

## 1. Goal

Add Google AI Studio as an explicit inference provider for named Yuri agents,
using an owner-supplied Gemini API key. The provider must work on Free Tier,
preserve Yuri's local tool authorization boundary, expose honest usage and
failure information, and offer a slow mode that coordinates all calls sharing
the same Google project quota.

This integration uses Gemini Developer API quota. It does not use or claim to
use consumer Antigravity, Gemini app, Gemini Code Assist, or Google AI Pro
product quotas. An eligible AI Pro developer credit may offset billed API
usage after the owner applies it to the relevant Cloud billing account, but it
does not change the provider contract.

## 2. Confirmed external constraints

1. Gemini API rate limits are evaluated per Google Cloud project, not per API
   key. Multiple keys and all Yuri agents using the same project therefore
   share the same capacity.
2. The principal inference dimensions are requests per minute (RPM), input
   tokens per minute (TPM), and requests per day (RPD). Some models can add
   other dimensions. RPD resets at midnight Pacific time.
3. Limits vary by model, usage tier, project status, and current capacity.
   Preview/experimental models normally have stricter limits. The authoritative
   active limits are displayed in Google AI Studio; there is no documented
   general API that lets Yuri discover every project's current ceilings.
4. A `429` can mean a short rolling-window rate limit or a daily quota limit.
   Retryable `429`, `408`, and `5xx` responses should use bounded exponential
   backoff with jitter. Authentication and permission failures must not retry.
5. `models.countTokens` can count the input before inference. Final responses
   include provider usage metadata. Exact provider counts are preferred over a
   tokenizer approximation near a quota boundary.
6. Google supports an OpenAI-compatible endpoint and calls it the fastest path
   for platforms that already use a unified OpenAI schema. Google recommends
   direct native APIs when Gemini-specific features are required and requires
   partner/library traffic to identify itself with `x-goog-api-client`.
7. Free Tier availability is model-specific. The provider must use the live
   model catalog and must not label a model as free merely because another
   Gemini model has a free allocation.

Primary references:

- <https://ai.google.dev/gemini-api/docs/rate-limits>
- <https://ai.google.dev/gemini-api/docs/billing>
- <https://ai.google.dev/gemini-api/docs/pricing>
- <https://ai.google.dev/gemini-api/docs/partner-integration>
- <https://ai.google.dev/api/tokens>
- <https://ai.google.dev/gemini-api/docs/troubleshooting>
- <https://ai.google.dev/gemini-api/docs/api-errors>

## 3. Scope and non-goals

### In scope

- Explicit `google-ai-studio` provider kind and UI.
- Owner-supplied Gemini API key stored through the existing keyring boundary.
- Google model discovery and a low-cost connection/capability probe.
- Text, image input, streaming output, Yuri function tools, cancellation,
  provider usage, and normalized errors.
- Per-agent primary/fallback routing through the existing route model.
- `off`, `free-tier`, and `custom` quota-control modes.
- A project-wide slow-mode coordinator covering foreground chat, peer turns,
  subagents, reflection, previews, titles, and scheduled/background work.
- Unit, contract, integration, frontend, and opt-in live smoke tests.

### Not in scope

- Google/Antigravity OAuth cache reuse or consumer subscription quota.
- Shipping a Yuri-owned shared API key.
- Automatically enabling Cloud Billing or spending paid credits.
- Claiming an exact remaining Google quota when Google provides no
  authoritative quota endpoint.
- Google-managed filesystem, code execution, or Search tools in the first
  provider increment. Yuri's existing locally authorized tools remain the
  action boundary.

## 4. Transport decision

The first production increment will use Google's documented OpenAI-compatible
Gemini endpoint behind a small `googleaistudio` adapter/wrapper:

```text
https://generativelanguage.googleapis.com/v1beta/openai/
```

Reasons:

- Yuri already has a hardened OpenAI Chat Completions streaming adapter.
- Its normalized message, image, function-call, usage, timeout, and
  cancellation contracts already match the runtime.
- Google documents this route for platforms prioritizing a unified schema.
- A distinct wrapper still fixes the endpoint, attaches
  `x-goog-api-client: ordoai-yuri/<version>`, implements Google-specific model
  discovery/probing, and parses Google error details without weakening the
  generic adapter.

Before relying on it, a contract spike and the owner's live key must confirm:

- model listing;
- one minimal non-streaming/probe response;
- streaming deltas and terminal usage;
- function call followed by a Yuri tool result;
- image input for a model declaring vision support;
- cancellation and representative invalid-key/model errors.

If the compatibility endpoint cannot preserve any required contract, the
fallback design is a native Gemini Interactions adapter. That is a roadmap
decision gate, not an automatic silent transport switch.

## 5. Provider configuration

Proposed non-secret configuration:

```text
kind                 google-ai-studio
provider_id          owner selected; default google-ai-studio
model                exact Google model id
credential_ref       keyring reference only
quota_mode           off | free-tier | custom
quota_profile        provider/model quota settings
quota_safety_percent default 80
interactive_reserve  default 25 percent of RPD
```

`quota_profile` contains optional RPM, TPM, RPD, and maximum concurrent
inference values. Zero means unknown, never unlimited. In `free-tier` mode,
unknown ceilings must result in a conservative single-flight queue plus a UI
request to copy the current limits from AI Studio. Yuri must not invent an
authoritative quota number.

The UI must distinguish:

- configured key;
- successful provider probe;
- selected model and its observed capabilities;
- API usage tier (`free`, `paid`, or `unknown` when not reliably known);
- configured local pacing envelope;
- observed local usage, which is advisory because the project may be used by
  other keys or applications.

## 6. Slow mode architecture

### 6.1 Placement and quota identity

Introduce a provider-neutral quota admission layer between route resolution
and `ModelBackend.Start`. It wraps the Google backend but remains independent
of HTTP payload encoding.

The limiter is owned by the desktop bridge and shared by every run. Its scope
key is initially `provider_id + model`; its accounting owner is the Google
project behind the API key. Since a key does not safely expose its project id,
the provider id is the local isolation boundary and the UI warns against
configuring the same project as multiple independent providers.

### 6.2 Admission algorithm

For every inference attempt:

1. Assemble the final model request first.
2. Estimate input tokens locally. When the estimate is close to the TPM
   ceiling or the request shape changed materially, call `countTokens` and
   cache the result by bounded request hash.
3. Reserve one RPM unit and the estimated/exact input TPM amount in rolling
   60-second windows.
4. Reserve one RPD unit in a Pacific-date bucket.
5. Admit the request only when all known dimensions have capacity under the
   configured safety percentage.
6. On completion, reconcile the token reservation with provider usage.
7. On a pre-response failure, retain request accounting conservatively unless
   Google returns enough structured information to prove it was not counted.

The rolling window uses timestamped reservations, not a fixed wall-clock
minute, so a burst at a minute boundary cannot exceed the intended envelope.
Waiting is context-cancellable and bounded by the run's existing duration
budget.

### 6.3 Queue policy

Free Tier mode defaults to one active Google inference request. Queue classes:

1. foreground user chat and an already-started tool continuation;
2. owner-triggered preview or peer dialogue;
3. scheduled tasks and delegated subagents;
4. titles, reflection, and autonomous peer work.

Within a class, admission is FIFO. Lower classes use aging to prevent
starvation, but they cannot consume the configured interactive RPD reserve.
Tool continuations keep priority so a partially completed foreground run does
not deadlock behind new work.

When the remaining local RPD envelope reaches the reserve, automatic
background/reflection work pauses until the Pacific reset. Foreground work is
not silently rejected until its own envelope is exhausted.

### 6.4 Prompt and token controls

Slow mode is both a pacer and a quota-saving execution policy:

- intersect the agent preset with a Google slow-mode ceiling; never expand an
  existing hard budget;
- prefer a stable Flash model selected by the owner; never silently replace a
  configured model;
- reduce output allowance for background/reflection/title workloads;
- serialize anonymous subagents and peer turns using this provider;
- keep stable system/personality prefixes ordered consistently to benefit
  from Gemini implicit caching;
- reuse existing bounded context assembly and memory selection;
- if one request itself exceeds the configured TPM ceiling, request bounded
  context compaction and count again;
- preserve immutable policy, identity, the current user turn, required tool
  schemas, and the most recent relevant context during compaction;
- fail with an actionable `context_limit`/quota message if the request still
  cannot fit. Never wait forever for an impossible request.

### 6.5 429 feedback and adaptive cooldown

Parse Google's structured error code/reason and `Retry-After`/retry metadata
when present:

- short-window `rate_limit_exceeded`: freeze the affected scope until the
  provider hint or bounded exponential backoff expires;
- `quota_exceeded`: stop automatic work and mark the daily scope exhausted;
- ambiguous `RESOURCE_EXHAUSTED`: apply a conservative cooldown, surface the
  ambiguity, and require a successful request before gradually returning to
  the configured envelope.

Jitter is required in production and injected as a deterministic dependency
in tests. Automatic retries stop at the existing attempt and run-duration
ceilings. A retry must never create a second visible assistant branch.

### 6.6 Persistence and honesty

- RPM/TPM reservations live in memory; after restart, free-tier mode starts
  with a conservative warm-up rather than assuming an empty remote window.
- RPD usage and quota-exhausted state are persisted by provider/model and
  Pacific date so restarting Yuri cannot intentionally reset its local guard.
- Provider-reported usage remains authoritative for completed runs.
- The UI labels limiter statistics as `local estimate`, because other apps and
  keys can consume the same Google project quota.

## 7. Implementation sequence

### Phase 0 — contracts and decision records

- Update provider ADR and product documentation.
- Add provider/quota configuration contracts with validation and migrations.
- Define quota clock, token counter, ledger, admission, and queue interfaces.
- Specify redaction and API-key rotation behavior.

Exit: pure contract/config tests pass; no network path is enabled.

### Phase 1 — documentation and mocked compatibility proof

- Add the Google adapter wrapper around the existing Chat Completions client.
- Implement fixed endpoint, client identification, safe headers, model
  catalog, provider probe, errors, and usage normalization.
- Prove stream, tools, vision, cancel, and error fixtures with `httptest`.

Exit: no live credential required; all adapter contract tests pass.

### Phase 2 — slow-mode core

- Implement rolling RPM/TPM reservations, Pacific RPD ledger, priorities,
  cancellation, retry feedback, safety margin, and restart warm-up.
- Connect admission to every Google-backed workload, including nested and
  background runs.
- Add token-count cache and exact `countTokens` path near boundaries.

Exit: deterministic clock tests prove no configured ceiling is exceeded under
concurrency, cancellation, retries, nested runs, and day rollover.

### Phase 3 — backend and frontend integration

- Save/test/remove Google provider credentials through Keychain.
- Add onboarding and Settings controls, model selector, slow-mode profile,
  queue state, local quota estimate, and clear Free Tier explanations.
- Make Google available in per-agent primary and fallback routes.

Exit: Wails bridge and React tests cover configuration, validation, routing,
visibility, and failure recovery.

### Phase 4 — owner-key live verification

- Receive the key through a secret-safe mechanism; never paste it into source,
  shell history, chat output, config, or test fixtures.
- Confirm the project/model limits shown in the owner's AI Studio dashboard
  and enter them into the custom/free-tier quota profile.
- Run a bounded smoke matrix: list, minimal response, stream, usage, function
  call, cancel, and optional image.
- Record only redacted model ids, status, capability results, token counts, and
  timestamps. Delete/rotate the test credential if exposure is suspected.

Exit: documented live compatibility result and no credential in repository,
logs, SQLite, or artifacts.

### Phase 5 — regression and release gate

- Run Go unit/integration/race tests and frontend unit/lint/build suites.
- Exercise two agents sharing one Google quota coordinator.
- Exercise Google primary with an explicit non-Google fallback and vice versa.
- Update threat model, user docs, backup/export exclusions, and acceptance
  matrix.

Exit: full test suite green and Free Tier acceptance scenarios documented.

## 8. Test matrix

### Unit tests

- Config rejects custom slow mode without positive limits and impossible
  safety margins.
- Key never appears in config/views/errors/logs.
- Rolling-window math at exact RPM/TPM boundaries.
- Pacific midnight rollover, including DST-independent zone handling.
- Reservation reconciliation and cancelled waiters.
- Priority, FIFO ordering, aging, and interactive RPD reserve.
- 429 short-window versus daily-quota classification.
- Deterministic exponential backoff and jitter ceilings.
- Impossible single request fails instead of waiting forever.

### Provider contract tests

- Model listing and normalization.
- Chat Completions request shape, required client header, and fixed host.
- SSE text and tool-call fragments.
- Terminal usage and malformed/oversized response handling.
- Vision payload and declared capabilities.
- 400/401/403/404/429/5xx normalization and redaction.
- Cancellation before request, during queue wait, and during stream.

### Integration tests

- Two named agents share the same RPM/TPM/RPD coordinator.
- Background reflection pauses while foreground capacity is reserved.
- Tool continuation remains within the same run and keeps priority.
- Restart preserves daily usage and applies warm-up.
- Fallback only occurs before visible output or tool side effect.
- Onboarding completes only after a successful Google probe.

### Frontend tests

- Add/edit/remove Google AI Studio provider.
- Free Tier explanation and unknown-limit warning.
- Custom limit validation and slow-mode queue display.
- Model/capability selection and per-agent routing.
- Rate-limit, daily-quota, invalid-key, and unavailable-model recovery actions.

### Live tests

Live tests are opt-in, bounded, skipped without a credential, and excluded from
normal CI. They must use a dedicated test provider/profile and a token/output
budget small enough to avoid deliberate quota exhaustion.

## 9. Acceptance criteria

1. A Free Tier key can complete a streaming text turn through the production
   bridge and an agent can complete one approved function-tool round trip.
2. All agents using the provider share one quota coordinator.
3. Under a configured test envelope, concurrent tests never exceed RPM, TPM,
   RPD, or concurrency ceilings.
4. Daily exhaustion stops autonomous/background calls and gives foreground
   calls a clear next action and reset basis.
5. A key is present only in Keychain and transient request headers.
6. Google usage and failure metadata are attached to the immutable run route.
7. No consumer Google subscription or Antigravity token/cache is accessed.
8. Existing OpenAI-compatible and Codex provider tests remain green.

## 10. Inputs required from the owner before Phase 4

- A dedicated Gemini API key created for this integration.
- The exact model ids to support first (at least one Free Tier Flash model).
- The active RPM, TPM, and RPD values shown for that project/model in Google AI
  Studio, because those values cannot be reliably inferred from the key.
- Whether the first release should permit paid-tier use with slow mode off, or
  remain fail-closed to Free Tier/custom pacing only.
