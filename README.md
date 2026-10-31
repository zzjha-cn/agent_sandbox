<h1 align="center">sbx</h1>

<p align="center">A controlled execution runtime for Claude Code and Codex.</p>

<p align="center">
  <a href="https://zzjha-cn.github.io/agent_sandbox/"><b>Website & Wiki</b></a> ·
  <b>English</b> · <a href="README.cn.md">简体中文</a> ·
  <a href="docs/walkthrough.md">Walkthrough</a> · <a href="docs/commands.md">Commands</a>
</p>

<p align="center">
  <img alt="Go" src="https://img.shields.io/badge/Go-1.27-00ADD8?logo=go&logoColor=white">
  <img alt="License" src="https://img.shields.io/badge/license-Apache--2.0-blue">
  <img alt="Providers" src="https://img.shields.io/badge/providers-Claude%20%2B%20Codex-brightgreen">
</p>

sbx runs an agent in a Docker sandbox and manages the work around it: preparing inputs, controlling access, recording execution, validating results and delivering artifacts. Use it interactively for code, or submit a structured task from a script or another system.

| Entry point | Use it for | Workspace and completion |
|---|---|---|
| `sbx run <task>` | Interactive coding in a Git repository | A separate worktree and `sbx/<task>` branch; review and merge yourself |
| `sbx run <task> -p "…" --jsonl` | A scripted, headless Git task | Git worktree with machine events; agent success alone does not prove business completion |
| `sbx task run --spec task.json` | Reports, code tasks, tool queries or declared external operations | A file, Git or empty input; explicit outputs, criteria and optional local delivery |
| `sbx run --spec task.json` | Convenience entry for a general task | Same submission path as `sbx task run --spec` |

The Git convenience workflow and general tasks share an execution service. They do not use the same workspace or result semantics. Omitting the name in `sbx run` selects the **main task**, which works directly in the repository root.

## Quick start: a Git task

You need Go 1.27, Git, a running Docker daemon dedicated to one trusted operator, and a supported Claude Code or Codex login or API-key configuration. Mutually untrusted operators need separate daemons or VMs.

```sh
git clone https://github.com/zzjha-cn/agent_sandbox.git
cd agent_sandbox/core
make build
# Add core/bin to your PATH, or invoke the binary by its absolute path.
export PATH="$PWD/bin:$PATH"
sbx --version
```

Inspect daemon ownership, then log in to the provider you intend to use:

```sh
sbx isolation status --json
# Adopt only an unclaimed legacy daemon that you have verified is yours:
# sbx isolation adopt
sbx login codex --status
# If needed, import an existing Codex auth.json or use the login flow:
# sbx login codex --import ~/.codex
# sbx login codex
```

An empty daemon is claimed when sbx first creates resources. Do not remove the ownership marker to bypass a mismatch. Credentials are held in the daemon's shared `sbx-home` volume. `sbx login` without a provider selects Claude.

```sh
cd ~/code/my-project
sbx trust --show                 # inspect project configuration, if present
# Review and run sbx trust when project configuration needs approval.
sbx run --agent codex fix-login
```

Detach from the interactive session with `Ctrl-b`, release, then `d`. The task continues inside its container. Resume it with the same provider, review the worktree, and merge the branch yourself:

```sh
sbx ls
sbx attach fix-login
sbx path fix-login
sbx stop fix-login
sbx run --agent codex fix-login  # resume with the same provider
sbx done fix-login               # remove task runtime/worktree; keep the branch
git merge sbx/fix-login
```

`sbx drop` also removes the task branch and has a separate confirmation. See the [walkthrough](docs/walkthrough.md) and [command guide](docs/commands.md) before using it.

## General tasks: from input to verified delivery

A general task can run outside a Git repository. The operator first registers authorized sources, criteria, tools and sinks in `$SBX_HOME/execution-policy.toml`; a TaskSpec selects those references and can narrow their scope. The task does not grant its own access.

The [general-task guide](docs/general-tasks.md#1-可信注册表与任务声明) provides a complete report example with a file Source, validation, manual review and a local directory Sink. After adapting its paths and saving `report-task.json`:

```sh
sbx version --json
sbx schema task > task.schema.json
sbx admit --spec report-task.json
sbx task run --spec report-task.json
sbx task status monthly-report --json
sbx task logs monthly-report --tail 30
# If the recorded validation requires human review:
sbx task approve --spec report-task.json --reviewer operator-name
```

Admission is read-only and does not reserve resources; run checks again. The agent works in an isolated copy. When outputs are requested, sbx stops the runtime before freezing an artifact snapshot, evaluates it independently and delivers it to an authorized local directory or local Git target. A local Git delivery does not push or merge. A task without file outputs can instead use external operation receipts and business state as evidence.

For automation, stdout contains versioned JSONL events; diagnostics go to stderr. Check the process exit code and the recorded execution, validation and delivery states together. Schemas describe structure; the runtime still performs semantic checks and authorization. See [automation](docs/automation.md) and the [schema catalog](docs/schemas/README.md).

## Tools, external writes and recovery

- **Read-only tools:** supported pinned Claude/Codex versions can call `records.get` through a host gateway. Each call is checked against the current grant, target, fields and limits; credentials stay on the host. This requires synchronous headless execution and a dedicated proxy. See [dynamic tools](docs/dynamic-tools.md).
- **Declared writes:** TaskSpec `operations` declare the record update in advance. The host persists intent and submits it after the agent succeeds and the sandbox stops. A lost or ambiguous receipt remains `unknown`; use `sbx task reconcile --spec task.json` to query it first. `apply` is an explicit action for an operation proven not to have been submitted and still authorized. See [external writes](docs/commands.md#外部写入与核对).
- **Task operations:** `sbx task list`, `status` and `logs` inspect general tasks. `task stop` cancels an active run, `task recover` checks an orphaned execution, and `task clean` removes task-owned runtime resources while retaining the workspace, snapshots, logs and receipts. `task prune` separately removes an eligible artifact snapshot. Run `sbx task <command> --help` for arguments.

A new attempt, a stopped container or runtime cleanup does not prove that an external write did not happen. Keep the original records and reconcile unknown outcomes before any further submission.

## Configuration and boundaries

Personal settings live in `~/.sbx/config.toml`, or in `$SBX_HOME` when set. Git tasks also use project and workspace layers. Lists are generally combined and scalar values overridden; organization ceilings are applied separately and can only restrict the result.

```toml
# ~/.sbx/config.toml
default_agent = "codex"       # claude or codex
profile = "web-go"

[network]
mode = "allowlist"            # default is open
# upstream = "http://host.docker.internal:7890"
```

`sbx config show` reports effective values and their sources; `sbx trust` reviews project configuration. A task network is internal and HTTP(S) uses Squid. Explicit service, forward and port sidecars have their own paths and permissions, so do not interpret the proxy log as a record of every possible connection. The default network mode is **open**; use allowlist and appropriate grants when you need a narrower scope. See [network and host boundaries](docs/boundary.md).

For Git tasks, worktrees and dependency volumes help separate ordinary work, but the repository's Git data and selected shared state are exposed as documented. Task labels and container isolation do not protect against an operator with Docker API access. Review [design and trust boundaries](docs/design.md#7-信任与网络边界) for mounts, Git protection, forwarded services and credentials.

An administrator may install a fixed, root-owned `/etc/sbx/organization.toml` to impose ceilings on network, sources, sinks and tools. Managed execution accepts synchronous TaskSpec; changes or revocation cancel supervised activities and later actions recheck current permissions. Team events are written to a private outbox; `sbx events status` and `sbx events flush` inspect and explicitly export them. A collector and policy distribution are deployment responsibilities. See [organization commands](docs/commands.md#组织策略与事件上报).

## Documentation

| Document | Contents |
|---|---|
| [Website & Wiki](https://zzjha-cn.github.io/agent_sandbox/) | Architecture diagrams and scenarios for personal, team and infrastructure use |
| [Walkthrough](docs/walkthrough.md) | Setup, Git and general tasks, tools, review, delivery and recovery |
| [Design](docs/design.md) | Execution flow, abstractions, trust and deployment boundaries |
| [Commands](docs/commands.md) · [CLI reference](docs/cli-reference.md) | Scenario-oriented command guide and full CLI help |
| [Glossary](docs/glossary.md) | Task, attempt, grant, receipt, recovery and related terms |
| [General tasks](docs/general-tasks.md) · [Dynamic tools](docs/dynamic-tools.md) | TaskSpec, registered resources and read-only tool configuration |
| [Automation](docs/automation.md) · [Schemas](docs/schemas/README.md) | Protocol, status, exit codes and machine-readable contracts |
| [Boundary](docs/boundary.md) · [Publishing](docs/PUBLISHING.md) | Network/host risks and public Wiki export |

## Scope and development

The current scope includes local Docker execution, Claude/Codex, Git sessions, synchronous general tasks, local delivery, controlled read-only tools, declared record writes, recovery and organization ceilings. Remote artifact publishing, a central collector, managed interactive/detached execution, a resident worker and a multi-tenant control plane are not provided. Validate real providers and business connectors in your deployment; controlled fixtures do not establish compatibility with arbitrary production APIs.

```sh
cd core
make test          # unit tests
make test-docker   # Docker integration tests
make lint          # golangci-lint, or vet and formatting checks
make e2e           # Claude end-to-end flow; requires login
```

For release archives, use `make release` from `core`. To export only the public Wiki files for static hosting, see [publishing](docs/PUBLISHING.md). GitHub Pages configured to publish `docs/` directly does not require the export command; ensure everything pushed in that directory is intended to be public.

## License

[Apache-2.0](LICENSE).
