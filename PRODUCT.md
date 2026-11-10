# Product

<!-- impeccable:product-schema 1 -->

## Platform

web

## Stack

React, TypeScript, Vite, Tailwind CSS v4 and `@base-ui/react` (unstyled accessible primitives), embedded in a single Go binary. CodeMirror 6 for the SQL editor. Decided in `plans/PLAN.md`.

## Users

Backend engineers on small engineering teams at startups and scale-ups. They open dbairlock a few times a day, often in the middle of an incident or a support ticket: to submit a data fix, or to approve a change written by a teammate or by an AI agent working on their behalf. One admin per instance configures sign-in, connections and settings.

AI agents are a second kind of author. They reach dbairlock over MCP, never see row data, and cannot approve anything. Every change they submit is reviewed by a human in the dashboard.

## Product Purpose

dbairlock replaces running `UPDATE` statements by hand against production. A change is submitted, a teammate reviews exactly which rows it will touch, and the server then executes it with drift guards. Nobody on the team holds write credentials. Success means a reviewer can trust, in seconds, that what they approve is exactly what will run, and that every step is in a tamper-evident audit log.

## Positioning

Review is for exact rows, not for code: the reviewer sees the before and after image of every affected row, an approval needs a preview taken within the last few minutes, and execution is rolled back if the matching rows or overwritten values have drifted. It is built so AI agents can propose changes safely while humans keep the approval.

## Operating Context

- Self-hosted inside the company VPC as one container with one volume, reached over VPN or an internal load balancer.
- Configured entirely in the browser: a first-run wizard sets up Google sign-in, and everything else lives in Settings.
- Core loop: write SQL in the editor (dialect highlighting, schema autocomplete, inline server validation), get a preview, submit, review in the queue, execute, audit.
- Targets: Postgres and MySQL connections, each with its own review policy.

## Capabilities and Constraints

- Single-table `INSERT`, `UPDATE`, `DELETE` only. No DDL, no arbitrary `SELECT`.
- Two roles in v1: admin and member. The author can never review their own change.
- Secrets are write-only: the UI shows whether a secret is set, by whom and when, but never its value.
- Change statuses: draft, pending_review, approved, executing, executed, failed, rejected, expired, cancelled.
- SQL, titles and reasons are always rendered as plain text; agent-written content is never interpreted as HTML or markdown.
- Row previews are visible to every signed-in user in v1 (accepted risk, disclosed on the add-connection form).

## Brand Commitments

Name only: `dbairlock`. No existing logo, colors or type. Visual identity is to be designed from scratch. The owner's direction: sleek and modern, and unmistakably a developer tool.

## Evidence on Hand

No customers, testimonials or metrics exist. Do not invent any.

## Product Principles

1. Exactness over summary: show the actual rows, counts and SQL, never a paraphrase.
2. Fail closed, and say why: every refusal names its reason in plain language.
3. Fast for the in-a-hurry engineer: the common path (open a change, check rows, approve) takes seconds and works from the keyboard.
4. Humans approve; agents propose. The UI makes authorship (person or agent) unmistakable.
5. Trust is visible: audit, approvals, freshness of the preview and secret status are always shown, never hidden behind a click.

## Accessibility & Inclusion

Usable by keyboard alone at 1280 px and 375 px widths, in light and dark themes.
