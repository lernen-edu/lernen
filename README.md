# Lernen

**Learn to think before you prompt.**

Lernen is an open-source command-line tool for vibe coders who want to move toward using AI as an engineering tool rather than a black box. It does this in two phases:

In **Phase 1**, you build genuine fluency in one programming language of your choice. Lernen's AI tutor is structurally prevented from writing code — it asks Socratic questions, gives hints, critiques code you've written, and explains concepts. You write every line.

In **Phase 2**, you learn to direct, evaluate, and threat-model real agentic coding tools (Codex CLI, Gemini CLI, and others). Lernen becomes a critic and observer rather than the puppet master.

Between the phases is a capability gate. You either pass, or you don't.

## Why does this exist?

Most "learn to code with AI" tools are vibe-coding accelerators. They get you to working code faster, at the cost of genuinely understanding it. Lernen exists for people who would rather take longer and end up actually competent.

The primary users are the project author and his son. If you find Lernen useful, you're welcome to it.

## Current status

Lernen is in active development. Pre-1.0 releases publish the project
as it gets built, milestone by milestone. Installing a release gets you
everything that has shipped through that tag — no more, no less. The
PRD (`docs/PRD.md`) describes the full system; this section tells you
what is **actually working today**.

### Shipped (v0.1.0)

- `lernen setup` — one-time backend configuration. Pick from Codex CLI,
  Gemini CLI, or OpenRouter. Validates the connection and persists
  config under `~/.config/lernen/`.
- `lernen forge` — interactive curriculum authoring through Stages 0–3:
  - **Stage 0 — Goals.** Demanding-mentor dialogue eliciting
    `target_capability`, `target_project`, and motivation context.
  - **Stage 1 — Calibration.** Diagnostic dialogue producing
    `current_model`, `gaps`, and `prior_languages`.
  - **Stage 2 — Recommendation.** Language + curriculum recommendation
    grounded in your goals and calibration. (v0.1.0 only ships the
    Python `LanguageAdapter`.)
  - **Stage 3 — Source ingestion.** Three slash commands during the
    mentor dialogue — `/paste`, `/url <url>`, `/pdf <path>` — feed a
    candidate chapter list into the conversation. Heuristic extraction
    handles common book layouts (PDF outline trees, HTML semantic
    lists) with LLM fallback for unusual sources. The mentor proactively
    triages frontmatter; you correct misfires in plain language.
  - All four flags for managing forge state: `--reset`, `--restore=<ts>`,
    `--list-backups`, `--reset-stage=<name>`.
- `lernen work` — Phase 1 walking-skeleton TUI. Streams tutor responses
  with the Phase 1 firewall active (code blocks longer than three lines
  are stripped). Esc cancels mid-stream. Currently runs against a tiny
  built-in `hello-print` fixture — useful for verifying the firewall
  and stream pipeline, **not yet** a real learning experience because
  Stage 4 (per-chapter scaffolding) hasn't shipped.
- `/select` toggle for click-drag text selection in the TUI; full slash
  command surface (`/help`, `/clear`, `/history`, `/copy`, `/quit`,
  `/select`).

### Not yet shipped (planned in upcoming releases)

- **Stage 4 — Per-chapter scaffolding** (the heart of the forge).
  Co-authors competencies, exercises, and Socratic templates with the
  user, with a meta-concept teaching moment before each ask. Until this
  ships, `lernen work` against your forge-generated manifest has no
  scaffolded chapters to walk through.
- **Stage 5 — Reflection.** Closing dialogue that walks the user
  through what they've built.
- **Phase 1 mechanics on real manifests.** Explain-back gate, AI-off
  practice mode (`lernen practice`), competency tracker.
- **`lernen gate`** — the build/comprehension/debugging exam between
  Phase 1 and Phase 2.
- **`lernen review` / `lernen exercise`** — Phase 2 commands for
  AI-augmented engineering.
- **More language adapters.** Go, Rust, Java, Perl, etc. — v0.x.

If you install v0.1.0 today, you can run through Stages 0–3 of the
forge to produce a curriculum manifest skeleton (`goals.yaml`,
`starting_point.yaml`, `recommendation.yaml`, `ingestion.yaml`), but
you won't be able to use it for actual tutoring until Stage 4 ships.

## Install

### Prebuilt binaries (recommended)

Download the latest release from [GitHub Releases](https://github.com/lernen-edu/lernen/releases/latest):

- macOS (Apple Silicon): `lernen_<version>_darwin_arm64.tar.gz`
- macOS (Intel): `lernen_<version>_darwin_amd64.tar.gz`
- Linux (x86_64): `lernen_<version>_linux_amd64.tar.gz`
- Linux (ARM64): `lernen_<version>_linux_arm64.tar.gz`
- Windows: `lernen_<version>_windows_amd64.zip`

Verify the checksum against `checksums.txt` before running. Extract, move `lernen` somewhere on your `PATH` (e.g., `/usr/local/bin`), and run `lernen setup`.

### From source

Requires Go 1.22+:

```sh
go install github.com/lernen-edu/lernen/cmd/lernen@latest
```

### Build it yourself

```sh
git clone https://github.com/lernen-edu/lernen.git
cd lernen
make build
./lernen --help
```

## How it works

Lernen runs in your terminal as a TUI built with Bubble Tea. You point it at a curriculum source you have legitimate access to (a book, a website, a PDF) and it works *with* you — not autonomously — to generate a personalized curriculum manifest. Then it tutors you through that manifest, refusing to write code for you while you build genuine fluency.

The harness supports any programming language for the learner via a `LanguageAdapter` framework. v0 ships with a Python adapter; Go, Rust, Java, Perl, and others come in later versions.

## Backends

Lernen works with three inference backends:

- **Codex CLI** (OpenAI) — via ChatGPT account OAuth
- **Gemini CLI** (Google) — via Google account OAuth, free tier with ~1000 calls/day
- **OpenRouter** — direct API, free models available

Anthropic's Claude is *not* a supported backend. See `docs/PRD.md` for the reasoning.

Lernen also integrates with [Context7](https://context7.com) for up-to-date library documentation, dramatically reducing the rate at which the tutor hallucinates library APIs.

## Quick start

```sh
# 1. Configure your inference backend                  [v0.1.0: shipped]
lernen setup

# 2. Author a curriculum manifest skeleton             [v0.1.0: Stages 0–3 shipped]
lernen forge

# 3. Phase 1 against a forge-generated manifest        [planned: needs Stage 4]
lernen work <curriculum-id>

# 4. Capability gate between Phase 1 and Phase 2       [planned]
lernen gate

# 5. Phase 2 review of agentic-CLI-authored code       [planned]
lernen review
```

In v0.1.0 only steps 1 and 2 produce useful output; see
[Current status](#current-status) above for the full breakdown.

See `docs/PRD.md` for the full architecture and pedagogical philosophy.

## Repository layout

This repository contains the source code of each tagged release. Active development happens in a separate private repository where the project owner iterates with agentic coding agents. Each release is a snapshot of stable, tested code mirrored from the development repo.

If you're a user: install a release binary or build from source. The README and the PRD have everything you need.

If you're a developer interested in contributing: see `CONTRIBUTING.md` for the contribution model.

## License

- Code: AGPLv3 (see `LICENSE`)
- Documentation and format specification: CC BY-SA 4.0 (see `LICENSE-CONTENT`)
- User-generated curriculum manifests: licensed by the user

## Code of Conduct

We follow the Contributor Covenant 2.1 (see `CODE_OF_CONDUCT.md`). The user base may include minors; please conduct yourself accordingly.

---

*Lernen is in active development. The architecture and design philosophy live in `docs/PRD.md`.*
