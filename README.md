<h1 align="center">sbx</h1>

<p align="center">
  Run Claude Code in a Docker sandbox — skip the permission prompts without betting your machine on it.
</p>

<p align="center">
  <a href="https://zzjha-cn.github.io/agent_sandbox/"><b>Website</b></a> ·
  <b>English</b> ·
  <a href="README.cn.md">简体中文</a>
</p>

<p align="center">
  <img alt="Go" src="https://img.shields.io/badge/Go-1.27-00ADD8?logo=go&logoColor=white">
  <img alt="License" src="https://img.shields.io/badge/license-Apache--2.0-blue">
  <img alt="Status" src="https://img.shields.io/badge/status-M1%20%2B%20M2%20done-brightgreen">
</p>

---

<p align="left">
  <b>Overview: </b> <a href="https://zzjha-cn.github.io/agent_sandbox/"><b> Website </b></a>
</p>


A coding agent only gets out of your way once you turn the permission prompts off — and the moment you do, it can touch anything on your machine. sbx moves that trade-off somewhere safer: **one container, one git worktree and one branch per task**. Inside, `--dangerously-skip-permissions` is fine. Outside, nothing changed.

You also get tasks that **actually run in parallel** without stepping on each other, dependency installs that never touch your host, and every outbound request through a proxy — so you can **ask afterwards what it talked to**.

```console
$ cd ~/code/shop
$ sbx run fix-login          # branch + container + Claude Code, in one command
$ sbx ls
workspace shop-e76272 (/Users/you/code/shop)
TASK        STATUS   BRANCH          AHEAD  DIFF         LAST-ACTIVE  PATH
add-search  idle     sbx/add-search  2      7f +210 -14  12m ago      ~/.sbx/worktrees/shop-e76272/add-search
fix-login   running  sbx/fix-login   1      3f +48 -6    8s ago       ~/.sbx/worktrees/shop-e76272/fix-login
```

`Ctrl-b`, release, then `d` leaves the session with the agent still running. Come back with `sbx attach fix-login`; when it's done, `sbx done` and `git merge`.

## What it solves

| Problem | How sbx handles it |
|---|---|
| You want the prompts off, but not an agent loose on your filesystem | It only ever sees one worktree and its own container. The rest of your host is never mounted |
| Two tasks editing the same repo collide | Each task gets its own git worktree and `sbx/<task>` branch — physically separate |
| Agent-installed dependencies pollute your local environment | `node_modules` and friends are masked by volumes; invisible in both directions |
| It ran all night and you have no idea what it did or where it connected | State comes from Claude hooks (`sbx ls`); all egress goes through Squid (`sbx net denied`) |
| Closing the terminal kills the agent | The agent runs in tmux *inside* the container — survives closed terminals and host reboots |

## Quick start

**Prerequisites**: Docker Desktop running (10GB of memory recommended), a Claude subscription. Building from source also needs Go 1.27.

### 1. Install

From a release archive (`sbx` is a single binary that only shells out to `docker` and `git`):

```bash
tar -xzf sbx_<version>_<os>_<arch>.tar.gz
sudo install -m 0755 sbx /usr/local/bin/sbx
sbx --version
```

Or from source:

```bash
cd core
make build
ln -sf "$PWD/bin/sbx" /usr/local/bin/sbx   # optional
make release                               # cross-compile into dist/ for darwin+linux, amd64+arm64
```

### 2. Configure (only if you need a host proxy)

`~/.sbx/config.toml`:

```toml
[network]
upstream = "http://host.docker.internal:7890"   # leave empty for a direct connection
```

### 3. Log in (once; every task shares it)

```bash
sbx login            # opens claude's login flow in a throwaway container
sbx login --status   # which account is this?
```

Open the link it prints, paste the code back. Credentials live in the `sbx-home` volume, shared by every task and never inside one. `sbx login --logout` clears them; `--force` switches accounts.

### 4. Run your first task

```bash
cd ~/code/shop
sbx run fix-login
```

That's it. Worktree, branch, container, network and proxy credentials are all set up, and Claude Code is waiting inside.

## The workflow

```bash
sbx run fix-login              # start (running it again resumes, continuing the last conversation)
sbx run nightly -p "fix the failing tests"   # headless: run a prompt, stop the container when done
sbx logs nightly -f            # follow a headless run's output
sbx run add-search --detach    # queue up another one without entering it
sbx ls                         # who's running, commits ahead, diff size, time since last activity
sbx attach fix-login           # back into the session (Ctrl-b, release, d to leave)
code "$(sbx path fix-login)"   # open what it changed in your editor
sbx done fix-login             # tear down the container, keep the branch
git merge sbx/fix-login        # merge it yourself — sbx never merges for you
```

**`Ctrl-D` exits Claude; it does not detach.** The tmux session survives it, though, so `sbx run` brings Claude back and continues the previous conversation. A slip of the finger costs nothing.

Want the changes to land in your repo directory, visible to `git status` on the host right away? **Omit the task name** — `sbx run` uses the main task, which works on the repo root with no worktree and no branch. Trade-offs and when to use it: [docs](docs/commands.md#我想直接在仓库目录里干活main-task).

## The model

| | What it is |
|---|---|
| **Workspace** | A git repository, detected from your current directory |
| **Task** | One line of work = one container + one worktree + one `sbx/<task>` branch |
| **main task** | The special case when you omit the name: works on the repo root, no isolation |

All a container can see is: its worktree, your repository's `.git` directory, the shared `sbx-home` (login state and Claude config), `sbx-cache` (package-manager caches) and a read-only `/sbx/gen` (hooks, settings, status line). **Nothing else from your host is mounted.**

> **Known gap — `.git` is mounted writable, and that is an escape hatch.** It has to be: writing to your real `.git` is what lets a commit made inside the sandbox show up in `git log sbx/<task>` on your host with no syncing step. The cost is that an agent can also write `.git/hooks/*` or `.git/config`, and git hooks run **as you, on your host**, the next time you touch that repo. sbx does not guard against this today — it is risk R1 in [design.md](docs/design.md), accepted knowingly and still open. Isolation holds against everything else (network, filesystem, credentials), but if you are running a genuinely untrusted agent or codebase, do it in a throwaway clone rather than your working repo.

## Networking

All egress goes through a Squid proxy — by default one shared instance (`sbx-proxy`) for every task — which is **open by default**.

```bash
sbx net denied                     # what got blocked (--all for every category, --since 2h to narrow)
sbx net allow fastdl.mongodb.org   # allow a host and hot-reload it into running tasks
sbx run t1 --net allowlist         # allowlist mode, just for this run
sbx run t1 --proxy dedicated       # give this task its own proxy sidecar
```

Allowlist mode (`network.mode = "allowlist"`) permits only the built-in list plus whatever you configure — the right choice when running untrusted code. Both modes go through Squid, so the logs are always there, and the cloud-MCP policy block applies either way ([ADR 0015](docs/CONTEXT.md)).

> Flipping the default from allowlist to open was a deliberate call on 2026-10-08. When the allowlist blocked web search or a dependency download, the agent would quietly substitute something else and report a passing test — an outcome worse than the block itself. The trade-off is recorded in the ADR 0005 revision.

## Configuration

`~/.sbx/config.toml`; every field is optional:

```toml
profile = "web-go"            # built-in profile: web-go | py-rust
# image = "ghcr.io/me/dev:1"  # or bring your own image, overriding profile

[network]
upstream  = ""                # host proxy; empty means direct
mode      = "open"            # open | allowlist
allow     = []                # appended to the built-in allowlist

[resources]
cpus = 2 ; memory = "3g" ; pids = 1024

[deps]
mask = ["node_modules"]       # each masked by a volume, keeping your host clean
```

Configuration has four layers, each overriding the last — lists are unioned, scalars replaced: built-in defaults → `~/.sbx/config.toml` → `<repo>/.sbx/sandbox.toml` (committed, shared with your team) → `~/.sbx/workspaces/<ws>.toml` (your own override for this one repo).

```bash
sbx config show    # every effective value and which layer it came from
sbx trust          # review this repo's .sbx/ and record it as trusted
```

The project layer travels with the repo, so **secret-looking keys and absolute paths are rejected outright in that layer**. And because a `git pull` can change it under you, `sbx run` stops and shows you the diff whenever `.sbx/` differs from what you last confirmed — repos without a `.sbx/` directory never see any of this. Full field list and when changes take effect: [docs/commands.md](docs/commands.md#配置四层adr-00090010design-91).

## Bringing your own image

The built-in profiles are a convenience, not a requirement. There are three ways to decide what a task runs in, highest priority first:

```toml
image = "ghcr.io/me/devbox:2026-10"   # 1. a ready-made image
```
```bash
<repo>/.sbx/Dockerfile                # 2. a Dockerfile in the repo — no config needed
profile = "web-go"                    # 3. a built-in profile (the default)
```

Whichever you pick, **you only supply the language environment**. sbx layers its own agent layer on top — tini, tmux, git, ripgrep, Node, claude-code, mise, and an `agent` user whose UID matches yours — so you never install the agent yourself. The image tag is a hash of its inputs, so changing your base rebuilds automatically.

Two things to know before you build one:

- **The base has to be Debian or Ubuntu.** The agent layer installs with `apt-get`. An Alpine or RHEL base fails during build with an apt error rather than a message from sbx.
- **Dependency masking still follows `profile`, not your image.** The defaults are keyed off the `profile` field, which a custom `image` does not change — so an image you built for Rust still inherits `web-go`'s masks. And because lists are unioned across layers, `mask` only ever *adds*; you cannot remove an inherited entry. Setting `image` plus `mask = ["target"]` leaves you with `node_modules`, `.next` **and** `target`, and the two you did not want get created as empty mount points in your worktree. Set `profile` to whichever built-in is closest, then add on top:

  ```toml
  image   = "ghcr.io/me/rustbox:1"
  profile = "py-rust"           # masks .venv and target; without this you inherit web-go's
  ```

`image` is allowed in the project layer, so a team can commit its image choice to `<repo>/.sbx/sandbox.toml` — and because `.sbx/` is covered by trust, a `git pull` that swaps the image stops `sbx run` and shows you the diff first.

## Working as a team

The project layer travels with the repo, so onboarding a teammate is three steps:

1. Commit `<repo>/.sbx/sandbox.toml` (profile, network mode, extra allowlist entries, dep masks) — and `<repo>/.sbx/Dockerfile` if the project needs its own image.
2. Your teammate clones, runs `sbx trust` and reads what the diff shows them.
3. `sbx run`.

What *cannot* be in that file, by design: anything that looks like a credential, any absolute path, and any command that would run in the container (`on_idle` and friends). Those three red lines are enforced, not advisory — a repo cannot decide where your credentials come from or what runs inside your sandbox.

## Troubleshooting

| Symptom | What's going on |
|---|---|
| `exited(oom)` in `sbx ls` | The container hit its memory cap. Raise `resources.memory`, or lower `max_running` — `sbx doctor` tells you whether the total fits in your Docker VM |
| The agent says a download failed | In allowlist mode the proxy blocked it. `sbx net denied` shows what, `sbx net allow <host>` fixes it and hot-reloads running tasks |
| `sbx run` stops and asks you to log in | The credentials in `sbx-home` expired. `sbx login` once; every task shares it |
| `sbx run` stops on `.sbx/` | Someone changed the project config. It prints the diff — read it, then `sbx trust` |
| Something is off and you don't know what | `sbx doctor`. It checks docker, the proxy, login, trust, notifications and the agent's first-run state, and prints a fix for each problem |

## Documentation

> The [introduction page](https://zzjha-cn.github.io/agent_sandbox/) is in English; the in-repo reference docs are in Chinese.

| Doc | What's in it |
|---|---|
| [Introduction](https://zzjha-cn.github.io/agent_sandbox/) | **Start here** — a web page covering a day of real use, the architecture diagram, and a Q&A on each piece |
| [commands.md](docs/commands.md) | **Command reference**: every command, flag, config field and environment variable |
| [walkthrough.md](docs/walkthrough.md) | One scenario end to end — what Docker, git and squid each do behind every command |
| [architecture.md](docs/architecture.md) | How the pieces fit together and where data flows |
| [design.md](docs/design.md) | Full design and its trade-offs |
| [CONTEXT.md](docs/CONTEXT.md) | Glossary and decision index |

## Status

**M1, M2 and nearly all of M3 are done** — only Codex support (M3-6) is left. Available commands: `run / attach / shell / stop / ls / path / done / drop / logs / port / net / memory / login / config / trust / doctor / upgrade`.

Known limits: a writable `.git` means a sandboxed agent can plant a git hook that later runs on your host (R1, see [The model](#the-model)); there is no `sbx merge` and no Codex support yet (M3-6); a host proxy has to speak HTTP (SOCKS-only needs a shim of your own); base images have to be Debian or Ubuntu.

Roadmap: [implementation-checklist.md](docs/implementation-checklist.md).

## Development

```bash
make test          # unit tests
make test-docker   # docker-backed integration tests (first run builds images; takes a few minutes)
make lint          # golangci-lint, falling back to go vet + gofmt
make e2e           # end-to-end acceptance (requires a logged-in sandbox)
```

Add `-v / --verbose` to print every docker and git command as it runs.

sbx drives everything through the `docker` and `git` CLIs rather than SDKs (ADR 0013). Dockerfiles, squid templates and allowlists are `go:embed`-ed into the binary, so it runs without the source tree.

## License

[Apache-2.0](LICENSE). Everything in this repository — including the Dockerfiles, the squid templates, the hooks and the status line script — is written for sbx and carries the same license.
