<h1 align="center">sbx</h1>

<p align="center">
  Run Claude Code in a Docker sandbox — skip the permission prompts without betting your machine on it.
</p>

<p align="center">
  <b>English</b> ·
  <a href="README.cn.md">简体中文</a>
</p>

<p align="center">
  <img alt="Go" src="https://img.shields.io/badge/Go-1.27-00ADD8?logo=go&logoColor=white">
  <img alt="License" src="https://img.shields.io/badge/license-Apache--2.0-blue">
  <img alt="Status" src="https://img.shields.io/badge/status-M1%20%2B%20part%20of%20M2-orange">
</p>

---

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

**Prerequisites**: Docker Desktop running (10GB of memory recommended), Go 1.27, a Claude subscription.

### 1. Install

```bash
cd core
make build
ln -sf "$PWD/bin/sbx" /usr/local/bin/sbx   # optional
```

### 2. Configure (only if you need a host proxy)

`~/.sbx/config.toml`:

```toml
[network]
upstream = "http://host.docker.internal:7890"   # leave empty for a direct connection
```

### 3. Log in (once; every task shares it)

Run `sbx run` in any repo. Once the image is built and sbx finds no credentials, it prints the exact command to run:

```bash
docker run -it --rm -e HOME=/home/agent -v sbx-home:/home/agent \
  sbx/web-go:<hash> claude auth login
```

Open the link, paste the code back. Credentials live in the `sbx-home` volume, not inside any task.

### 4. Run your first task

```bash
cd ~/code/shop
sbx run fix-login
```

That's it. Worktree, branch, container, network and proxy credentials are all set up, and Claude Code is waiting inside.

## The workflow

```bash
sbx run fix-login              # start (running it again resumes, continuing the last conversation)
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

All a container can see is: its worktree, the shared `sbx-home` (login state and Claude config), `sbx-cache` (package-manager caches) and a read-only `/sbx/gen` (hooks, settings, status line). **Nothing else from your host is mounted.**

## Networking

All egress goes through one shared Squid proxy (`sbx-proxy`), which is **open by default**.

```bash
sbx net denied                     # what got blocked (--all for every category, --since 2h to narrow)
sbx net allow fastdl.mongodb.org   # allow a host and hot-reload it into running tasks
sbx run t1 --net allowlist         # allowlist mode, just for this run
```

Allowlist mode (`network.mode = "allowlist"`) permits only the built-in list plus whatever you configure — the right choice when running untrusted code. Both modes go through Squid, so the logs are always there, and the cloud-MCP policy block applies either way ([ADR 0015](docs/CONTEXT.md)).

> Flipping the default from allowlist to open was a deliberate call on 2026-10-08. When the allowlist blocked web search or a dependency download, the agent would quietly substitute something else and report a passing test — an outcome worse than the block itself. The trade-off is recorded in the ADR 0005 revision.

## Configuration

`~/.sbx/config.toml`; every field is optional:

```toml
profile = "web-go"            # built-in profile (currently the only one)

[network]
upstream  = ""                # host proxy; empty means direct
mode      = "open"            # open | allowlist
allow     = []                # appended to the built-in allowlist

[resources]
cpus = 2 ; memory = "3g" ; pids = 1024

[deps]
mask = ["node_modules"]       # each masked by a volume, keeping your host clean
```

Full field list, precedence and when changes take effect: [docs/commands.md](docs/commands.md#配置文件sbxconfigtoml).

## Documentation

> Docs are currently written in Chinese.

| Doc | What's in it |
|---|---|
| [commands.md](docs/commands.md) | **Command reference**: every command, flag, config field and environment variable |
| [walkthrough.md](docs/walkthrough.md) | One scenario end to end — what Docker, git and squid each do behind every command |
| [architecture.md](docs/architecture.md) | How the pieces fit together and where data flows |
| [design.md](docs/design.md) | Full design and its trade-offs |
| [CONTEXT.md](docs/CONTEXT.md) | Glossary and decision index |

## Status

**M1 (MVP) is done; M2 is in progress.** Available commands: `run / attach / shell / stop / ls / path / done / net / memory / upgrade`.

Known limits: `web-go` is the only built-in profile; networking is shared-proxy only (dedicated is M2-7); configuration has two layers, not four (project layer is M2-1); `sbx login` still means running a docker command by hand (M2-12); there is no `sbx merge`.

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

[Apache-2.0](LICENSE).

The status line script `core/assets/agent-layer/statusline.sh` is vendored from [ykdojo/claude-code-tips](https://github.com/ykdojo/claude-code-tips) by YK Sugi; attribution is kept in the file header.
