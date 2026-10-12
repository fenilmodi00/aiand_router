"""Codex ``config.toml`` for the router arms.

Codex has no env var for extra request headers: they only reach the wire
through a named ``model_providers`` block, which is why router arms ride on
Harbor's ``config`` agent kwarg rather than ``OPENAI_BASE_URL``.
The Bearer token still comes from ``OPENAI_API_KEY`` (Harbor sets it from the
arm's key), so the rendered file holds no secret.
"""

from __future__ import annotations

from enum import StrEnum

from weave_bench.arms import ArmSpec

ROUTER_CLIENT_APP_HEADER = "x-app"
ROUTER_CLIENT_APP = "weave-eval-codex"
ROUTER_ROLLOUT_ID_HEADER = "x-weave-rollout-id"
ROUTER_FORCE_MODEL_HEADER = "x-weave-force-model"
PROVIDER_ID = "weave"


class CodexProviderName(StrEnum):
    """Codex enables remote (same-model) compaction only for a provider named
    exactly ``OpenAI``; any other name compacts locally in a separate turn.
    The router serves ``/responses/compact``, so its arms keep that name."""

    OPENAI = "OpenAI"


def render_codex_config(
    *,
    base_url: str,
    provider_name: CodexProviderName,
    headers: dict[str, str],
) -> str:
    lines = [f'model_provider = "{PROVIDER_ID}"']
    lines += [
        "",
        f"[model_providers.{PROVIDER_ID}]",
        f'name = "{provider_name}"',
        f'base_url = "{base_url}"',
        'wire_api = "responses"',
        'env_key = "OPENAI_API_KEY"',
        "requires_openai_auth = true",
        "supports_websockets = false",
        "",
        f"[model_providers.{PROVIDER_ID}.http_headers]",
        *(f'{header} = "{value}"' for header, value in headers.items()),
        "",
    ]
    return "\n".join(lines)


def codex_config_for_arm(arm: ArmSpec, *, run_id: str, router_base_url: str) -> str:
    headers = {ROUTER_CLIENT_APP_HEADER: ROUTER_CLIENT_APP, ROUTER_ROLLOUT_ID_HEADER: run_id}
    if arm.force_model is not None:
        headers[ROUTER_FORCE_MODEL_HEADER] = arm.force_model
    return render_codex_config(
        base_url=f"{router_base_url.rstrip('/')}/v1",
        provider_name=CodexProviderName.OPENAI,
        headers=headers,
    )
