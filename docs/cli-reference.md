# CLI 完整参数参考

[首页](index.html) · [按场景查命令](commands.md)

由当前二进制的 `--help` 导出，包含所有可发现的子命令与参数；不执行登录、Docker 或业务操作。用法解释和风险说明以 [命令手册](commands.md) 为入口。升级后应重新导出并核对差异。

## sbx

```text
Run Claude Code / Codex tasks in container sandboxes

Usage:
  sbx [command]

Available Commands:
  admit       Check v2 task policy and runtime admission without provisioning
  attach      Enter the task's agent session (tmux; Ctrl-b, release, then d leaves it running)
  clean       Reclaim disk: old images and derived caches (--deep also clears package and build caches)
  completion  Generate the autocompletion script for the specified shell
  config      Inspect the effective configuration, or write a template for it
  doctor      Check docker, the proxy, login, trust and the agent's first-run state
  done        Finish a task: remove its container, network, dep volumes, worktree and state; keep the branch
  drop        Finish a task and delete its branch too (asks you to type the task name)
  events      Inspect or flush the private durable team-event outbox
  execution   Inspect durable execution progress
  forward     Single-target TCP holes from a task to one address you name
  help        Help about any command
  isolation   Inspect or adopt this user's dedicated Docker daemon
  login       Log in to an agent inside the sandbox (default claude; credentials live in sbx-home, shared by every task)
  logs        Show a headless run's output (sbx run -p); -f follows it
  ls          List the tasks in this workspace (--all for every workspace)
  memory      Claude project memory between your host and the sandbox (ADR 0016)
  net         Inspect and adjust what tasks can reach on the network
  path        Print a task's working directory (omit task for main, the repo root): cd $(sbx path <task>)
  policy      Inspect the fixed administrator policy and effective ceilings
  port        Expose a port from the task on 127.0.0.1 (--rm takes it back)
  prep        Freeze private dependencies into an image that new tasks start from
  run         Create or resume a task and start the agent in its sandbox (omit task for main)
  schema      List or export bundled JSON Schemas without configuration or Docker
  shell       Open a bash shell in the task's container
  stop        Stop the task's container, keeping everything; stops sbx-proxy with the last task
  task        Run and inspect general tasks with registered inputs and delivery targets
  trust       Review what this repo's .sbx/ contains and record it as trusted
  upgrade     Upgrade claude in the sandbox to the latest version (rebuilds the agent layer; new tasks pick it up)
  version     Print application and protocol version information

Flags:
  -h, --help      help for sbx
  -v, --verbose   print every docker and git command sbx runs
      --version   version for sbx

Use "sbx [command] --help" for more information about a command.
```

## sbx admit

```text
Check v2 task policy and runtime admission without provisioning

Usage:
  sbx admit --spec <file|-> [flags]

Flags:
      --envelope string   alias for --spec; accepts v2 tasks only
  -h, --help              help for admit
      --spec string       v2 task file or - for stdin

Global Flags:
  -v, --verbose   print every docker and git command sbx runs
```

## sbx attach

```text
Enter the task's agent session (tmux; Ctrl-b, release, then d leaves it running)

Usage:
  sbx attach <task> [flags]

Flags:
  -h, --help   help for attach

Global Flags:
  -v, --verbose   print every docker and git command sbx runs
```

## sbx clean

```text
Reclaim disk: old images and derived caches (--deep also clears package and build caches)

Usage:
  sbx clean [flags]

Flags:
      --deep   also clear package caches and the BuildKit build cache (they have to be re-fetched)
  -h, --help   help for clean
  -y, --yes    skip the confirmation (for scripts)

Global Flags:
  -v, --verbose   print every docker and git command sbx runs
```

## sbx completion

```text
Generate the autocompletion script for sbx for the specified shell.
See each sub-command's help for details on how to use the generated script.

Usage:
  sbx completion [command]

Available Commands:
  bash        Generate the autocompletion script for bash
  fish        Generate the autocompletion script for fish
  powershell  Generate the autocompletion script for powershell
  zsh         Generate the autocompletion script for zsh

Flags:
  -h, --help   help for completion

Global Flags:
  -v, --verbose   print every docker and git command sbx runs

Use "sbx completion [command] --help" for more information about a command.
```

## sbx completion bash

```text
Generate the autocompletion script for the bash shell.

This script depends on the 'bash-completion' package.
If it is not installed already, you can install it via your OS's package manager.

To load completions in your current shell session:

	source <(sbx completion bash)

To load completions for every new session, execute once:

#### Linux:

	sbx completion bash > /etc/bash_completion.d/sbx

#### macOS:

	sbx completion bash > $(brew --prefix)/etc/bash_completion.d/sbx

You will need to start a new shell for this setup to take effect.

Usage:
  sbx completion bash

Flags:
  -h, --help              help for bash
      --no-descriptions   disable completion descriptions

Global Flags:
  -v, --verbose   print every docker and git command sbx runs
```

## sbx completion fish

```text
Generate the autocompletion script for the fish shell.

To load completions in your current shell session:

	sbx completion fish | source

To load completions for every new session, execute once:

	sbx completion fish > ~/.config/fish/completions/sbx.fish

You will need to start a new shell for this setup to take effect.

Usage:
  sbx completion fish [flags]

Flags:
  -h, --help              help for fish
      --no-descriptions   disable completion descriptions

Global Flags:
  -v, --verbose   print every docker and git command sbx runs
```

## sbx completion powershell

```text
Generate the autocompletion script for powershell.

To load completions in your current shell session:

	sbx completion powershell | Out-String | Invoke-Expression

To load completions for every new session, add the output of the above command
to your powershell profile.

Usage:
  sbx completion powershell [flags]

Flags:
  -h, --help              help for powershell
      --no-descriptions   disable completion descriptions

Global Flags:
  -v, --verbose   print every docker and git command sbx runs
```

## sbx completion zsh

```text
Generate the autocompletion script for the zsh shell.

If shell completion is not already enabled in your environment you will need
to enable it.  You can execute the following once:

	echo "autoload -U compinit; compinit" >> ~/.zshrc

To load completions in your current shell session:

	source <(sbx completion zsh)

To load completions for every new session, execute once:

#### Linux:

	sbx completion zsh > "${fpath[1]}/_sbx"

#### macOS:

	sbx completion zsh > $(brew --prefix)/share/zsh/site-functions/_sbx

You will need to start a new shell for this setup to take effect.

Usage:
  sbx completion zsh [flags]

Flags:
  -h, --help              help for zsh
      --no-descriptions   disable completion descriptions

Global Flags:
  -v, --verbose   print every docker and git command sbx runs
```

## sbx config

```text
Inspect the effective configuration, or write a template for it

Usage:
  sbx config [command]

Available Commands:
  init        Write a commented <repo>/.sbx/sandbox.toml template (the project layer)
  show        Print every effective value and which layer it came from

Flags:
  -h, --help   help for config

Global Flags:
  -v, --verbose   print every docker and git command sbx runs

Use "sbx config [command] --help" for more information about a command.
```

## sbx config init

```text
Write a commented <repo>/.sbx/sandbox.toml template (the project layer)

Usage:
  sbx config init [flags]

Flags:
      --force         overwrite an existing file
  -h, --help          help for init
      --lang string   comment language of the template: en | zh (default "en")
      --print         print the template instead of writing it

Global Flags:
  -v, --verbose   print every docker and git command sbx runs
```

## sbx config show

```text
Print every effective value and which layer it came from

Usage:
  sbx config show [flags]

Flags:
  -h, --help   help for show

Global Flags:
  -v, --verbose   print every docker and git command sbx runs
```

## sbx doctor

```text
Check docker, the proxy, login, trust and the agent's first-run state

Usage:
  sbx doctor [flags]

Flags:
  -h, --help     help for doctor
      --quick    skip the slow checks (the first-run smoke test)
      --strict   exit non-zero on warnings too (for scripts)

Global Flags:
  -v, --verbose   print every docker and git command sbx runs
```

## sbx done

```text
Finish a task: remove its container, network, dep volumes, worktree and state; keep the branch

Usage:
  sbx done <task>... [flags]

Flags:
      --force   remove the task even with uncommitted changes in its worktree
  -h, --help    help for done

Global Flags:
  -v, --verbose   print every docker and git command sbx runs
```

## sbx drop

```text
Finish a task and delete its branch too (asks you to type the task name)

Usage:
  sbx drop <task> [flags]

Flags:
      --force   drop it even with uncommitted changes in its worktree
  -h, --help    help for drop
  -y, --yes     skip the confirmation (for scripts)

Global Flags:
  -v, --verbose   print every docker and git command sbx runs
```

## sbx events

```text
Inspect or flush the private durable team-event outbox

Usage:
  sbx events [command]

Available Commands:
  flush       
  status      

Flags:
  -h, --help   help for events

Global Flags:
  -v, --verbose   print every docker and git command sbx runs

Use "sbx events [command] --help" for more information about a command.
```

## sbx events flush

```text
Usage:
  sbx events flush [flags]

Flags:
  -h, --help   help for flush

Global Flags:
  -v, --verbose   print every docker and git command sbx runs
```

## sbx events status

```text
Usage:
  sbx events status [flags]

Flags:
  -h, --help   help for status

Global Flags:
  -v, --verbose   print every docker and git command sbx runs
```

## sbx execution

```text
Inspect durable execution progress

Usage:
  sbx execution [command]

Available Commands:
  status      Read recorded attempts without contacting Docker

Flags:
  -h, --help   help for execution

Global Flags:
  -v, --verbose   print every docker and git command sbx runs

Use "sbx execution [command] --help" for more information about a command.
```

## sbx execution status

```text
Read recorded attempts without contacting Docker

Usage:
  sbx execution status <task> [flags]

Flags:
  -h, --help   help for status
      --json   print versioned execution status JSON

Global Flags:
  -v, --verbose   print every docker and git command sbx runs
```

## sbx forward

```text
A forward lets a task reach exactly one address and port -- a database on your machine,
or one on an internal network. The task still has no default route and still reaches the
internet only through the proxy. Because a forward names a real address, it can only be
written on your own machine: the project layer rejects it.

Usage:
  sbx forward [command]

Available Commands:
  init        Write a commented [[forward]] template into the workspace layer

Flags:
  -h, --help   help for forward

Global Flags:
  -v, --verbose   print every docker and git command sbx runs

Use "sbx forward [command] --help" for more information about a command.
```

## sbx forward init

```text
Write a commented [[forward]] template into the workspace layer

Usage:
  sbx forward init [flags]

Flags:
      --force         overwrite an existing file
  -h, --help          help for init
      --lang string   comment language of the template: en | zh (default "en")
      --print         print the template instead of writing it

Global Flags:
  -v, --verbose   print every docker and git command sbx runs
```

## sbx isolation

```text
Inspect or adopt this user's dedicated Docker daemon

Usage:
  sbx isolation [command]

Available Commands:
  adopt       Explicitly bind existing volumes to this user's dedicated daemon; preserve credentials
  status      Read-only daemon ownership and legacy-volume status

Flags:
  -h, --help   help for isolation

Global Flags:
  -v, --verbose   print every docker and git command sbx runs

Use "sbx isolation [command] --help" for more information about a command.
```

## sbx isolation adopt

```text
Explicitly bind existing volumes to this user's dedicated daemon; preserve credentials

Usage:
  sbx isolation adopt [flags]

Flags:
  -h, --help   help for adopt

Global Flags:
  -v, --verbose   print every docker and git command sbx runs
```

## sbx isolation status

```text
Read-only daemon ownership and legacy-volume status

Usage:
  sbx isolation status [flags]

Flags:
  -h, --help   help for status
      --json   print non-secret ownership metadata as JSON

Global Flags:
  -v, --verbose   print every docker and git command sbx runs
```

## sbx login

```text
Log in to an agent inside the sandbox (default claude; credentials live in sbx-home, shared by every task)

Usage:
  sbx login [claude|codex] [flags]

Flags:
      --console         claude: use an Anthropic Console account (usage billing) instead of a Claude subscription
      --email string    claude: pre-fill this email address on the login page
      --force           run the login flow again, or replace existing credentials when importing
  -h, --help            help for login
      --import string   codex: import credentials from a local Codex directory or auth.json (use --force to replace existing credentials)
      --logout          log out, clearing the credentials in sbx-home
      --status          only print who is logged in, do not log in

Global Flags:
  -v, --verbose   print every docker and git command sbx runs
```

## sbx logs

```text
Show a headless run's output (sbx run -p); -f follows it

Usage:
  sbx logs <task> [flags]

Flags:
  -f, --follow     keep printing new output until the task finishes
  -h, --help       help for logs
  -n, --tail int   only show the last n lines (default: everything, or 20 with -f)

Global Flags:
  -v, --verbose   print every docker and git command sbx runs
```

## sbx ls

```text
List the tasks in this workspace (--all for every workspace)

Usage:
  sbx ls [flags]

Flags:
      --all         list tasks in every workspace, not just this repo's
  -h, --help        help for ls
      --no-denied   skip the DENIED column (it reads the proxy's access log)

Global Flags:
  -v, --verbose   print every docker and git command sbx runs
```

## sbx memory

```text
Claude project memory between your host and the sandbox (ADR 0016)

Usage:
  sbx memory [command]

Available Commands:
  pull        Bring memory written in the sandbox back to the host (shows the diff, then asks)

Flags:
  -h, --help   help for memory

Global Flags:
  -v, --verbose   print every docker and git command sbx runs

Use "sbx memory [command] --help" for more information about a command.
```

## sbx memory pull

```text
Bring memory written in the sandbox back to the host (shows the diff, then asks)

Usage:
  sbx memory pull [flags]

Flags:
  -h, --help   help for pull
  -y, --yes    write without asking

Global Flags:
  -v, --verbose   print every docker and git command sbx runs
```

## sbx net

```text
Inspect and adjust what tasks can reach on the network

Usage:
  sbx net [command]

Available Commands:
  allow       Add hosts to the allowlist (~/.sbx/config.toml, or --project) and reload running tasks
  denied      List the hosts the proxy denied (omit task to cover every task in this workspace)

Flags:
  -h, --help   help for net

Global Flags:
  -v, --verbose   print every docker and git command sbx runs

Use "sbx net [command] --help" for more information about a command.
```

## sbx net allow

```text
Add hosts to the allowlist (~/.sbx/config.toml, or --project) and reload running tasks

Usage:
  sbx net allow <host>... [flags]

Flags:
  -h, --help      help for allow
      --project   write to <repo>/.sbx/sandbox.toml instead, so the whole team gets it

Global Flags:
  -v, --verbose   print every docker and git command sbx runs
```

## sbx net denied

```text
List the hosts the proxy denied (omit task to cover every task in this workspace)

Usage:
  sbx net denied [task] [flags]

Flags:
      --all              also show policy blocks, known telemetry and auth failures
  -h, --help             help for denied
      --since duration   only look this far back, e.g. 2h (default: everything)

Global Flags:
  -v, --verbose   print every docker and git command sbx runs
```

## sbx path

```text
Print a task's working directory (omit task for main, the repo root): cd $(sbx path <task>)

Usage:
  sbx path [task] [flags]

Flags:
  -h, --help   help for path

Global Flags:
  -v, --verbose   print every docker and git command sbx runs
```

## sbx policy

```text
Inspect the fixed administrator policy and effective ceilings

Usage:
  sbx policy [command]

Available Commands:
  show        

Flags:
  -h, --help   help for policy

Global Flags:
  -v, --verbose   print every docker and git command sbx runs

Use "sbx policy [command] --help" for more information about a command.
```

## sbx policy show

```text
Usage:
  sbx policy show [flags]

Flags:
  -h, --help   help for show

Global Flags:
  -v, --verbose   print every docker and git command sbx runs
```

## sbx port

```text
Expose a port from the task on 127.0.0.1 (--rm takes it back)

Usage:
  sbx port <task> <port> [flags]

Flags:
  -h, --help   help for port
      --rm     remove the forwarder for this port (omit the port to remove all of them)

Global Flags:
  -v, --verbose   print every docker and git command sbx runs
```

## sbx prep

```text
prep runs a docker build on the host, where you still have intranet access and credentials,
and seeds the result into each new task's dependency volumes. The task container itself
needs no private network configuration, and never sees the credentials.

Usage:
  sbx prep [command]

Available Commands:
  build       Build the prep image (use --check to report without seeding)
  drop        Remove prep images for this workspace
  init        Write a commented prep.Dockerfile template
  reseed      Re-seed a task's dependency volumes from the current prep image (destructive)
  show        Show the current prep image, whether it drifted, and which tasks were seeded from it

Flags:
  -h, --help   help for prep

Global Flags:
  -v, --verbose   print every docker and git command sbx runs

Use "sbx prep [command] --help" for more information about a command.
```

## sbx prep build

```text
Build the prep image (use --check to report without seeding)

Usage:
  sbx prep build [flags]

Flags:
      --check   build and report, but do not seed anything
  -h, --help    help for build

Global Flags:
  -v, --verbose   print every docker and git command sbx runs
```

## sbx prep drop

```text
Remove prep images for this workspace

Usage:
  sbx prep drop [flags]

Flags:
      --all    also remove the image the current config points at
  -h, --help   help for drop

Global Flags:
  -v, --verbose   print every docker and git command sbx runs
```

## sbx prep init

```text
Write a commented prep.Dockerfile template

Usage:
  sbx prep init [flags]

Flags:
      --force         overwrite an existing file
  -h, --help          help for init
      --lang string   comment language of the template: en | zh (default "en")

Global Flags:
  -v, --verbose   print every docker and git command sbx runs
```

## sbx prep reseed

```text
Re-seed a task's dependency volumes from the current prep image (destructive)

Usage:
  sbx prep reseed <task> [flags]

Flags:
  -h, --help   help for reseed
      --yes    skip the confirmation (for scripts)

Global Flags:
  -v, --verbose   print every docker and git command sbx runs
```

## sbx prep show

```text
Show the current prep image, whether it drifted, and which tasks were seeded from it

Usage:
  sbx prep show [flags]

Flags:
  -h, --help   help for show

Global Flags:
  -v, --verbose   print every docker and git command sbx runs
```

## sbx run

```text
Create or resume a task and start the agent in its sandbox (omit task for main)

Usage:
  sbx run [task] [flags]

Flags:
      --agent string      agent for this run: claude or codex (overrides default_agent without changing config files)
      --base string       starting point for the new branch (default: current HEAD)
      --cloud-mcp         let this task reach Claude's cloud connectors (off by default; see ADR 0015)
  -d, --detach            start the task without attaching to it
      --envelope string   alias for --spec; accepts v2 tasks only
      --fresh             start a new conversation instead of continuing this task's last one
  -h, --help              help for run
      --jsonl             write machine-readable execution events and result to stdout (headless only)
      --net string        network mode for this run: open (default, everything allowed) or allowlist
  -p, --prompt string     headless: run this prompt and exit instead of opening the session ("-" reads stdin)
      --proxy string      egress proxy for this task: shared (default, one squid for everyone) or dedicated (its own sidecar)
      --spec string       run a v2 task from a file or - for stdin

Global Flags:
  -v, --verbose   print every docker and git command sbx runs
```

## sbx schema

```text
List or export bundled JSON Schemas without configuration or Docker

Usage:
  sbx schema [name] [flags]

Flags:
  -h, --help   help for schema

Global Flags:
  -v, --verbose   print every docker and git command sbx runs
```

## sbx shell

```text
Open a bash shell in the task's container

Usage:
  sbx shell <task> [flags]

Flags:
  -h, --help   help for shell

Global Flags:
  -v, --verbose   print every docker and git command sbx runs
```

## sbx stop

```text
Stop the task's container, keeping everything; stops sbx-proxy with the last task

Usage:
  sbx stop <task> [flags]

Flags:
  -h, --help   help for stop

Global Flags:
  -v, --verbose   print every docker and git command sbx runs
```

## sbx task

```text
Run and inspect general tasks with registered inputs and delivery targets

Usage:
  sbx task [command]

Available Commands:
  apply       
  approve     
  clean       Remove idle task runtime resources, retaining workspace, logs, artifacts and receipts
  deliver     
  list        List durable general tasks without Git, configuration or Docker
  logs        Read current or retained attempt logs without Git or Docker
  prune       Explicitly prune a reconciled artifact, retaining history and delivery targets
  reconcile   
  recover     Stop orphaned runtime and reconcile interrupted execution without replaying effects
  reject      
  run         
  status      Read general-task records without Git, configuration or Docker
  stop        Cancel execution and stop owned containers, retaining all facts
  verify      

Flags:
  -h, --help   help for task

Global Flags:
  -v, --verbose   print every docker and git command sbx runs

Use "sbx task [command] --help" for more information about a command.
```

## sbx task apply

```text
Usage:
  sbx task apply --spec <file|-> [flags]

Flags:
  -h, --help          help for apply
      --spec string   general task JSON (maximum 1 MiB), or - for stdin

Global Flags:
  -v, --verbose   print every docker and git command sbx runs
```

## sbx task approve

```text
Usage:
  sbx task approve --spec <file|-> [flags]

Flags:
  -h, --help              help for approve
      --reviewer string   operator identity recorded with the review
      --spec string       general task JSON (maximum 1 MiB), or - for stdin

Global Flags:
  -v, --verbose   print every docker and git command sbx runs
```

## sbx task clean

```text
Remove idle task runtime resources, retaining workspace, logs, artifacts and receipts

Usage:
  sbx task clean <task> [flags]

Flags:
  -h, --help   help for clean

Global Flags:
  -v, --verbose   print every docker and git command sbx runs
```

## sbx task deliver

```text
Usage:
  sbx task deliver --spec <file|-> [flags]

Flags:
  -h, --help          help for deliver
      --spec string   general task JSON (maximum 1 MiB), or - for stdin

Global Flags:
  -v, --verbose   print every docker and git command sbx runs
```

## sbx task list

```text
List durable general tasks without Git, configuration or Docker

Usage:
  sbx task list [flags]

Flags:
  -h, --help   help for list
      --json   print versioned task list JSON

Global Flags:
  -v, --verbose   print every docker and git command sbx runs
```

## sbx task logs

```text
Read current or retained attempt logs without Git or Docker

Usage:
  sbx task logs <task> [flags]

Flags:
      --attempt string   recorded attempt ID (default latest)
  -f, --follow           follow this attempt until its record closes
  -h, --help             help for logs
  -n, --tail int         last n lines; 0 means all

Global Flags:
  -v, --verbose   print every docker and git command sbx runs
```

## sbx task prune

```text
Explicitly prune a reconciled artifact, retaining history and delivery targets

Usage:
  sbx task prune <task> <attempt> [flags]

Flags:
  -h, --help   help for prune

Global Flags:
  -v, --verbose   print every docker and git command sbx runs
```

## sbx task reconcile

```text
Usage:
  sbx task reconcile --spec <file|-> [flags]

Flags:
  -h, --help          help for reconcile
      --spec string   general task JSON (maximum 1 MiB), or - for stdin

Global Flags:
  -v, --verbose   print every docker and git command sbx runs
```

## sbx task recover

```text
Stop orphaned runtime and reconcile interrupted execution without replaying effects

Usage:
  sbx task recover <task> [flags]

Flags:
  -h, --help   help for recover

Global Flags:
  -v, --verbose   print every docker and git command sbx runs
```

## sbx task reject

```text
Usage:
  sbx task reject --spec <file|-> [flags]

Flags:
  -h, --help              help for reject
      --reviewer string   operator identity recorded with the review
      --spec string       general task JSON (maximum 1 MiB), or - for stdin

Global Flags:
  -v, --verbose   print every docker and git command sbx runs
```

## sbx task run

```text
Usage:
  sbx task run --spec <file|-> [flags]

Flags:
  -h, --help          help for run
      --spec string   general task JSON (maximum 1 MiB), or - for stdin

Global Flags:
  -v, --verbose   print every docker and git command sbx runs
```

## sbx task status

```text
Read general-task records without Git, configuration or Docker

Usage:
  sbx task status <task> [flags]

Flags:
  -h, --help   help for status
      --json   print versioned general-task status JSON

Global Flags:
  -v, --verbose   print every docker and git command sbx runs
```

## sbx task stop

```text
Cancel execution and stop owned containers, retaining all facts

Usage:
  sbx task stop <task> [flags]

Flags:
  -h, --help   help for stop

Global Flags:
  -v, --verbose   print every docker and git command sbx runs
```

## sbx task verify

```text
Usage:
  sbx task verify --spec <file|-> [flags]

Flags:
  -h, --help          help for verify
      --spec string   general task JSON (maximum 1 MiB), or - for stdin

Global Flags:
  -v, --verbose   print every docker and git command sbx runs
```

## sbx trust

```text
Review what this repo's .sbx/ contains and record it as trusted

Usage:
  sbx trust [flags]

Flags:
  -h, --help   help for trust
      --show   only show what changed, do not record anything
  -y, --yes    record it without asking

Global Flags:
  -v, --verbose   print every docker and git command sbx runs
```

## sbx upgrade

```text
Upgrade claude in the sandbox to the latest version (rebuilds the agent layer; new tasks pick it up)

Usage:
  sbx upgrade [flags]

Flags:
  -h, --help   help for upgrade

Global Flags:
  -v, --verbose   print every docker and git command sbx runs
```

## sbx version

```text
Print application and protocol version information

Usage:
  sbx version [flags]

Flags:
  -h, --help   help for version
      --json   print machine-readable protocol capabilities

Global Flags:
  -v, --verbose   print every docker and git command sbx runs
```
