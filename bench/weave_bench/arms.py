"""Comparison arms: which upstream serves Codex and how the model is pinned.

The router is the only upstream. A *router* arm sends Codex to the Weave router
and opens every trial with a ``/beta`` turn, so the router's beta policy picks
the model per request. A *router-force* arm also goes through the router but
pins the model with the ``x-weave-force-model`` header — the headless equivalent
of ``/force-model``.
"""

from __future__ import annotations

from dataclasses import dataclass
from enum import StrEnum


class Upstream(StrEnum):
    ROUTER = "router"
    ROUTER_FORCE = "router-force"


class ReasoningEffort(StrEnum):
    """Codex ``model_reasoning_effort``."""

    MEDIUM = "medium"
    HIGH = "high"
    MAX = "max"


class HarborModel(StrEnum):
    """Harbor ``--model`` ids. Harbor maps the ``aiand/`` prefix to
    ``OPENAI_API_KEY`` and strips it before handing the model to Codex."""

    GLM_5_3 = "aiand/zai-org/glm-5.3"


@dataclass(frozen=True)
class ArmSpec:
    name: str
    upstream: Upstream
    harbor_model: HarborModel
    reasoning_effort: ReasoningEffort = ReasoningEffort.HIGH

    @property
    def beta_preflight(self) -> bool:
        return self.upstream is Upstream.ROUTER

    @property
    def via_router(self) -> bool:
        """Traffic lands in the router's analytics export (billed cost, served model)."""
        return self.upstream in (Upstream.ROUTER, Upstream.ROUTER_FORCE)

    @property
    def codex_model(self) -> str:
        """The model name Codex itself sees (provider prefix stripped by Harbor)."""
        return self.harbor_model.split("/", 1)[1]

    @property
    def force_model(self) -> str | None:
        return self.codex_model if self.upstream is Upstream.ROUTER_FORCE else None


# Router arms still pass a model because Codex requires one; the router's beta
# policy ignores it and picks per request. GLM-5.3 is the force-pin target: a
# mid-cost roster model, so a full pinned arm stays affordable while still
# exercising the force-model header end to end.
ARMS: dict[str, ArmSpec] = {
    spec.name: spec
    for spec in (
        ArmSpec("router", Upstream.ROUTER, HarborModel.GLM_5_3),
        ArmSpec("router-glm", Upstream.ROUTER_FORCE, HarborModel.GLM_5_3),
    )
}

DEFAULT_ARMS = ("router", "router-glm")


def parse_arms(arms: str) -> tuple[ArmSpec, ...]:
    """Comma-separated ``--arms``; empty selects the default router-vs-force-pin pair."""
    names = tuple(name.strip() for name in arms.split(",")) if arms else DEFAULT_ARMS
    unknown = [name for name in names if name not in ARMS]
    if unknown:
        raise ValueError(f"unknown arms {unknown}; known: {sorted(ARMS)}")
    return tuple(ARMS[name] for name in names)
