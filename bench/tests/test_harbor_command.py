from __future__ import annotations

import shlex
import tomllib

import pytest

from weave_bench.arms import ARMS, DEFAULT_ARMS, parse_arms
from weave_bench.benchmarks import ATLAS_JUDGE_BASE_URL, ATLAS_JUDGE_MODEL, Benchmark
from weave_bench.codex_config import codex_config_for_arm
from weave_bench.config import load_config
from weave_bench.harbor_command import build_job
from weave_bench.launch import coordinator_env, plan, render_dry_run

CONFIG = load_config(None, env={"WEAVE_BENCH_ROUTER_BASE_URL": "https://router.example.test:8443/"})


def _flag_values(argv: tuple[str, ...], flag: str) -> list[str]:
    return [argv[i + 1] for i, arg in enumerate(argv) if arg == flag]


def _job(benchmark: Benchmark, arm_name: str, **overrides):
    kwargs = dict(run_id="run1", config=CONFIG, task_names=["a", "b"], n_attempts=2, n_concurrent=12)
    kwargs.update(overrides)
    return build_job(benchmark, ARMS[arm_name], **kwargs)


def test_arms_are_router_only() -> None:
    assert sorted(ARMS) == ["router", "router-glm"]
    assert list(DEFAULT_ARMS) == ["router", "router-glm"]
    assert all(ARMS[name].via_router for name in ARMS)


def test_router_beta_arm_pins_versions_and_toggles_beta() -> None:
    job = _job(Benchmark.TERMINAL_BENCH_4, "router")
    argv = job.argv
    assert argv[:2] == ("harbor", "run")
    assert _flag_values(argv, "--dataset") == ["terminal-bench/terminal-bench@4.0.0"]
    assert _flag_values(argv, "--agent") == ["weave_bench.agent.beta_codex:BetaCodex"]
    assert _flag_values(argv, "--model") == ["aiand/zai-org/glm-5.3"]
    assert _flag_values(argv, "--n-attempts") == ["2"] and _flag_values(argv, "--n-concurrent") == ["12"]
    assert _flag_values(argv, "--job-name") == ["run1--router"]
    kwargs = _flag_values(argv, "--agent-kwarg")
    assert "version=0.150.0" in kwargs and "reasoning_effort=high" in kwargs and "beta_toggle=true" in kwargs
    assert _flag_values(argv, "--agent-env") == ["OPENAI_API_KEY=${WEAVE_BENCH_AGENT_API_KEY}"]
    assert _flag_values(argv, "--allow-agent-host") == ["router.example.test"]
    assert _flag_values(argv, "--include-task-name") == ["a", "b"]
    assert argv[-4:-2] == ("--yes", "--quiet") or ("--yes" in argv and "--quiet" in argv)
    assert job.env_from == {"WEAVE_BENCH_AGENT_API_KEY": "WEAVE_ROUTER_API_KEY"}
    assert job.codex_config is not None
    provider = tomllib.loads(job.codex_config)["model_providers"]["weave"]
    assert provider["base_url"] == "https://router.example.test:8443/v1"
    assert provider["name"] == "OpenAI"
    assert provider["http_headers"] == {"x-app": "weave-eval-codex", "x-weave-rollout-id": "run1"}
    assert "web_search" not in tomllib.loads(job.codex_config)


def test_force_model_arm_pins_the_header_without_beta() -> None:
    job = _job(Benchmark.TERMINAL_BENCH_4, "router-glm")
    assert "beta_toggle=true" not in job.argv
    assert job.env_from == {"WEAVE_BENCH_AGENT_API_KEY": "WEAVE_ROUTER_API_KEY"}
    headers = tomllib.loads(job.codex_config or "")["model_providers"]["weave"]["http_headers"]
    assert headers["x-weave-force-model"] == "zai-org/glm-5.3"
    assert _flag_values(job.argv, "--model") == ["aiand/zai-org/glm-5.3"]
    assert (
        codex_config_for_arm(ARMS["router-glm"], run_id="run1", router_base_url="https://router.example.test:8443/")
        == job.codex_config
    )


def test_every_arm_writes_a_codex_config_pointing_at_the_router() -> None:
    for name, arm in ARMS.items():
        job = _job(Benchmark.TERMINAL_BENCH_4, name)
        assert job.codex_config_path is not None and job.codex_config is not None
        provider = tomllib.loads(job.codex_config)["model_providers"]["weave"]
        assert provider["base_url"] == "https://router.example.test:8443/v1"
        assert job.env_from["WEAVE_BENCH_AGENT_API_KEY"] == "WEAVE_ROUTER_API_KEY"


def test_atlas_uses_local_checkout_and_rubric_judge_env() -> None:
    job = _job(Benchmark.ATLAS_QNA, "router", codex_version="0.160.0")
    assert _flag_values(job.argv, "--path") == ["vendor/SWE-Atlas/data/qa"]
    assert "--dataset" not in job.argv
    assert "version=0.160.0" in _flag_values(job.argv, "--agent-kwarg")
    verifier_env = _flag_values(job.argv, "--verifier-env")
    assert f"EVAL_MODEL={ATLAS_JUDGE_MODEL}" in verifier_env
    assert f"EVAL_BASE_URL={ATLAS_JUDGE_BASE_URL}" in verifier_env
    assert "EVAL_API_KEY=${WEAVE_BENCH_JUDGE_API_KEY}" in verifier_env
    assert job.env_from["WEAVE_BENCH_JUDGE_API_KEY"] == "AIAND_API_KEY"


def test_coordinator_env_resolves_keys_and_strips_base_url_overrides() -> None:
    job = _job(Benchmark.ATLAS_QNA, "router")
    env = coordinator_env(
        job,
        env={"WEAVE_ROUTER_API_KEY": "rk", "AIAND_API_KEY": "ak", "OPENAI_BASE_URL": "http://leak", "PATH": "/bin"},
    )
    assert env["WEAVE_BENCH_AGENT_API_KEY"] == "rk"
    assert env["WEAVE_BENCH_JUDGE_API_KEY"] == "ak"
    assert "OPENAI_BASE_URL" not in env and env["PATH"] == "/bin"
    with pytest.raises(RuntimeError, match="AIAND_API_KEY"):
        coordinator_env(job, env={"WEAVE_ROUTER_API_KEY": "rk"})


def test_plan_smoke_and_dry_run_never_leak_secret_values() -> None:
    launch = plan(
        Benchmark.TERMINAL_BENCH_4,
        parse_arms("router,router-glm"),
        run_id="smoke",
        config=CONFIG,
        smoke=True,
        n_attempts=None,
        n_concurrent=3,
        codex_version=None,
        task_names=None,
    )
    assert launch.task_names == ("terminal-bench/cad-model",)
    assert [job.arm.name for job in launch.jobs] == ["router", "router-glm"]
    rendered = render_dry_run(launch)
    assert "tasks=1 (manifest has 66)" in rendered
    assert "harbor=0.22.0" in rendered and "codex=0.150.0" in rendered
    assert 'export WEAVE_BENCH_AGENT_API_KEY="$WEAVE_ROUTER_API_KEY"' in rendered
    assert shlex.join(launch.jobs[0].argv) in rendered
    assert "--n-concurrent 3" in rendered


def test_parse_arms_trims_and_rejects_unknown() -> None:
    assert [arm.name for arm in parse_arms("router, router-glm")] == ["router", "router-glm"]
    with pytest.raises(ValueError):
        parse_arms("router,nope")
