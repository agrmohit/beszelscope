# beszelscope

A read-only [MCP](https://modelcontextprotocol.io) server for a [Beszel](https://beszel.dev) hub. It lets an AI assistant check server health, current metrics and trends.

[![AI Assisted](https://www.aihonestybadge.com/badges/ai-assisted-small.svg)](https://www.aihonestybadge.com)
[![License](https://img.shields.io/badge/license-MIT-informational)](LICENSE)

> [!NOTE]
> Work in progress. Setup steps, tools and the token workflow will be documented here before the first release.

## Development

Run `make` to list the targets. `make check` is the gate: it runs tidy, lint, race tests, govulncheck and gitleaks.
