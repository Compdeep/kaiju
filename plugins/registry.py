"""Plugin loading + dispatch for the kaiju plugin host.

Each plugin is a subdirectory with a ``plugin.py`` that defines:

    MANIFEST : dict
        {"name", "description",
         "tools": [{"name", "description", "parameters", "impact"}]}

    invoke(tool: str, params: dict) -> dict            # sync or async
        returns a kaiju ToolMessage envelope:
        {"kind", "status": "ok"|"empty"|"error", "content"?, "detail"?, "data"?}

An optional ``skill.md`` beside ``plugin.py`` is attached to the plugin's manifest
as ``skill`` — the "when/how" playbook kaiju loads next to the tool.

Loading happens ONCE at host startup; plugins stay warm for the life of the
process.
"""
from __future__ import annotations

import importlib.util
import inspect
import os
from typing import Any, Callable


def envelope(kind: str, status: str, content: str = "", detail: str = "", data: Any = None) -> dict:
    """Shape a result as a kaiju ToolMessage. status is one of ok|empty|error."""
    msg: dict[str, Any] = {"kind": kind, "status": status}
    if content:
        msg["content"] = content
    if detail:
        msg["detail"] = detail
    if data is not None:
        msg["data"] = data
    return msg


class Registry:
    def __init__(self) -> None:
        self._plugins: list[dict] = []            # manifest entries, in load order
        self._invokers: dict[str, Callable] = {}  # tool name -> the plugin's invoke

    def manifest(self) -> dict:
        return {"plugins": self._plugins}

    def has_tool(self, tool: str) -> bool:
        return tool in self._invokers

    async def invoke(self, tool: str, params: dict) -> dict:
        fn = self._invokers[tool]
        try:
            result = fn(tool, params or {})
            if inspect.isawaitable(result):
                result = await result
        except Exception as e:  # a plugin crash is an error envelope, never a 500
            return envelope(tool, "error", detail=f"{type(e).__name__}: {e}")
        if not isinstance(result, dict) or "status" not in result:
            # Be forgiving: a plugin that returns a bare string becomes ok content.
            return envelope(tool, "ok", content=str(result))
        return result

    def _add(self, mod: Any, skill: str) -> None:
        man = getattr(mod, "MANIFEST", None)
        fn = getattr(mod, "invoke", None)
        if not isinstance(man, dict) or not callable(fn):
            return
        entry = {
            "name": man.get("name", "unnamed"),
            "description": man.get("description", ""),
            "skill": skill,
            "tools": man.get("tools", []),
        }
        self._plugins.append(entry)
        for tool in entry["tools"]:
            self._invokers[tool["name"]] = fn


def load_plugins(root: str) -> Registry:
    reg = Registry()
    for name in sorted(os.listdir(root)):
        pdir = os.path.join(root, name)
        pfile = os.path.join(pdir, "plugin.py")
        if not os.path.isfile(pfile):
            continue
        spec = importlib.util.spec_from_file_location(f"kaiju_plugin_{name}", pfile)
        if spec is None or spec.loader is None:
            continue
        mod = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(mod)  # imported once, at startup
        skill_path = os.path.join(pdir, "skill.md")
        skill = ""
        if os.path.isfile(skill_path):
            with open(skill_path, encoding="utf-8") as f:
                skill = f.read()
        reg._add(mod, skill)
    return reg
