#!/usr/bin/env python3
"""Check or regenerate the public CLI reference using read-only --help calls."""
import argparse
from pathlib import Path
import re
import subprocess

HEADER = '''# CLI 完整参数参考

[首页](index.html) · [按场景查命令](commands.md)

由当前二进制的 `--help` 导出，包含所有可发现的子命令与参数；不执行登录、Docker 或业务操作。用法解释和风险说明以 [命令手册](commands.md) 为入口。升级后应重新导出并核对差异。
'''

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--sbx', required=True, type=Path)
    parser.add_argument('--write', action='store_true')
    args = parser.parse_args()
    entries = []
    def visit(parts):
        result = subprocess.run([str(args.sbx.resolve()), *parts, '--help'],
                                capture_output=True, text=True, check=True, timeout=10)
        entries.append((parts, result.stdout))
        block = re.search(r'Available Commands:\n(.*?)(?:\n\n|\nFlags:)', result.stdout, re.S)
        if block:
            for line in block.group(1).splitlines():
                match = re.match(r'  ([a-z][a-z-]*)\s', line)
                if match and match[1] != 'help':
                    visit(parts + [match[1]])
    visit([])
    text = HEADER
    for parts, help_text in entries:
        text += '\n## ' + ('sbx ' + ' '.join(parts)).strip() + '\n\n```text\n' + help_text.rstrip() + '\n```\n'
    target = Path(__file__).resolve().parents[2] / 'docs/cli-reference.md'
    if args.write:
        target.write_text(text)
    if target.read_text() != text:
        parser.error('CLI reference differs; review changes and rerun with --write')
    print(f'CLI reference matches {len(entries)} help entries')

if __name__ == '__main__':
    main()
