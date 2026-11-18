#!/usr/bin/env python3
"""Render an evidence-labelled sbx graph to standalone, offline HTML (Python 3.9+)."""
import argparse
import html
import json
from pathlib import Path
import re
import sys

STATUSES = {"proposed", "configured", "verified", "blocked", "unknown", "na"}


def require_text(value, field):
    if not isinstance(value, str) or not value.strip():
        raise ValueError(field + " must be a non-empty string")


def validate(data):
    if not isinstance(data, dict):
        raise ValueError("input must be an object")
    for field in ("title", "scope", "generated_at"):
        require_text(data.get(field), field)
    if not isinstance(data.get("nodes"), list) or not data["nodes"]:
        raise ValueError("nodes must be a non-empty list")
    ids = set()
    for node in data["nodes"]:
        if not isinstance(node, dict):
            raise ValueError("node must be an object")
        for field in ("id", "title"):
            require_text(node.get(field), "node." + field)
        if node["id"] in ids:
            raise ValueError("duplicate node id: " + node["id"])
        ids.add(node["id"])
        if type(node.get("lane")) is not int or node["lane"] not in range(4):
            raise ValueError("node.lane must be 0, 1, 2 or 3")
        if node.get("status") not in STATUSES:
            raise ValueError("invalid node.status")
        for field in ("details", "evidence"):
            if not isinstance(node.get(field), list) or not node[field]:
                raise ValueError("node." + field + " must be a non-empty list")
            for item in node[field]:
                require_text(item, "node." + field)
    if not isinstance(data.get("edges"), list):
        raise ValueError("edges must be a list")
    for edge in data["edges"]:
        if not isinstance(edge, dict):
            raise ValueError("edge must be an object")
        for field in ("source", "target", "label", "evidence"):
            require_text(edge.get(field), "edge." + field)
        if edge["source"] not in ids or edge["target"] not in ids:
            raise ValueError("edge references missing node")
        if edge["source"] == edge["target"]:
            raise ValueError("use separate nodes to express a self relationship")
        if edge.get("status") not in STATUSES:
            raise ValueError("invalid edge.status")
    if not isinstance(data.get("notes"), list):
        raise ValueError("notes must be a list")
    for note in data["notes"]:
        require_text(note, "note")
    if not isinstance(data.get("configuration", []), list):
        raise ValueError("configuration must be a list")
    for setting in data.get("configuration", []):
        if not isinstance(setting, dict):
            raise ValueError("configuration entry must be an object")
        for field in ("key", "source", "evidence"):
            require_text(setting.get(field), "configuration." + field)
        if not isinstance(setting.get("value"), (str, int, float, bool)):
            raise ValueError("configuration.value must be a scalar display value")
        if setting.get("status") not in STATUSES:
            raise ValueError("invalid configuration.status")
    if not isinstance(data.get("display", {}), dict):
        raise ValueError("display must be an object")


def render(data, language=None, theme=None, density=None):
    validate(data)
    assets = Path(__file__).resolve().parents[1] / "assets"
    locales = json.loads((assets / "topology-locales.json").read_text(encoding="utf-8"))
    language = language or data.get("language", "zh-CN")
    if not isinstance(language, str) or language not in locales:
        raise ValueError("supported languages: " + ", ".join(locales))
    display = dict(data.get("display", {}))
    display["theme"] = theme or display.get("theme", "light")
    display["density"] = density or display.get("density", "comfortable")
    if display["theme"] not in ("light", "dark"):
        raise ValueError("display.theme must be light or dark")
    if display["density"] not in ("comfortable", "compact"):
        raise ValueError("display.density must be comfortable or compact")
    unknown = set(display) - {"theme", "density"}
    if unknown:
        raise ValueError("unknown display options: " + ", ".join(sorted(unknown)))
    template = (assets / "topology.html").read_text(encoding="utf-8")
    replacements = {"__SBX_LANGUAGE__": language, "__SBX_TITLE__": html.escape(data["title"]),
                    "__SBX_THEME__": display["theme"], "__SBX_DENSITY__": display["density"]}
    for key, value in locales[language].items():
        if isinstance(value, str):
            replacements["__UI_" + key + "__"] = html.escape(value, quote=True)
    # Script data must not be able to close its inert JSON element or inject HTML.
    def safe_json(value):
        return json.dumps(value, ensure_ascii=False, allow_nan=False).replace("&", "\\u0026").replace("<", "\\u003c").replace(">", "\\u003e")
    replacements["__SBX_UI_DATA__"] = safe_json(locales[language])
    replacements["__SBX_TOPOLOGY_DATA__"] = safe_json(dict(data, language=language, display=display))
    return re.sub(r"__(?:SBX|UI)_[A-Za-z_]+__", lambda match: replacements[match.group()], template)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--input", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--lang", choices=("zh-CN", "en"), help="page language; overrides input.language (default: zh-CN)")
    parser.add_argument("--theme", choices=("light", "dark"), help="override display.theme (default: light)")
    parser.add_argument("--density", choices=("comfortable", "compact"), help="override display.density (default: comfortable)")
    parser.add_argument("--replace", action="store_true", help="replace a reviewed generated HTML")
    args = parser.parse_args()
    try:
        document = render(json.loads(args.input.read_text(encoding="utf-8")), args.lang, args.theme, args.density)
        if args.input.resolve() == args.output.resolve():
            raise ValueError("input and output must differ")
        args.output.parent.mkdir(parents=True, exist_ok=True)
        with args.output.open("w" if args.replace else "x", encoding="utf-8") as stream:
            stream.write(document)
    except (OSError, ValueError, TypeError) as exc:
        print("topology error: " + str(exc), file=sys.stderr)
        return 1
    print(str(args.output.resolve()))
    return 0


if __name__ == "__main__":
    sys.exit(main())
