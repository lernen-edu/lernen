# Contributing to Lernen

Thank you for your interest in Lernen. Before doing anything else, please read this document. Lernen has an unusual contribution model and unusual values, and engaging with the project effectively requires understanding both.

## What this repository is, and isn't

This repository (`lernen-edu/lernen`) is the **public release repository**. Every commit here corresponds to a tagged release. Active development happens in a separate private repository where the project owner iterates with agentic coding agents and dogfoods the tool against his own learning process.

Public release repository means:

- You can clone it, build it, install it, run it, fork it
- The code is licensed AGPLv3; you have all the freedoms that license guarantees
- Issue reports for actual bugs in released versions are welcome
- Pull requests can be opened, but see "How contributions work" below before opening one

This is a personal project that the author has chosen to develop in the open. It is not a community project. That distinction matters.

## What Lernen is, and isn't

Read `docs/PRD.md` before considering a contribution. The PRD §"Anti-Goals" lists real things people have asked for that we will not build, including:

- A "skip the gate" feature
- A central registry of community manifests
- An Anthropic Claude backend
- Telemetry
- Web UI surfaces
- Institutional / classroom features
- A `LanguageAdapter` that loosens the firewall, the explain-back gate, or the originality principle

These are not negotiable. Code that adds any of them will be closed.

## How contributions work

There are three reasonable ways to engage with the project as a contributor:

### 1. Fork it

You have all the AGPLv3 freedoms. Fork, modify, distribute under the same license. You don't need anyone's permission. If you build something interesting on top of Lernen, we'd love to hear about it — open an issue with a link.

This is the recommended path for anyone with their own ideas about what a Lernen-shaped tool should do.

### 2. Open a focused pull request

If you've found a real bug in a released version, or have a small, clearly-scoped improvement that respects the PRD's anti-goals, you can open a PR. Be aware:

- The project owner reviews PRs sporadically. Response times vary from days to weeks to "never."
- PRs that conflict with the PRD will be closed without extended discussion.
- PRs adding new features (vs. fixing bugs) are unlikely to be merged unless they were discussed in an issue first and explicitly invited.
- Because public commits correspond to releases, your PR if accepted will be replayed through the private development repository before being mirrored back here. Authorship attribution is preserved in the AGPLv3 sense via Co-Authored-By trailers, not via direct git history.

The TL;DR: fork is faster and more reliable for almost everyone. Use PRs only when you genuinely want this code merged into Lernen rather than your own variant.

### 3. Request collaborator access to the private development repository

If you're seriously interested in the project's direction and want to engage with the private development repository — including the working PRD, the agent constitution, the dogfood notes, and the design discussions — you can ask the project owner for an invitation. Reach out via [GitHub issue with the title "Collaboration request"](https://github.com/lernen-edu/lernen/issues/new) or via the email in the project owner's GitHub profile.

This is selective. The bar is "demonstrated thoughtful engagement with the project's philosophy, not just its code." If you've shipped something meaningful, written something thoughtful about education or AI tooling, or have a track record of substantial open-source work, you're a reasonable candidate. Day-one contributors are not.

## Code style

If you do open a PR:

- Go 1.25+
- Format with `gofmt` and `goimports`
- Lint with `golangci-lint` using the project's `.golangci.yml`
- Public types and functions need doc comments
- Errors are returned, not panicked
- Tests for behavioral changes are not optional — the firewall, the gate evaluator, the originality enforcement, and competency assessment must remain tested

## Things that will get a PR closed

- Adding any feature on the anti-goals list (PRD §12)
- Removing or weakening the Phase 1 firewall without an explicit PRD revision
- Shipping curriculum content in the repo
- Adding telemetry of any kind
- Adding a payment, account, or login surface
- Adding tracking pixels, analytics, or anything that calls home
- Loosening the originality enforcement in the forge
- Bypassing the explain-back gate, the AI-off practice mode, or the gate exam
- Adding a `LanguageAdapter` that violates the firewall or the explain-back gate
- Sending user code or conversations to any service Lernen operates

## Code of Conduct

This project follows the Contributor Covenant 2.1 (`CODE_OF_CONDUCT.md`). The user base may include minors. Conduct yourself in keeping with that.

## License

By contributing, you agree that your contributions to code are licensed under AGPLv3, and your contributions to documentation are licensed under CC BY-SA 4.0.

## A note on AI-generated contributions

Lernen is itself a tool for moving past vibe coding. PRs that are obviously AI-generated without the contributor's understanding will be closed. We don't object to AI-assisted contributions — Lernen is being built with agentic coding agents — but you must understand the code you submit and be able to discuss it. The same standard the tool teaches.

---

*Last updated alongside PRD v0.3.*
