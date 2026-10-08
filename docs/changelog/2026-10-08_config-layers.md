# 2026-10-08 · 四层配置、`sbx config show`、项目层红线（M2-1、M2-2、M2-9）

> 承接 [2026-10-08_sbx-login.md](2026-10-08_sbx-login.md)。

## 一句话

配置从两层变四层（内置 → 全局 → 项目 → 工作区），加 `sbx config show` 回答"这个值到底是谁给的"，并且给跟着仓库走的那一层划了两条红线。

## 为什么先做它

它卡着四件事：`sbx net allow --project`（之前写死报错）、项目层白名单（M2-9）、资源上限按层覆盖（M2-15），以及整个信任机制（M2-3/4/5 要先有 `.sbx/sandbox.toml` 才有东西可信任）。

## 1. 合并（M2-1）

```
内置默认值
  → ~/.sbx/config.toml                 个人全局
  → <repo>/.sbx/sandbox.toml           项目级，提交进仓库
  → ~/.sbx/workspaces/<ws>.toml        你对这一个仓库的个人覆盖
```

标量后者覆盖前者，列表取并集。实现上的几个决定：

- **逐层解码到一个空 `Config`，再按 `md.IsDefined` 决定要不要覆盖**，而不是把下一层直接 decode 到上一层的结果上。后者分不清"显式写了 `cloud_mcp = false`"和"没写"，布尔和零值会被悄悄改掉。
- **字段表驱动**（`bindings()`）。合并、打印、记来源都走同一张表，加字段只改一处。
- **`<ws>` 用 Workspace id**（`shop-e76272`）而不是目录名，同名仓库不会撞车。
- **并集的代价**：列表项只能加不能减。想去掉内置默认的 `node_modules` 现在没有办法——记在 design §9.1 里，等有人真的需要再说。

### 校验改在合并之后

原来每读一个文件就 `Validate` 一次。四层之后不行：只写了 `memory = "4g"` 的项目层文件，单独看 `mode`、`proxy` 全是空字符串，必然误报。

所以校验只跑在合并结果上。代价是**被后面的层盖掉的非法值不会被发现**（项目层写了 `memory = "lots"`、工作区层写了 `6g`，不报错）——这是故意的，"最终生效的配置合法"才是要保证的事。

作为补偿，`Validate` 现在返回 `FieldError{Key, Msg}`，报错会带上来源：

```
sbx: network.mode 只能是 allowlist 或 open："wide-open"（项目 这一层给的值：/Users/you/code/shop/.sbx/sandbox.toml）
```

## 2. `sbx config show`

```console
$ sbx config show
默认      (内置)
全局      ~/.sbx/config.toml
项目      ~/code/shop/.sbx/sandbox.toml
工作区    ~/.sbx/workspaces/shop-e76272.toml  ← 没有这个文件

KEY                VALUE                                    FROM
network.mode       "allowlist"                              项目
network.allow      ["global.example.com", "api.internal"]   全局 + 项目
resources.memory   "6g"                                     工作区
```

FROM 这一列是重点：标量显示决定最终值的那一层，列表显示所有贡献者。仓库外也能跑，那时只有前两层。

层名是中文，tabwriter 按字节算宽度会把这张表排歪，所以第一张表自己对齐（`padCJK`）。`sbx ls` 的同类问题还在。

### 一个自己写出来又自己踩到的坑

第一版的 `config show` 是这么写的：

```go
if err := a.load(); err != nil {
    a.loadConfig()                       // 退回只读全局
    fmt.Fprintln(a.Err, "提示: 不在 git 仓库里…")
}
```

然后测项目层报错时，它平静地打印"不在 git 仓库里"，把真正的错误吞了——`load()` 失败的原因不止"不在仓库里"一种，**配置本身非法也会让它失败**。改成显式 `workspace.Resolve`，只有解析仓库失败才降级，配置错误照常往上抛。

## 3. 项目层的两条红线（M2-2）

`.sbx/sandbox.toml` 跟着仓库走，clone 下来就生效，所以这一层里出现下面两类内容直接报错：

- **密钥类字段**：键名含 `key`、`token`、`secret`、`password`、`credential`。`api_key_file` 这种"指明凭据从哪来"的也算——它本身不是密钥，但让一个提交进仓库的文件决定凭据来源，是同一类问题。
- **绝对路径**：值以 `/`、`~/`、`C:\` 开头。

检查走的是通用的 map 遍历而不是 `Config` 结构体：**未知字段在这一层也要被检查**，否则写一个 sbx 还不认识的 `api_key` 就能绕过去（未知字段在别的层只是警告）。

```
sbx: /Users/you/code/shop/.sbx/sandbox.toml: 项目层配置里有不允许的内容：
  - agents.claude.api_key_file（密钥类字段只能写在 全局 或 工作区 层）
  - deps.mask = "/abs/path"（绝对路径只能写在 全局 或 工作区 层）
```

## 4. 顺带收掉的两条

- **M2-9 项目层白名单**：`network.allow` 的并集生效即是，没有额外代码。
- **M2-11 的 `--project`**：`sbx net allow --project <host>` 现在写 `<repo>/.sbx/sandbox.toml`（原来会明确报错）。写完重新读四层再热加载，所以下发的是合并后的名单。

## 验证

- 单元测试：四层合并（标量覆盖、列表并集去重、默认值保留、来源标注）、缺层跳过、非法值指出所在层、被覆盖的非法值不报错、项目层五种拒绝 + 一个正常文件不误伤、`Paths` 的仓库内外两种形态。
- 真实跑：临时仓库上摆齐四层，`config show` 的 FROM 列逐行核对；`memory` 3g → 4g → 6g 落在工作区层；`allow` 的重复项只留一个。
- `net allow --project` 写文件、合并、热加载走通。
- `go build` / `go vet` / `go test ./...` 全绿。
