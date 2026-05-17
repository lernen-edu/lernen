# Lernen — Architecture and Philosophy

**Status:** Stable (synchronized with each public release)
**Tagline:** *Learn to think before you prompt.*

This document describes what Lernen is, why it exists, the architecture that produces it, and the things it explicitly will not become. It is the canonical reference for users of Lernen and for anyone considering contributing.

A separate working draft of this document lives in the project's private development repository, where it captures iteration notes and rejected alternatives. This public version is stable: it changes only at release time.

---

## 1. Vision

Lernen is an open-source command-line tool for vibe coders who want genuine fluency in one programming language — the foundation that lets them evaluate AI-generated code critically instead of treating a model as a black box.

The user develops genuine fluency in one programming language of their choice (Python, Go, Rust, Java, Perl, or whichever language has an installed `LanguageAdapter`). The AI tutor is structurally prevented from writing code — it asks Socratic questions, gives hints, critiques work the user has written, and explains concepts. The user produces every line.

The product's promise, in one sentence: *the AI cannot touch your code; you write every line until you can prove you don't need it to.*

That proof is the capability gate — the Lernen capstone. Either the user can build something non-trivial under AI-off conditions, read unfamiliar code fluently, and survive a debugging gauntlet, or they cannot. Time spent and chapters completed are not the signal. The capstone is where Lernen's job ends: a learner who passes it has the foundations to evaluate AI output and the judgment to know when to write code themselves rather than blindly accepting whatever a model produces. How far along that path any individual user goes from there is up to them.

---

## 2. Who Lernen Is For

The project's primary users are the project author and his college-age son. Lernen is designed for people in roughly that range — high school students, community college students, early undergraduates, self-directed learners — who have some technical background but lack the deep fluency that lets them evaluate AI-generated code critically.

Anyone is welcome to use Lernen. The project is open-source and works for users it has never met. But the design target is concrete: two specific people with different starting points and different gaps. Optimizing for hypothetical strangers at the cost of either of them is something the project explicitly resists.

Lernen is not for:

- Institutions buying for departments. Lernen is not a classroom platform.
- Children below high school age.
- Practicing professional engineers learning a new language. Different problem.

---

## 3. The Fluency Mission

### Phase 1 — Fluency

The user develops a working mental model of their chosen language's runtime, idiomatic patterns, standard library fluency, debugging without AI, reading unfamiliar code fluently, testing as a default, and the full toolchain.

**The hard rule:** Lernen's AI in Phase 1 cannot return code longer than three lines. Short snippets for syntax demonstrations are permitted. Anything longer is structurally blocked through both the system prompt and a regex post-processor on AI output. The user writes every line.

Three anti-vibe-coding mechanics, all on by default:

1. **Inverted assistance.** The tutor asks Socratic questions, gives hints, critiques code the user has written, and explains concepts. It does not produce solutions.
2. **Explain-back gates.** Before the tutor will engage on a problem, the user types a 2-3 sentence explanation of what they have tried and what they think the issue is. The gate is evaluated for evidence of attempted reasoning.
3. **AI-off intervals (`lernen practice`).** The harness disables the tutor entirely and assigns a problem from chapters the user has completed. Performance in practice mode is the real competency signal.

Disabling these is possible only via an explicit `--training-wheels-off` flag with a warning. The flag exists for cases where the user has good reason; it is documented but not encouraged.

### The Gate

The gate is the **Lernen capability capstone** — the proof that the learner has the fluency the product set out to build. It is composed of three components, attempted via `lernen gate`:

1. **AI-off build.** Build a small but non-trivial program from scratch under timed conditions, with no AI assistance.
2. **Code comprehension.** Read three unfamiliar code samples drawn from real open-source projects in the user's chosen language, predict their output, and identify at least one bug or design issue per sample.
3. **Debugging gauntlet.** Fix three pre-broken programs of escalating difficulty.

The gate is pass/fail and re-attemptable. There is no checklist, no completion percentage, and no time-based progression. The verdict is intentionally terminal: it is the end of Lernen's job and is consumed by nothing downstream.

---

## 4. Architecture

Lernen is a single static binary written in Go. It runs in your terminal as a TUI built with Bubble Tea. It speaks to inference backends, language adapters, and a documentation provider through small, well-defined interfaces.

### Inference Backends

Lernen never speaks to a model directly; it speaks to a `Backend`. Three backends are supported:

- **Codex CLI** (OpenAI) — via ChatGPT account OAuth
- **Gemini CLI** (Google) — via Google account OAuth, free tier with ~1000 calls/day
- **OpenRouter** — direct API, free models available

Anthropic's Claude is not supported. The project relies on backends whose vendors have not signaled hostility toward third-party harnesses, and Anthropic has done so.

### Language Adapters

A `LanguageAdapter` interface encapsulates everything the harness needs to know about a specific programming language. Adapters are how Lernen supports Python, Go, Rust, Java, Perl, and more without the harness itself being language-specific. v0 ships with one adapter (Python), with the framework designed to accept additional adapters cleanly.

Each adapter knows the language's toolchain, test runner, REPL, formatter, linter, language-specific competency taxonomy, common error patterns, and system prompt addenda for the tutor.

### Documentation Provider

A `DocsProvider` gives the harness reliable, current access to library and language documentation through Context7 (https://context7.com). The runtime tutor uses the DocsProvider to ground responses about library and language-specific topics, dramatically reducing hallucinated APIs. A SQLite cache makes repeat queries fast.

### The Forge

Lernen ships no curriculum content. Instead, the forge — invoked via `lernen forge` — works *with* the user to generate a personalized curriculum manifest from a source the user has legitimate access to (a book, a website, a PDF). The forge is not a setup wizard. It is a pedagogical experience that produces a manifest as a byproduct.

The forge's job is to develop the user's judgment about their own learning while it authors the curriculum. It elicits goals, calibrates the user's actual starting point, recommends a curriculum (and language) with reasoning the user can engage with, ingests the chosen source, and scaffolds chapters one at a time — teaching the user the meta-concepts they need (competency tiers, exercise quality, prerequisite reasoning) before asking them to evaluate any forge proposal. By the end, the user has a manifest *and* a working theory of what their learning will look like.

The forge enforces an originality principle: generated exercise prompts and Socratic templates test the same competencies as source-curriculum exercises but are never paraphrases of them. The user reads the source curriculum directly via URLs the manifest references; Lernen tutors *around* it.

Manifests live on the user's local machine. The project does not host, curate, or recommend community-generated manifests.

### The Tutor

In Phase 1, the tutor is a demanding mentor. Pure Socratic is exhausting; drill instructor alienates; peer pair-programmer undermines rigor. Demanding mentor — asks first, explains when asking has run its course, willing to say "you're not ready for this answer yet, back up" — is the right default. The same voice runs the forge, the runtime tutor, and the gate.

---

## 5. Copyright Posture

Lernen is primarily a personal learning tool. Users working through CC-licensed curricula they have legitimate access to — including ATBS (CC BY-NC-SA 4.0) — is straightforward fair use, no different from a person writing their own study notes.

Two principles:

1. **The project ships no curriculum content.** No exercises, no Socratic templates, no chapter summaries. The repository contains the harness, the forge, and the format specification.

2. **Forge-generated manifests are original content.** The forge's system prompts, enforcement checks, and review steps are designed so that exercise prompts and Socratic templates are original works that test the same competencies as the source curriculum without paraphrasing or reproducing it.

If a user shares a forge-generated manifest publicly, the copyright analysis for that act is the user's responsibility. Manifests that follow the originality principle can be shared under any license the user chooses. Manifests that embed source content (which the forge is designed to prevent) carry whatever obligations the source's license imposes.

This is not legal advice.

---

## 6. License

- Harness code: **AGPLv3** (`LICENSE`)
- Format specification, documentation, examples in the repo: **CC BY-SA 4.0** (`LICENSE-CONTENT`)
- Forge-generated user manifests: licensed by the user as they choose

---

## 7. Anti-Goals

These are things Lernen will explicitly *not* be, even when they would be easy or popular.

- A general AI coding assistant
- A code-completion tool
- A Replit-style integrated environment
- A platform for selling courses
- A platform for credentialing
- A tool that gets users through assignments faster
- A tool that requires a credit card
- A tool that requires an account on a Lernen server
- A tool that uploads user code or conversations to any service the project operates
- A tool that has a "skip" button on the gate
- A central registry, hub, or directory of community-generated manifests
- A forge that produces manifests autonomously without user engagement
- A manifest that systematically reproduces a source curriculum's exercises, examples, or pedagogical sequence
- An institutional platform with classroom features, dashboards, or rosters
- A `LanguageAdapter` that loosens the firewall, the explain-back gate, or the originality principle for "language-specific reasons"
- A `DocsProvider` that sends user code or conversations off-device
- An Anthropic Claude backend (the vendor has signaled hostility toward third-party harnesses)

---

## 8. Success Criteria

Lernen is successful for a given user when they leave with judgment they didn't have before — when they can write, read, evaluate, and direct code rather than passively accept what an AI produces. The capability gate measures the technical foundation; whether the broader shift has happened, and how far it has gone, is the user's own judgment to make.

The project does not measure adoption, retention, or any user-side metric. Lernen is a tool, used or unused; what users do with it is their business.

---

*This document is the stable, public-facing version of Lernen's design. The working draft, decision logs, and implementation plans live in the project's private development repository. Each public release of Lernen is accompanied by a synchronized version of this document.*
