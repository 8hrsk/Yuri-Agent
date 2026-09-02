[Русская версия](docs/README.ru.md)

<p align="center">
  <img src="frontend/src/assets/yuri-app-icon.png" width="128" alt="Yuri Agent icon">
</p>

<h1 align="center">Yuri Agent</h1>

<p align="center">
  A local-first desktop AI companion with persistent memory, evolving personality, explicit permissions, and multi-agent collaboration.
</p>

Yuri is a personal AI workspace designed for a single owner. It combines a desktop chat experience with durable local memory, configurable agent identities, background tasks, voice interaction, plugins, and controlled collaboration between multiple named agents.

The project is macOS-first. Its domain and application layers are kept portable so Windows and Linux support can be added later.

## Project idea

Most AI chat applications treat every conversation as an isolated session or keep the important state on a remote service. Yuri takes a different approach:

- SQLite is the authoritative local store for conversations, memories, agent state, schedules, and audit history.
- Each named agent has its own model route, identity seed, mutable personality, emotional state, relationships, and private memories.
- The model cannot silently grant itself permissions or rewrite the owner's identity and security rules.
- File changes and other sensitive side effects pass through explicit policy and approval boundaries.
- Conversations may produce durable memories, while hiding a conversation does not delete those memories or its transcript.
- Agents can collaborate through bounded, auditable peer dialogues without exposing each other's private context.

Yuri is built with Go, Wails, React, TypeScript, and SQLite.

## Current capabilities

- Streaming chat through OpenAI-compatible providers and the Codex App Server.
- Per-agent provider, model, fallback route, and execution budget.
- Persistent multi-agent memory with private and explicitly shared scopes.
- Versioned personality, relationship, affect, and owner-authored personalization data.
- Voice input through compatible speech-to-text providers and interruptible system text-to-speech.
- Permission-gated filesystem tools, bounded web access, and redacted audit records.
- Durable schedules, background runs, proactive activity, and notifications.
- Out-of-process plugins with manifest validation, capability grants, checksums, and lifecycle management.
- Local archive search and encrypted portable agent export/import.

See the [product specification](docs/PRODUCT_SPEC.md), [architecture](docs/ARCHITECTURE.md), and [threat model](docs/THREAT_MODEL.md) for the complete design and security boundaries.

## Install from Releases

Prebuilt packages, when available, are published on the [GitHub Releases page](https://github.com/8hrsk/Yuri-Agent/releases).

1. Download the latest macOS archive and its `.sha256` file.
2. Verify the archive before opening it:

   ```bash
   shasum -a 256 -c yuri-<version>-macos-universal.zip.sha256
   ```

3. Unzip the archive and move `yuri.app` to `Applications`.
4. Open Yuri and complete the onboarding flow to create an agent and connect a supported model provider.

The current open-source macOS build targets macOS 11 or newer and contains both Apple Silicon and Intel architectures. Unless a release explicitly says otherwise, community artifacts are not signed or notarized. Review the release notes and checksum before running them. More details are available in the [macOS release guide](docs/MACOS_RELEASE.md).

## Build from source

### Requirements

- macOS 11 or newer
- Xcode Command Line Tools
- Go 1.25
- Node.js 22 and npm
- GNU Make

Clone and verify the project:

```bash
git clone https://github.com/8hrsk/Yuri-Agent.git
cd Yuri-Agent
npm --prefix frontend ci
make check
```

Build the desktop application:

```bash
make macos-build
open cmd/yuri/build/bin/yuri.app
```

`make macos-build` installs the pinned Wails CLI into the repository-local `.tools` directory and produces a universal macOS application. To create and verify the distributable archive as well, run:

```bash
make macos-smoke YURI_VERSION=0.7.0
```

The archive and checksum are written to `dist/macos/`.

For development with hot reload:

```bash
make wails-install
cd cmd/yuri
../../.tools/wails dev
```

## Provider setup

On first launch, create an agent and configure one of the supported routes in the onboarding flow or Settings. Provider credentials are handled by the backend and must not be committed to the repository. On macOS, secrets are stored through the system keyring boundary rather than in the main SQLite database.

## Development and testing

Run the standard checks with:

```bash
npm --prefix frontend ci
make check
make mvp-smoke
```

Architecture decisions are recorded under [`docs/adr/`](docs/adr/). Implementation-oriented documentation starts at [`docs/implementation/README.md`](docs/implementation/README.md).

## Contributing

Read [CONTRIBUTING.md](CONTRIBUTING.md) before opening an issue or pull request. Changes affecting storage, security policy, permissions, providers, plugins, or context assembly should include appropriate tests and an architecture decision when a trust boundary changes.

## License

Yuri core and the Plugin SDK are available under the [Apache License 2.0](LICENSE). Third-party dependencies remain subject to their respective licenses and notices.
