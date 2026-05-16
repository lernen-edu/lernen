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

### Shipped (through v0.3.2)

- **`lernen setup`** — one-time backend configuration. Pick from
  Codex CLI, Gemini CLI, or OpenRouter. Validates the connection and
  persists config under `~/.config/lernen/`.

- **`lernen forge`** — interactive curriculum authoring through all
  five stages, producing a complete published manifest:
  - **Stage 0 — Goals.** Demanding-mentor dialogue eliciting
    `target_capability`, `target_project`, and motivation context.
  - **Stage 1 — Calibration.** Diagnostic dialogue producing
    `current_model`, `gaps`, and `prior_languages`.
  - **Stage 2 — Recommendation.** Language + curriculum recommendation
    grounded in your goals and calibration. (v0.x ships the Python
    `LanguageAdapter`; others follow.)
  - **Stage 3 — Source ingestion.** Slash commands during the mentor
    dialogue — `/paste`, `/url <url>`, `/pdf <path>` — feed a candidate
    chapter list into the conversation. Heuristic extraction handles
    common book layouts (PDF outline trees, HTML semantic lists) with
    LLM fallback for unusual sources.
  - **Stage 4 — Per-chapter scaffolding** *(new in v0.2.0)*. Two-pass
    flow: Pass 1 classifies chapters (content / orientation / deferred);
    Pass 2 co-authors competencies, exercises, and Socratic templates
    per chapter, with a meta-concept teaching moment before each ask.
  - **Stage 5 — Reflection** *(new in v0.2.0)*. Closing dialogue plus
    the structurer-driven publish step that writes the runtime-loadable
    manifest (`curriculum.yaml`, `competencies.yaml`, `chapters/*.yaml`,
    `forge_log.md`) to `~/.local/share/lernen/manifests/<id>/`.
  - Lifecycle flags for managing forge state: `--reset`,
    `--restore=<ts>`, `--list-backups`, `--reset-stage=<name>`.
    *(hardened in v0.3.2)* `--reset-stage` validates the stage name
    *before* backing anything up — an unknown name is rejected with
    the profile left byte-for-byte untouched — and `--list-backups`
    / `--restore` work with no backend configured (offline recovery).

- **`lernen work <curriculum-id>`** — Phase 1 sessions against a real,
  forge-published manifest:
  - **Chapter navigation** *(new in v0.2.0)*. Resumes at the chapter
    you left off. `/next` triggers a mentor-judged completion
    structurer, records the demonstration evidence, and advances state.
    `/chapter <id-or-1-indexed-number-or-prev-or-next>` jumps manually.
    `/progress` shows your status across the curriculum. Progress
    persists at `~/.local/share/lernen/progress/<id>/state.yaml`.
    `--chapter <arg>` flag overrides the resume target for one
    invocation without persisting.
  - **Competency tracking** *(new in v0.3.0)*. `/competency` shows a
    read-only, foundation-first table of how many clean demonstrations
    you've shown for each competency against its gate thresholds, plus
    a gate-readiness summary. Pure derivation from your recorded
    progress — no AI call. Demonstrations now carry an `outcome`
    (progress state is schema v2; older state auto-migrates on load).
  - **Explain-back gate** *(new in v0.3.0)*. Before the tutor engages
    on a stuck-on-a-problem turn, it asks you to say what you tried and
    what you think is wrong — closing the "just paste the error" reflex.
    A genuine concept question passes straight through. The gate fails
    open: if the check itself errors, the tutor engages anyway, never
    blocking you. `--training-wheels-off` disables it (documented
    escape hatch; not encouraged).
  - **Phase 1 firewall.** Code blocks longer than three lines are
    stripped before reaching the screen; Esc cancels mid-stream.
  - **Inline-rendered TUI** *(new in v0.2.0)*. Drag-to-select text and
    mouse-wheel / trackpad scrollback work natively, no modifier keys
    or mode toggles. Matches Claude Code / Codex CLI / Gemini CLI
    conventions.

- **`lernen practice <curriculum-id>`** *(new in v0.3.1)* — AI-off
  practice mode. No tutor: Lernen picks an under-practiced, test-ready
  exercise from a chapter you've completed, drops a workdir with the
  prompt and a pytest scaffold (`solution.py` you edit, plus a
  verbatim `test_exercise.py`), and grades your `/submit` by running
  the real test suite. A clean pass records a practice demonstration
  toward gate-readiness; a failing run is honest history with no
  credit; a broken toolchain records nothing and the session stays
  open to retry. `/docs <lib>` and `/repl` are available; there is no
  model in the loop. Pytest + pytest-json-report are pre-flighted
  before the session starts.

- **`lernen status <curriculum-id>`** *(new in v0.3.1)* — the
  out-of-session twin of `/competency`: prints the foundation-first
  competency table, the gate-readiness summary, and the chapter
  progress table. Pure derivation, no AI, read-only.

- **TUI slash commands.** `/help`, `/copy`, `/quit`, `/competency`,
  plus the chapter navigation commands above. Ctrl+L clears the
  visible screen
  (terminal-native); scroll up in your terminal for prior conversation.

### Not yet shipped (planned)

- **Phase 1 polish.** Smarter practice selection — spaced-repetition /
  difficulty-ramp weak-area drilling (v0.3.1 ships the naive
  under-practiced weighting; `lernen practice` itself is shipped).
- **`lernen gate`** — the build / comprehension / debugging exam
  between Phase 1 and Phase 2.
- **`lernen review` / `lernen exercise`** — Phase 2 commands for
  AI-augmented engineering.
- **More language adapters.** Go, Rust, Java, Perl, etc.

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

Requires Go 1.25+:

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

# 2. Author a curriculum manifest                      [v0.2.0: all 5 stages shipped]
lernen forge

# 3. Phase 1 tutoring: chapter nav + explain-back gate [v0.3.0: shipped]
lernen work <curriculum-id>

# 3b. AI-off practice + out-of-session progress view   [v0.3.1: shipped]
lernen practice <curriculum-id>
lernen status <curriculum-id>

# 4. Capability gate between Phase 1 and Phase 2       [planned]
lernen gate

# 5. Phase 2 review of agentic-CLI-authored code       [planned]
lernen review
```

Steps 1–3b produce a working forge-author, tutor, and AI-off practice
loop through v0.3.1; steps 4–5 are pending. See
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
