#!/usr/bin/env python3
"""Export the public static wiki; never copy private or historical directories."""
import argparse
from pathlib import Path
import shutil

PUBLIC = (
    'index.html', '.nojekyll', 'PUBLISHING.md', 'walkthrough.md', 'design.md',
    'commands.md', 'glossary.md', 'cli-reference.md', 'general-tasks.md',
    'dynamic-tools.md', 'automation.md', 'boundary.md', 'architecture.md', 'CONTEXT.md',
)

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', required=True, type=Path, help='new output directory')
    args = parser.parse_args()
    source = Path(__file__).resolve().parents[2] / 'docs'
    output = args.output.resolve()
    if output.exists():
        parser.error('output already exists; choose a new directory')
    if output == source or source in output.parents:
        parser.error('output must be outside docs')
    for name in PUBLIC:
        if not (source / name).is_file():
            parser.error('missing public document: ' + name)
    output.mkdir(parents=True)
    for name in PUBLIC:
        shutil.copyfile(source / name, output / name)
    (output / 'schemas').mkdir()
    for path in (source / 'schemas').iterdir():
        if path.name == 'README.md' or path.name.endswith('.schema.json'):
            shutil.copyfile(path, output / 'schemas' / path.name)
    print('Public wiki exported to ' + str(output))

if __name__ == '__main__':
    main()
