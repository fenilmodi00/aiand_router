from __future__ import annotations

import json
from datetime import timedelta
from pathlib import Path

import pytest

from tests.conftest import T0, TrialWriter
from weave_bench.arms import ARMS
from weave_bench.markdown import render_markdown
from weave_bench.report import build_report
from weave_bench.trials import load_job

TURN = {"type": "turn.completed", "usage": {"input_tokens": 1000, "cached_input_tokens": 600, "output_tokens": 50}}
BETA_ACK = '{"type":"item.completed","text":"Beta enabled. Type /beta again to turn it off."}\n'


def _analytics_row(session: str, model: str, cost: float) -> dict[str, object]:
    return {
        "id": hash((session, model, cost)) & 0xFFFF,
        "session_id": session,
        "requested_at": T0.isoformat(),
        "decision_model": model,
        "decision_provider": "aiand",
        "actual_input_cost_usd": cost,
        "actual_output_cost_usd": 0.0,
        "input_tokens": 1000,
        "cache_read_tokens": 600,
        "cache_creation_tokens": 0,
        "output_tokens": 50,
    }


@pytest.fixture
def two_arm_job(tmp_path: Path, write_trial: TrialWriter) -> dict[str, Path]:
    """router beats the force-pinned arm on t1, ties on t2, loses on t3; the
    force arm errors on t4 where router passes."""
    router_dir, force_dir = tmp_path / "run--router", tmp_path / "run--router-glm"
    outcomes = {"t1": (1.0, 0.0), "t2": (1.0, 1.0), "t3": (0.0, 1.0)}
    for offset, (task, (router_reward, force_reward)) in enumerate(outcomes.items()):
        started = T0 + timedelta(minutes=offset)
        write_trial(
            router_dir,
            task=task,
            trial_name=f"{task}__r1",
            reward=router_reward,
            started_at=started,
            session_id=f"router-{task}",
            beta_output=BETA_ACK,
            codex_events=(TURN,),
            agent_seconds=100 + offset,
        )
        write_trial(
            force_dir,
            task=task,
            trial_name=f"{task}__f1",
            reward=force_reward,
            started_at=started,
            session_id=f"force-{task}",
            codex_events=(TURN,),
            agent_cost_usd=2.0,
            agent_seconds=200,
        )
    write_trial(router_dir, task="t4", trial_name="t4__r1", reward=1.0, session_id="router-t4", beta_output="nope")
    write_trial(
        force_dir,
        task="t4",
        trial_name="t4__f1",
        reward=None,
        exception={"exception_type": "EnvironmentSetupError", "exception_message": "image build failed"},
    )
    return {"router": router_dir, "router-glm": force_dir}


def _report(two_arm_job: dict[str, Path], *, analytics_rows: list[dict[str, object]] | None):
    trials_by_arm = {ARMS[name]: load_job(ARMS[name], path) for name, path in two_arm_job.items()}
    return build_report(
        benchmark="terminal-bench-4",
        run_id="run",
        trials_by_arm=trials_by_arm,
        k=1,
        analytics_rows=analytics_rows,
    )


def test_pairing_statistics_and_error_categories(two_arm_job: dict[str, Path]) -> None:
    report = _report(two_arm_job, analytics_rows=None)
    router, force = report.arms
    assert (router.n_tasks, router.n_passed, force.n_passed, force.n_errored) == (4, 3, 2, 1)
    assert router.trial_pass_rate.point == pytest.approx(0.75)
    assert force.error_categories == {"harbor-environment-setup-failure": 1}
    assert router.n_beta_acknowledged == 3
    assert force.n_beta_acknowledged is None
    assert router.agent_seconds_median == 101.0
    assert router.codex_client_tokens.input_tokens == 1200  # Codex's input_tokens include the cached share
    assert router.codex_client_tokens.cache_read_tokens == 1800
    # No router-side rows: both arms keep Harbor's own (optional) cost figure.
    assert router.agent_cost_usd is None
    assert force.agent_cost_usd == pytest.approx(6.0)

    (pair,) = report.pairs
    assert (pair.router_arm, pair.control_arm) == ("router", "router-glm")
    assert pair.n_common_tasks == 4
    assert pair.delta_task_mean.point == pytest.approx(0.25)
    assert (pair.wins_ties_losses.wins, pair.wins_ties_losses.ties, pair.wins_ties_losses.losses) == (2, 1, 1)
    assert (pair.mcnemar.a_only, pair.mcnemar.b_only) == (2, 1)
    assert pair.cost_ratio is None


def test_router_billed_costs_stay_blank_without_analytics(two_arm_job: dict[str, Path]) -> None:
    router = _report(two_arm_job, analytics_rows=None).arms[0]
    assert router.router_billed_usd is None
    assert router.router_list_price_usd is None
    assert router.router_served_models == {}
    assert router.sessions_without_router_rows == []


def test_analytics_rows_join_on_codex_session(two_arm_job: dict[str, Path]) -> None:
    rows = [
        _analytics_row("router-t1", "zai-org/glm-5.3", 0.30),
        _analytics_row("router-t1", "qwen/qwen3.8-27b", 0.01),
        _analytics_row("router-t2", "zai-org/glm-5.3", 0.20),
        _analytics_row("force-t1", "zai-org/glm-5.3", 9.99),
    ]
    report = _report(two_arm_job, analytics_rows=rows)
    router, force = report.arms
    assert router.router_billed_usd == pytest.approx(0.51)
    assert router.router_served_models == {"zai-org/glm-5.3": 2, "qwen/qwen3.8-27b": 1}
    assert sorted(router.sessions_without_router_rows) == ["t3__r1", "t4__r1"]
    assert router.router_tokens is not None and router.router_tokens.requests == 3
    # aiand rows fold the 600 cached tokens into input_tokens (400 fresh each).
    assert router.router_tokens.input_tokens == 400 + 400 + 400
    # Both arms are router arms, so both join their own analytics rows.
    assert force.router_billed_usd == pytest.approx(9.99)
    assert force.router_served_models == {"zai-org/glm-5.3": 1}
    (pair,) = report.pairs
    # arm_spend_usd prefers router-billed: 0.51 (router arm) over 9.99 (force arm).
    assert pair.cost_ratio == pytest.approx(0.51 / 9.99)


def test_markdown_and_json_render_every_arm_and_pair(two_arm_job: dict[str, Path]) -> None:
    report = _report(two_arm_job, analytics_rows=None)
    markdown = render_markdown(report)
    assert "| `router` |" in markdown and "| `router-glm` |" in markdown
    assert "harbor-environment-setup-failure" in markdown
    assert "75.0%" in markdown
    assert "router-billed" not in markdown  # no analytics: nothing is presented as router-billed
    parsed = json.loads(report.to_json())
    assert [arm["arm"] for arm in parsed["arms"]] == ["router", "router-glm"]
    assert parsed["pairs"][0]["wins_ties_losses"] == {"wins": 2, "ties": 1, "losses": 1}


def test_single_arm_report_has_no_pairs(tmp_path: Path, write_trial: TrialWriter) -> None:
    job = tmp_path / "run--router"
    write_trial(job, task="t", trial_name="t__a", reward=1.0, session_id="sess")
    trials = load_job(ARMS["router"], job)
    report = build_report(
        benchmark="atlas-qna",
        run_id="run",
        trials_by_arm={ARMS["router"]: trials},
        k=1,
        analytics_rows=None,
    )
    (summary,) = report.arms
    assert report.pairs == []
    assert summary.router_billed_usd is None
    assert summary.arm == "router"
