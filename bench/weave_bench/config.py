"""``bench.toml`` + environment overrides.

The file names *which* environment variable holds each secret; values are
read from the process environment at launch and never written anywhere. Every
string/int field can also be overridden with ``WEAVE_BENCH_<SECTION>_<KEY>``
(e.g. ``WEAVE_BENCH_ROUTER_BASE_URL``), so a CI job needs no file at all.
"""

from __future__ import annotations

import os
import tomllib
from collections.abc import Mapping
from dataclasses import dataclass, fields
from enum import StrEnum
from pathlib import Path
from typing import get_type_hints

ENV_OVERRIDE_PREFIX = "WEAVE_BENCH"


class HarborEnvironment(StrEnum):
    """Harbor ``--env`` sandbox backends this harness supports."""

    DOCKER = "docker"
    MODAL = "modal"


@dataclass(frozen=True)
class RouterConfig:
    base_url: str = "http://localhost:8080"
    api_key_env: str = "WEAVE_ROUTER_API_KEY"
    # Read-only ``ra_`` analytics key (docs/ANALYTICS_EXPORT.md). Empty base URL
    # means "same host as the router".
    analytics_base_url: str = ""
    analytics_key_env: str = "WEAVE_ANALYTICS_KEY"

    @property
    def analytics_url(self) -> str:
        return self.analytics_base_url or self.base_url


@dataclass(frozen=True)
class ProviderConfig:
    # Atlas rubric judge key; the judge is served over any OpenAI-compatible
    # endpoint (default: AIand), so this defaults to the AIand key.
    judge_api_key_env: str = "AIAND_API_KEY"


@dataclass(frozen=True)
class HarborConfig:
    environment: HarborEnvironment = HarborEnvironment.DOCKER
    jobs_dir: str = "jobs"
    # SWE-Atlas checkout at the pinned commit (``weave-bench fetch atlas``).
    atlas_checkout_dir: str = "vendor/SWE-Atlas"


@dataclass(frozen=True)
class BenchConfig:
    router: RouterConfig = RouterConfig()
    providers: ProviderConfig = ProviderConfig()
    harbor: HarborConfig = HarborConfig()


Section = RouterConfig | ProviderConfig | HarborConfig
SECTION_NAMES = ("router", "providers", "harbor")


def _coerce(field_type: type, raw: object) -> object:
    if field_type is int:
        return int(str(raw))
    if field_type is HarborEnvironment:
        return HarborEnvironment(str(raw))
    return str(raw)


def _section[SectionT: Section](
    name: str, section_type: type[SectionT], document: Mapping[str, object], env: Mapping[str, str]
) -> SectionT:
    file_values = document.get(name, {})
    if not isinstance(file_values, dict):
        raise ValueError(f"[{name}] must be a table")
    field_types = get_type_hints(section_type)
    known = {f.name for f in fields(section_type)}
    unknown = set(file_values) - known
    if unknown:
        raise ValueError(f"[{name}] has unknown keys {sorted(unknown)}; known: {sorted(known)}")
    values = {key: _coerce(field_types[key], raw) for key, raw in file_values.items()}
    for key in known:
        override = env.get(f"{ENV_OVERRIDE_PREFIX}_{name.upper()}_{key.upper()}")
        if override is not None:
            values[key] = _coerce(field_types[key], override)
    return section_type(**values)


def load_config(path: Path | None, env: Mapping[str, str] = os.environ) -> BenchConfig:
    """Parse ``bench.toml`` (optional) and apply ``WEAVE_BENCH_*`` overrides."""
    document: dict[str, object] = tomllib.loads(path.read_text()) if path is not None else {}
    unknown_sections = set(document) - set(SECTION_NAMES)
    if unknown_sections:
        raise ValueError(f"unknown sections {sorted(unknown_sections)}; known: {list(SECTION_NAMES)}")
    return BenchConfig(
        router=_section("router", RouterConfig, document, env),
        providers=_section("providers", ProviderConfig, document, env),
        harbor=_section("harbor", HarborConfig, document, env),
    )


def require_secret(env_name: str, env: Mapping[str, str] = os.environ) -> str:
    value = env.get(env_name, "")
    if not value:
        raise RuntimeError(f"{env_name} is not set (named in bench.toml)")
    return value
