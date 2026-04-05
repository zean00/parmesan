import asyncio
import json
import os
import re
import sys
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path
from types import MethodType
from typing import Any

PARLANT_ROOT = Path.cwd()
if str(PARLANT_ROOT) not in sys.path:
    sys.path.insert(0, str(PARLANT_ROOT))
RUNNER_DIR = Path(__file__).resolve().parent
if str(RUNNER_DIR) not in sys.path:
    sys.path.insert(0, str(RUNNER_DIR))

from parlant.core.agents import AgentStore, CompositionMode
from parlant.core.customers import CustomerStore
from parlant.core.emission.event_buffer import EventBuffer
from parlant.core.canned_responses import CannedResponseField, CannedResponseStore
from parlant.core.engines.alpha.canned_response_generator import DEFAULT_NO_MATCH_CANREP
from parlant.core.engines.alpha.engine import AlphaEngine
from parlant.core.engines.alpha.planners import BasicPlan, NullPlan
from parlant.core.engines.alpha.guideline_matching.generic.journey.journey_backtrack_check import (
    JourneyBacktrackCheck,
)
from parlant.core.engines.alpha.guideline_matching.generic.journey.journey_backtrack_node_selection import (
    JourneyBacktrackNodeSelection,
)
from parlant.core.engines.alpha.guideline_matching.generic.journey.journey_next_step_selection import (
    JourneyNextStepSelection,
)
from parlant.core.evaluations import JourneyPayload, PayloadOperation
from parlant.core.guidelines import Guideline, GuidelineStore
from parlant.core.guideline_tool_associations import GuidelineToolAssociationStore
from parlant.core.journeys import Journey, JourneyStore
from parlant.core.relationships import (
    RelationshipEntity,
    RelationshipEntityKind,
    RelationshipKind,
    RelationshipStore,
)
from parlant.core.sessions import (
    AgentState,
    EventKind,
    EventSource,
    SessionStore,
    SessionUpdateParams,
    ToolEventData,
)
from parlant.core.tags import Tag, TagStore
from parlant.core.tools import LocalToolService, ToolId, ToolOverlap
from parlant.core.entity_cq import EntityCommands
from parlant.core.loggers import LogLevel, StdoutLogger
from parlant.core.tracer import LocalTracer
from parlant.core.meter import LocalMeter
from parlant.core.engines.types import Context
from parlant.core.nlp.generation import SchematicGenerator
from parlant.core.services.indexing.behavioral_change_evaluation import JourneyEvaluator
from parlant.adapters.nlp.openrouter_service import OpenRouterService

import tests.conftest as parlant_conftest
from tests.conftest import CacheOptions, container as make_container
from tests.core.common.utils import ContextOfTest
from tests.test_utilities import (
    SyncAwaiter,
    JournalingEngineHooks,
    create_schematic_generation_result_collection,
)
from tests.core.common.engines.alpha.steps.guidelines import (
    get_guideline_properties,
)
from tests.core.common.engines.alpha.steps.tools import (
    TOOLS,
)


@dataclass
class ScenarioRunContext:
    sync_await: SyncAwaiter
    container: Any
    context: ContextOfTest
    agent_id: Any
    customer_id: Any
    session_id: Any
    latest_resolver_result: Any | None = None
    latest_response_analysis_result: Any | None = None
    latest_tool_inference_result: Any | None = None
    latest_tool_batches: list[Any] | None = None
    latest_tool_batch_results: list[dict[str, Any]] | None = None
    latest_journey_batch_results: list[dict[str, Any]] | None = None
    latest_backtrack_checks: list[dict[str, Any]] | None = None
    latest_backtrack_node_selections: list[dict[str, Any]] | None = None
    latest_next_step_selections: list[dict[str, Any]] | None = None


def normalize_mode(mode: str) -> str:
    mode = (mode or "").strip().lower()
    if mode in ("strict", "canned_strict"):
        return CompositionMode.CANNED_STRICT.value
    return CompositionMode.CANNED_FLUID.value


def extract_canned_response_fields(text: str) -> list[CannedResponseField]:
    names: list[str] = []
    for match in re.findall(r"\{\{\s*([^{}]+?)\s*\}\}", text or ""):
        name = (match or "").strip()
        if not name:
            continue
        if name not in names:
            names.append(name)
    return [CannedResponseField(name=name, description="", examples=[]) for name in names]


def create_run_context(scenario: dict[str, Any]) -> tuple[ScenarioRunContext, Any, Any | None]:
    loop = asyncio.new_event_loop()
    asyncio.set_event_loop(loop)
    sync_await = SyncAwaiter(loop)
    tracer = LocalTracer()
    logger = StdoutLogger(tracer=tracer, log_level=LogLevel.WARNING)
    cache_collection_gen = None
    cache_collection = None
    cache_file_exists = (PARLANT_ROOT / "schematic_generation_test_cache.json").exists()
    if cache_file_exists:
        cache_collection_gen = create_schematic_generation_result_collection(logger=logger)
        cache_collection = sync_await(cache_collection_gen.__aenter__())
    cache_options = CacheOptions(
        cache_enabled=cache_collection is not None,
        cache_schematic_generation_collection=cache_collection,
    )
    if os.environ.get("OPENROUTER_API_KEY"):
        class EmcieCompatibleOpenRouterGenerator:
            def __init__(self, base_generator: SchematicGenerator[Any], emcie_id: str):
                self._base_generator = base_generator
                self._emcie_id = emcie_id

            async def generate(self, prompt, hints={}):
                return await self._base_generator.generate(prompt, hints)

            @property
            def id(self) -> str:
                return self._emcie_id

            @property
            def schema(self):
                return self._base_generator.schema

            @property
            def tokenizer(self):
                return self._base_generator.tokenizer

            @property
            def max_tokens(self) -> int:
                return self._base_generator.max_tokens

        class CompatOpenRouterService(OpenRouterService):
            def __init__(self, logger, tracer, meter, model_tier=None, model_role=None):
                super().__init__(logger, tracer, meter)
                self._emcie_model_tier = (model_tier or os.environ.get("EMCIE_MODEL_TIER") or "jackal").strip()
                self._emcie_model_role = (model_role or os.environ.get("EMCIE_MODEL_ROLE") or "teacher").strip()
                if self._emcie_model_role == "teacher":
                    self.model_name = "openai/gpt-4o"
                elif self._emcie_model_role == "student":
                    self.model_name = "openai/gpt-4o-mini"

            async def get_schematic_generator(self, t, hints={}):
                generator = await super().get_schematic_generator(t, hints)
                return EmcieCompatibleOpenRouterGenerator(
                    generator,
                    emcie_id=f"emcie/{self._emcie_model_tier}",
                )

        parlant_conftest.EmcieService = CompatOpenRouterService
        parlant_conftest.OpenRouterService = CompatOpenRouterService
    container_factory = getattr(make_container, "__wrapped__", make_container)
    container_gen = container_factory(tracer, logger, cache_options)
    container = sync_await(container_gen.__anext__())

    ctx = ContextOfTest(
        sync_await=sync_await,
        container=container,
        events=[],
        guidelines={},
        guideline_matches={},
        tools={},
        actions=[],
        journeys={},
        nodes={},
    )
    agent_name = (scenario.get("agent_name") or "parity-agent").strip() or "parity-agent"
    agent_job = (scenario.get("agent_job") or "").strip()
    agent_kwargs = {
        "name": agent_name,
        "max_engine_iterations": 2,
        "composition_mode": CompositionMode(normalize_mode(scenario.get("mode", ""))),
    }
    if agent_job:
        agent_kwargs["description"] = f"Your job is {agent_job}"
    agent = sync_await(container[AgentStore].create_agent(**agent_kwargs))
    customer = sync_await(
        container[CustomerStore].create_customer(name="Parity Customer", extra={"email": "parity@example.com"})
    )
    session = sync_await(container[SessionStore].create_session(customer.id, agent.id))
    agent_id = agent.id
    customer_id = customer.id
    session_id = session.id
    run = ScenarioRunContext(sync_await, container, ctx, agent_id, customer_id, session_id)
    instrument_engine(run)
    return run, container_gen, cache_collection_gen


def instrument_engine(run: ScenarioRunContext) -> None:
    engine = run.container[AlphaEngine]
    run.latest_journey_batch_results = []
    run.latest_backtrack_checks = []
    run.latest_backtrack_node_selections = []
    run.latest_next_step_selections = []

    original_resolve = engine._relational_resolver.resolve

    async def resolve_with_capture(self, *args, **kwargs):
        result = await original_resolve(*args, **kwargs)
        run.latest_resolver_result = result
        return result

    engine._relational_resolver.resolve = MethodType(resolve_with_capture, engine._relational_resolver)

    original_analyze = engine._guideline_matcher.analyze_response

    async def analyze_with_capture(self, *args, **kwargs):
        result = await original_analyze(*args, **kwargs)
        run.latest_response_analysis_result = result
        return result

    engine._guideline_matcher.analyze_response = MethodType(analyze_with_capture, engine._guideline_matcher)

    tool_event_generator = engine._tool_event_generator
    tool_caller = tool_event_generator._tool_caller
    batcher = tool_caller.batcher

    tool_event_generator_cls = type(tool_event_generator)
    tool_caller_cls = type(tool_caller)
    batcher_cls = type(batcher)

    original_infer_tool_calls = tool_event_generator_cls.infer_tool_calls
    original_generate_tool_events = tool_event_generator_cls.generate_events
    original_tool_caller_do_infer = tool_caller_cls._do_infer_tool_calls
    original_create_batches = batcher_cls.create_batches
    original_nullplan_on_tools_inferred = NullPlan.on_tools_inferred
    original_basicplan_on_tools_inferred = BasicPlan.on_tools_inferred

    async def infer_tool_calls_with_capture(self, *args, **kwargs):
        result = await original_infer_tool_calls(self, *args, **kwargs)
        run.latest_tool_inference_result = result
        return result

    tool_event_generator_cls.infer_tool_calls = infer_tool_calls_with_capture

    async def generate_events_with_capture(self, *args, **kwargs):
        result = await original_generate_tool_events(self, *args, **kwargs)
        setattr(run, "latest_tool_event_generation_result", result)
        return result

    tool_event_generator_cls.generate_events = generate_events_with_capture

    async def do_infer_tool_calls_with_capture(self, *args, **kwargs):
        result = await original_tool_caller_do_infer(self, *args, **kwargs)
        run.latest_tool_inference_result = result
        return result

    tool_caller_cls._do_infer_tool_calls = do_infer_tool_calls_with_capture

    async def create_batches_with_capture(self, *args, **kwargs):
        batches = await original_create_batches(self, *args, **kwargs)
        run.latest_tool_batches = list(batches)
        run.latest_tool_batch_results = []
        for batch in batches:
            if hasattr(batch, "_run_consequential_tool_inference"):
                original_run_consequential = batch._run_consequential_tool_inference

                async def run_consequential_with_capture(self, *args, _orig=original_run_consequential, **kwargs):
                    generation_info, output = await _orig(*args, **kwargs)
                    setattr(self, "_parity_inference_output", output)
                    return generation_info, output

                batch._run_consequential_tool_inference = MethodType(
                    run_consequential_with_capture, batch
                )
            if hasattr(batch, "_run_non_consequential_tool_inference"):
                original_run_non_consequential = batch._run_non_consequential_tool_inference

                async def run_non_consequential_with_capture(self, *args, _orig=original_run_non_consequential, **kwargs):
                    generation_info, output = await _orig(*args, **kwargs)
                    setattr(self, "_parity_inference_output", output)
                    return generation_info, output

                batch._run_non_consequential_tool_inference = MethodType(
                    run_non_consequential_with_capture, batch
                )
            if hasattr(batch, "_run_inference"):
                original_run_inference = batch._run_inference

                async def run_inference_with_capture(self, *args, _orig=original_run_inference, **kwargs):
                    generation_info, output = await _orig(*args, **kwargs)
                    setattr(self, "_parity_inference_output", output)
                    return generation_info, output

                batch._run_inference = MethodType(run_inference_with_capture, batch)
            original_batch_process = batch.process

            async def process_with_capture(self, _orig=original_batch_process, _batch=batch):
                result = await _orig()
                (run.latest_tool_batch_results or []).append(
                    capture_tool_batch_result(_batch, result)
                )
                return result

            batch.process = MethodType(process_with_capture, batch)
        return batches

    batcher_cls.create_batches = create_batches_with_capture

    async def nullplan_on_tools_inferred_with_capture(self, context, inference_result):
        run.latest_tool_inference_result = inference_result
        tool_calls = await original_nullplan_on_tools_inferred(self, context, inference_result)
        setattr(run, "latest_planned_tool_calls", list(tool_calls))
        return tool_calls

    NullPlan.on_tools_inferred = nullplan_on_tools_inferred_with_capture

    async def basicplan_on_tools_inferred_with_capture(self, context, inference_result):
        run.latest_tool_inference_result = inference_result
        tool_calls = await original_basicplan_on_tools_inferred(self, context, inference_result)
        setattr(run, "latest_planned_tool_calls", list(tool_calls))
        return tool_calls

    BasicPlan.on_tools_inferred = basicplan_on_tools_inferred_with_capture

    original_backtrack_check_process = JourneyBacktrackCheck.process

    async def backtrack_check_process_with_capture(self, *args, **kwargs):
        original_generate = self._schematic_generator.generate

        async def generate_with_capture(*g_args, **g_kwargs):
            inference = await original_generate(*g_args, **g_kwargs)
            (run.latest_backtrack_checks or []).append(
                {
                    "journey_title": self._examined_journey.title,
                    "journey_id": str(self._examined_journey.id),
                    "content": inference.content.model_dump(),
                }
            )
            return inference

        self._schematic_generator.generate = generate_with_capture
        try:
            return await original_backtrack_check_process(self, *args, **kwargs)
        finally:
            self._schematic_generator.generate = original_generate

    JourneyBacktrackCheck.process = backtrack_check_process_with_capture

    original_backtrack_node_process = JourneyBacktrackNodeSelection.process

    async def backtrack_node_process_with_capture(self, *args, **kwargs):
        original_generate = self._schematic_generator.generate

        async def generate_with_capture(*g_args, **g_kwargs):
            inference = await original_generate(*g_args, **g_kwargs)
            (run.latest_backtrack_node_selections or []).append(
                {
                    "journey_title": self._examined_journey.title,
                    "journey_id": str(self._examined_journey.id),
                    "content": inference.content.model_dump(),
                }
            )
            return inference

        self._schematic_generator.generate = generate_with_capture
        try:
            return await original_backtrack_node_process(self, *args, **kwargs)
        finally:
            self._schematic_generator.generate = original_generate

    JourneyBacktrackNodeSelection.process = backtrack_node_process_with_capture

    original_next_step_process = JourneyNextStepSelection.process

    async def next_step_process_with_capture(self, *args, **kwargs):
        original_generate = self._schematic_generator.generate

        async def generate_with_capture(*g_args, **g_kwargs):
            inference = await original_generate(*g_args, **g_kwargs)
            (run.latest_next_step_selections or []).append(
                {
                    "journey_title": self._examined_journey.title,
                    "journey_id": str(self._examined_journey.id),
                    "content": inference.content.model_dump(),
                }
            )
            return inference

        self._schematic_generator.generate = generate_with_capture
        try:
            return await original_next_step_process(self, *args, **kwargs)
        finally:
            self._schematic_generator.generate = original_generate

    JourneyNextStepSelection.process = next_step_process_with_capture

    for strategy in getattr(engine._guideline_matcher, "_strategies", []) or []:
        original_matching_batches = getattr(strategy, "create_matching_batches", None)
        if not callable(original_matching_batches):
            continue

        async def create_matching_batches_with_capture(self, *args, __orig=original_matching_batches, **kwargs):
            batches = await __orig(*args, **kwargs)
            for batch in batches or []:
                if batch.__class__.__name__ != "GenericJourneyNodeSelectionBatch":
                    continue
                original_process = batch.process

                async def process_with_capture(self, _orig=original_process, _batch=batch):
                    result = await _orig()
                    (run.latest_journey_batch_results or []).append(
                        capture_journey_batch_result(_batch, result)
                    )
                    return result

                batch.process = MethodType(process_with_capture, batch)
            return batches

        strategy.create_matching_batches = MethodType(create_matching_batches_with_capture, strategy)


def tool_id_to_string(value: Any) -> str:
    if value is None:
        return ""
    if isinstance(value, str):
        return value
    if hasattr(value, "to_string"):
        return value.to_string()
    service = getattr(value, "service_name", "")
    name = getattr(value, "tool_name", "")
    if service and name:
        return f"{service}:{name}"
    return str(value)


def capture_tool_batch_result(batch: Any, result: Any) -> dict[str, Any]:
    candidate_tools: list[str] = []
    batch_type = "unknown"
    if hasattr(batch, "_candidate_tool"):
        batch_type = "single_tool"
        candidate_tools = [tool_id_to_string(batch._candidate_tool[0])]
    elif hasattr(batch, "_overlapping_tools_batch"):
        batch_type = "overlapping_tools"
        candidate_tools = [tool_id_to_string(item[0]) for item in batch._overlapping_tools_batch]
    selected_tools = [tool_id_to_string(call.tool_id) for call in getattr(result, "tool_calls", []) or []]
    evaluations = {
        tool_id_to_string(tool_id): getattr(evaluation, "value", str(evaluation))
        for tool_id, evaluation in (getattr(getattr(result, "insights", None), "evaluations", []) or [])
    }
    return {
        "type": batch_type,
        "candidate_tools": candidate_tools,
        "selected_tools": selected_tools,
        "evaluations": evaluations,
        "inference_output": getattr(batch, "_parity_inference_output", None),
    }


def capture_journey_batch_result(batch: Any, result: Any) -> dict[str, Any]:
    journey = getattr(batch, "_examined_journey", None)
    matches = getattr(result, "matches", []) or []
    return {
        "journey_title": getattr(journey, "title", ""),
        "journey_id": str(getattr(journey, "id", "") or ""),
        "previous_path": list(getattr(batch, "_previous_path", []) or []),
        "match_ids": [str(getattr(getattr(item, "guideline", None), "id", "") or "") for item in matches],
        "match_metadata": [dict(getattr(item, "metadata", {}) or {}) for item in matches],
        "match_rationales": [str(getattr(item, "rationale", "") or "") for item in matches],
    }


def append_transcript(run: ScenarioRunContext, transcript: list[dict[str, str]]) -> None:
    agent = run.sync_await(run.container[AgentStore].read_agent(run.agent_id))
    customer = run.sync_await(run.container[CustomerStore].read_customer(run.customer_id))
    for item in transcript:
        source = EventSource.CUSTOMER
        participant_id = customer.id
        participant_name = customer.name
        if item["role"] != "customer":
            source = EventSource.AI_AGENT
            participant_id = agent.id
            participant_name = agent.name
        event = run.sync_await(
            run.container[SessionStore].create_event(
                session_id=run.session_id,
                source=source,
                kind=EventKind.MESSAGE,
                trace_id="<main>",
                data={
                    "message": item["text"],
                    "participant": {
                        "id": participant_id,
                        "display_name": participant_name,
                    },
                },
            )
        )
        run.context.events.append(event)


def append_staged_tool_calls(run: ScenarioRunContext, scenario: dict[str, Any]) -> None:
    setup = scenario.get("policy_setup") or {}
    staged_tool_calls = setup.get("staged_tool_calls") or []
    if not staged_tool_calls:
        return
    for item in staged_tool_calls:
        tool_id = (item.get("tool_id") or "").strip()
        if tool_id.startswith("local:"):
            _, tool_name = split_tool_id(tool_id)
            ensure_local_tool(run, tool_name)
        event = run.sync_await(
            run.container[SessionStore].create_event(
                session_id=run.session_id,
                source=EventSource.AI_AGENT,
                kind=EventKind.TOOL,
                trace_id="<main>",
                data={
                    "tool_calls": [
                        {
                            "tool_id": tool_id,
                            "arguments": item.get("arguments") or {},
                            "result": item.get("result") or {"data": None, "metadata": {}, "control": {}},
                        }
                    ]
                },
            )
        )
        run.context.events.append(event)


def create_guideline_direct(
    run: ScenarioRunContext,
    guideline_name: str,
    condition: str,
    action: str | None,
    priority: int = 0,
    tags: list[str] | None = None,
) -> Guideline:
    metadata = {}
    try:
        metadata = get_guideline_properties(run.context, condition, action)
    except Exception:
        metadata = {}
    guideline = run.sync_await(
        run.container[GuidelineStore].create_guideline(
            condition=condition,
            action=action,
            metadata=metadata,
            priority=priority,
        )
    )
    run.sync_await(
        run.container[GuidelineStore].upsert_tag(
            guideline.id,
            Tag.for_agent_id(run.agent_id).id,
        )
    )
    for tag_name in tags or []:
        tag = ensure_named_tag(run, tag_name)
        run.sync_await(
            run.container[GuidelineStore].upsert_tag(
                guideline.id,
                tag.id,
            )
        )
    run.context.guidelines[guideline_name] = guideline
    return guideline


def guideline_signature(condition: str, action: str | None) -> tuple[str, str]:
    return ((condition or "").strip(), (action or "").strip())


def build_guideline_id_lookup(run: ScenarioRunContext, scenario: dict[str, Any] | None = None) -> dict[str, str]:
    lookup: dict[str, str] = {
        str(guideline.id): name
        for name, guideline in run.context.guidelines.items()
    }
    setup = (scenario or {}).get("policy_setup") or {}
    signatures: dict[tuple[str, str], str] = {}
    for item in setup.get("extra_guidelines") or []:
        signatures[guideline_signature(item.get("condition") or "", item.get("action"))] = item["id"]
    if not signatures:
        return lookup
    try:
        guidelines = run.sync_await(run.container[GuidelineStore].list_guidelines())
    except Exception:
        return lookup
    for guideline in guidelines:
        name = signatures.get(
            guideline_signature(
                getattr(getattr(guideline, "content", None), "condition", "") or "",
                getattr(getattr(guideline, "content", None), "action", None),
            )
        )
        if name:
            lookup[str(guideline.id)] = name
    return lookup


def ensure_named_tag(run: ScenarioRunContext, tag_name: str) -> Tag:
    tag_name = (tag_name or "").strip()
    if not tag_name:
        raise RuntimeError("empty tag name in parity helper")
    cache = getattr(ensure_named_tag, "_cache", {})
    run_cache = cache.setdefault(run.session_id, {})
    if tag_name in run_cache:
        return run_cache[tag_name]
    tag_store = run.container[TagStore]
    existing = run.sync_await(tag_store.list_tags(name=tag_name))
    if existing:
        tag = existing[0]
    else:
        tag = run.sync_await(tag_store.create_tag(name=tag_name))
    run_cache[tag_name] = tag
    setattr(ensure_named_tag, "_cache", cache)
    return tag


def relationship_entity_from_fixture(
    run: ScenarioRunContext,
    raw: str,
    scenario: dict[str, Any],
) -> RelationshipEntity | None:
    value = (raw or "").strip()
    if not value:
        return None
    if value.startswith("journey:"):
        journey_title = value.split(":", 1)[1]
        ensure_named_journey(run, journey_title, scenario)
        return RelationshipEntity(
            id=Tag.for_journey_id(run.context.journeys[journey_title].id).id,
            kind=RelationshipEntityKind.TAG_ALL,
        )
    if value.startswith("tag_all:"):
        tag = ensure_named_tag(run, value.split(":", 1)[1])
        return RelationshipEntity(id=tag.id, kind=RelationshipEntityKind.TAG_ALL)
    if value.startswith("tag_any:"):
        tag = ensure_named_tag(run, value.split(":", 1)[1])
        return RelationshipEntity(id=tag.id, kind=RelationshipEntityKind.TAG_ANY)
    if value in run.context.guidelines:
        return RelationshipEntity(
            id=run.context.guidelines[value].id,
            kind=RelationshipEntityKind.GUIDELINE,
        )
    return None


def ensure_local_tool(run: ScenarioRunContext, tool_name: str) -> None:
    if tool_name in run.context.tools:
        return
    local_tool_service = run.container[LocalToolService]
    spec = TOOLS.get(tool_name)
    if not spec:
        spec = {
            "name": tool_name,
            "description": "",
            "module_path": local_tool_module_path(tool_name),
            "parameters": {},
            "required": [],
        }
    elif not local_tool_exists_in_module(spec.get("module_path") or "", spec.get("name") or tool_name):
        spec = dict(spec)
        spec["module_path"] = local_tool_module_path(tool_name)
    existing = run.sync_await(local_tool_service.list_tools())
    tool = next((item for item in existing if item.name == tool_name), None)
    if not tool:
        tool = run.sync_await(local_tool_service.create_tool(**spec))
    run.context.tools[tool_name] = tool


def tool_spec_from_fixture(item: dict[str, Any]) -> dict[str, Any]:
    schema = item.get("schema") or {}
    parameters = dict(schema.get("properties") or {})
    required = list(schema.get("required") or [])
    tool_name = item["id"].split(":", 1)[-1] if ":" in item["id"] else item["id"]
    parameters, required = strip_runtime_tool_parameters(parameters, required)
    return {
        "name": tool_name,
        "description": item.get("description") or "",
        "module_path": "parity_tool_utilities",
        "parameters": parameters,
        "required": required,
        "consequential": bool(item.get("consequential")),
        "overlap": ToolOverlap.AUTO,
    }


def local_tool_module_path(tool_name: str) -> str:
    if local_tool_exists_in_module("tests.tool_utilities", tool_name):
        return "tests.tool_utilities"
    return "parity_tool_utilities"


def local_tool_exists_in_module(module_path: str, tool_name: str) -> bool:
    try:
        module = __import__(module_path, fromlist=[tool_name])
    except Exception:
        return False
    return hasattr(module, tool_name)


def strip_runtime_tool_parameters(
    parameters: dict[str, Any], required: list[str]
) -> tuple[dict[str, Any], list[str]]:
    runtime_params = {
        "session_id",
        "customer_message",
        "conversation_excerpt",
        "journey_id",
        "journey_state",
    }
    filtered_parameters = {
        key: value
        for key, value in parameters.items()
        if key not in runtime_params
    }
    filtered_required = [item for item in required if item not in runtime_params]
    return filtered_parameters, filtered_required


def create_chat_node(
    run: ScenarioRunContext,
    journey: Journey,
    action: str,
    customer_action: str = "",
) -> Any:
    journey_store = run.container[JourneyStore]
    node = run.sync_await(
        journey_store.create_node(
            journey_id=journey.id,
            action=action,
            tools=[],
        )
    )
    run.sync_await(
        journey_store.set_node_metadata(
            node.id,
            "journey_node",
            {"kind": "chat"},
        )
    )
    customer_dependent_data = infer_chat_node_dependency_metadata(action, customer_action)
    if customer_dependent_data:
        run.sync_await(
            journey_store.set_node_metadata(
                node.id,
                "customer_dependent_action_data",
                customer_dependent_data,
            )
        )
    return node


def infer_chat_node_dependency_metadata(action: str, customer_action: str) -> dict[str, Any]:
    action = (action or "").strip()
    customer_action = (customer_action or "").strip()
    lowered = action.lower()
    if customer_action:
        return {
            "is_customer_dependent": True,
            "customer_action": customer_action,
            "agent_action": "",
        }
    customer_prefixes = (
        "ask ",
        "what",
        "when",
        "where",
        "which",
        "who",
        "can you",
        "could you",
        "please provide",
        "confirm ",
    )
    if "?" in action or lowered.startswith(customer_prefixes):
        return {
            "is_customer_dependent": True,
            "customer_action": action,
            "agent_action": "",
        }
    return {
        "is_customer_dependent": False,
        "customer_action": "",
        "agent_action": action,
    }


def create_tool_node(
    run: ScenarioRunContext,
    journey: Journey,
    tool_name: str,
    action: str,
) -> Any:
    ensure_local_tool(run, tool_name)
    journey_store = run.container[JourneyStore]
    relationship_store = run.container[RelationshipStore]
    node = run.sync_await(
        journey_store.create_node(
            journey_id=journey.id,
            action=action,
            tools=[ToolId("local", tool_name)],
        )
    )
    run.sync_await(journey_store.set_node_metadata(node.id, "tool_running_only", True))
    run.sync_await(
        journey_store.set_node_metadata(
            node.id,
            "journey_node",
            {"kind": "tool"},
        )
    )
    run.sync_await(
        relationship_store.create_relationship(
            source=RelationshipEntity(
                id=Tag.for_journey_node_id(node.id).id,
                kind=RelationshipEntityKind.TAG_ALL,
            ),
            target=RelationshipEntity(
                id=ToolId("local", tool_name),
                kind=RelationshipEntityKind.TOOL,
            ),
            kind=RelationshipKind.REEVALUATION,
        )
    )
    return node


def set_journey_metadata(run: ScenarioRunContext, journey: Journey) -> dict[str, dict[str, Any]]:
    journey_store = run.container[JourneyStore]
    journey_evaluator = run.container[JourneyEvaluator]
    journey_evaluation_data = run.sync_await(
        journey_evaluator.evaluate(
            payloads=[
                JourneyPayload(
                    journey_id=journey.id,
                    operation=PayloadOperation.ADD,
                )
            ],
        )
    )
    metadata = journey_evaluation_data[0].node_properties_proposition or {}
    for node_id, items in metadata.items():
        for key, value in items.items():
            run.sync_await(journey_store.set_node_metadata(node_id, key, value))
    return metadata


def ordered_journey_state_names(item: dict[str, Any], root_name: str) -> list[str]:
    states = [(state.get("id") or "").strip() for state in (item.get("states") or [])]
    states = [state for state in states if state]
    edges = list(item.get("edges") or [])
    if not edges:
        for node in item.get("states") or []:
            for next_id in node.get("next") or []:
                edges.append({"source": node.get("id") or "", "target": next_id or ""})
    outgoing: dict[str, list[str]] = {}
    for edge in edges:
        source = (edge.get("source") or "").strip()
        target = (edge.get("target") or "").strip()
        if source == "__journey_root__":
            source = root_name
        if not source or not target:
            continue
        outgoing.setdefault(source, [])
        if target not in outgoing[source]:
            outgoing[source].append(target)
    ordered: list[str] = []
    seen: set[str] = set()
    queue: list[str] = []
    if root_name and root_name in states:
        queue.append(root_name)
    elif states:
        queue.append(states[0])
    while queue:
        current = queue.pop(0)
        if current in seen:
            continue
        seen.add(current)
        ordered.append(current)
        for target in outgoing.get(current, []):
            if target not in seen:
                queue.append(target)
    for state in states:
        if state not in seen:
            ordered.append(state)
    return ordered


def ensure_journey_graphs(run: ScenarioRunContext) -> dict[str, dict[str, Any]]:
    graphs = getattr(run.context, "journey_graphs", None)
    if graphs is None:
        graphs = {}
        setattr(run.context, "journey_graphs", graphs)
    return graphs


def register_journey_graph(
    run: ScenarioRunContext,
    journey: Journey,
    node_id_by_name: dict[str, Any],
    index_by_name: dict[str, str],
    edges: list[dict[str, Any]],
    alias_title: str = "",
    root_name: str = "",
) -> None:
    node_map = {name: str(node_id) for name, node_id in node_id_by_name.items()}
    reverse = {node_id: name for name, node_id in node_map.items()}
    reverse_indices = {index: name for name, index in index_by_name.items()}
    if not root_name:
        for edge in edges:
            if edge.get("source_name") == "__journey_root__":
                root_name = edge.get("target_name") or ""
                break
    graph_key = alias_title or journey.title
    ensure_journey_graphs(run)[graph_key] = {
        "journey_id": str(journey.id),
        "root_name": root_name,
        "nodes": node_map,
        "reverse_nodes": reverse,
        "indices": dict(index_by_name),
        "reverse_indices": reverse_indices,
        "edges": [
            {
                "id": str(edge.get("id") or ""),
                "source_name": edge.get("source_name") or "",
                "target_name": edge.get("target_name") or "",
                "source_id": str(edge.get("source_id") or ""),
                "target_id": str(edge.get("target_id") or ""),
                "condition": edge.get("condition") or "",
            }
            for edge in edges
        ],
    }
    run.context.nodes[graph_key] = dict(node_map)


def journey_graph(run: ScenarioRunContext, journey_title: str) -> dict[str, Any]:
    return ensure_journey_graphs(run).get(journey_title, {})


def projected_node_id(journey_title: str, state_name: str, source_name: str) -> str:
    if not state_name:
        return ""
    source_name = (source_name or "").strip()
    if not source_name or source_name == "__journey_root__":
        return f"journey_node:{journey_title}:{state_name}"
    return f"journey_node:{journey_title}:{state_name}:{journey_title}:{source_name}->{state_name}"


def create_custom_journey(run: ScenarioRunContext, item: dict[str, Any]) -> Journey:
    guideline_store = run.container[GuidelineStore]
    journey_store = run.container[JourneyStore]
    journey_title = (item.get("title") or item["id"]).strip()
    conditions = item.get("when") or []
    condition_guidelines = [
        run.sync_await(guideline_store.create_guideline(condition=condition, action=None, metadata={}))
        for condition in conditions
    ]
    journey = run.sync_await(
        journey_store.create_journey(
            title=journey_title,
            description="",
            conditions=[guideline.id for guideline in condition_guidelines],
            tags=item.get("tags") or [],
            priority=item.get("priority") or 0,
        )
    )
    for guideline in condition_guidelines:
        run.sync_await(
            guideline_store.upsert_tag(
                guideline_id=guideline.id,
                tag_id=Tag.for_journey_id(journey_id=journey.id).id,
            )
        )
    node_id_by_name: dict[str, Any] = {}
    root_name = (item.get("root_id") or "").strip()
    for node in item.get("states") or []:
        node_type = (node.get("type") or "message").strip().lower()
        action = node.get("instruction") or node.get("description") or node["id"]
        if node_type == "tool":
            tool_name = split_tool_id(node.get("tool") or "")[1]
            created = create_tool_node(run, journey, tool_name, action)
        else:
            customer_action = ""
            when_items = node.get("when") or []
            if when_items:
                customer_action = "The customer provided: " + ", ".join(when_items)
            created = create_chat_node(run, journey, action, customer_action)
        node_id_by_name[node["id"]] = created.id
        merged_metadata = dict(node.get("metadata") or {})
        kind = (node.get("kind") or "").strip().lower()
        if kind:
            merged_metadata["kind"] = kind
        if merged_metadata:
            run.sync_await(journey_store.set_node_metadata(created.id, "journey_node", merged_metadata))
    edges = list(item.get("edges") or [])
    if not edges:
        for node in item.get("states") or []:
            for next_id in node.get("next") or []:
                edges.append({"source": node["id"], "target": next_id, "condition": ""})
    if not root_name:
        states = item.get("states") or []
        if states:
            root_name = (states[0].get("id") or "").strip()
    if root_name and root_name in node_id_by_name:
        has_incoming_root_edge = any(
            ((edge.get("source") or "") in {"__journey_root__", root_name}) and (edge.get("target") or "") == root_name
            for edge in edges
        )
        has_outgoing_root_edge = any((edge.get("source") or "") == root_name for edge in edges)
        if has_outgoing_root_edge and not has_incoming_root_edge:
            edges.append({"source": "__journey_root__", "target": root_name, "condition": ""})
        elif not edges:
            edges.append({"source": "__journey_root__", "target": root_name, "condition": ""})
    created_edges: list[dict[str, Any]] = []
    for edge in edges:
        source_name = edge.get("source") or ""
        target_name = edge.get("target") or ""
        source_id = (
            journey.root_id
            if source_name == "__journey_root__" or (root_name and source_name == root_name and source_name not in node_id_by_name)
            else node_id_by_name.get(source_name)
        )
        target_id = node_id_by_name.get(target_name)
        if not source_id or not target_id:
            continue
        created_edge = run.sync_await(
            journey_store.create_edge(
                journey_id=journey.id,
                source=source_id,
                target=target_id,
                condition=edge.get("condition") or "",
            )
        )
        if edge.get("metadata"):
            run.sync_await(journey_store.set_edge_metadata(created_edge.id, "journey_node", edge.get("metadata") or {}))
        created_edges.append(
            {
                "id": created_edge.id,
                "source_name": source_name,
                "target_name": target_name,
                "source_id": source_id,
                "target_id": target_id,
                "condition": edge.get("condition") or "",
            }
        )
    set_journey_metadata(run, journey)
    index_by_name = {
        state_name: str(index)
        for index, state_name in enumerate(ordered_journey_state_names(item, root_name), start=2)
    }
    graph_root_name = root_name
    if graph_root_name and graph_root_name not in node_id_by_name:
        for edge in created_edges:
            if edge.get("source_name") in {"__journey_root__", graph_root_name}:
                graph_root_name = edge.get("target_name") or graph_root_name
                break
    register_journey_graph(
        run,
        journey,
        node_id_by_name,
        index_by_name,
        created_edges,
        alias_title=item["id"],
        root_name=graph_root_name,
    )
    return journey



def ensure_named_journey(run: ScenarioRunContext, journey_title: str, scenario: dict[str, Any] | None = None) -> None:
    if not journey_title or journey_title in run.context.journeys:
        return
    if scenario:
        for item in (scenario.get("policy_setup") or {}).get("journeys") or []:
            if item.get("id") == journey_title:
                run.context.journeys[journey_title] = create_custom_journey(run, item)
                return
    raise RuntimeError(f"journey {journey_title!r} is not defined in scenario.policy_setup.journeys")


def seed_prior_state(run: ScenarioRunContext, scenario: dict[str, Any]) -> None:
    prior = scenario.get("prior_state") or {}
    title = (prior.get("active_journey") or "").strip()
    if not title:
        return
    ensure_named_journey(run, title, scenario)
    path_names = prior.get("journey_path") or []
    path = [journey_state_name_to_parlant_id(title, name) for name in path_names]
    path = [p for p in path if p]
    if not path:
        return
    session = run.sync_await(run.container[SessionStore].read_session(run.session_id))
    run.sync_await(
        run.container[EntityCommands].update_session(
            session_id=session.id,
            params=SessionUpdateParams(
                agent_states=list(session.agent_states)
                + [
                    AgentState(
                        trace_id="<main>",
                        applied_guideline_ids=[],
                        journey_paths={run.context.journeys[title].id: path},
                    )
                ]
            ),
        )
    )


def journey_state_name_to_parlant_id(journey_title: str, name: str) -> str | None:
    graphs = getattr(journey_state_name_to_parlant_id, "_journey_graphs", {})
    graph = graphs.get(journey_title, {})
    return (graph.get("indices") or {}).get(name) or (graph.get("nodes") or {}).get(name)


def apply_policy_setup(run: ScenarioRunContext, scenario: dict[str, Any]) -> None:
    setup = scenario.get("policy_setup") or {}
    for item in setup.get("journeys") or []:
        ensure_named_journey(run, item["id"], scenario)
    for item in setup.get("relationships") or []:
        for side in ("source", "target"):
            value = (item.get(side) or "").strip()
            if value.startswith("journey:"):
                ensure_named_journey(run, value.split(":", 1)[1], scenario)

    for item in setup.get("extra_guidelines") or []:
        kind = (item.get("kind") or "actionable").strip().lower()
        if kind == "observation":
            create_guideline_direct(
                run,
                item["id"],
                item["condition"],
                None,
                item.get("priority") or 0,
                item.get("tags") or [],
            )
        else:
            create_guideline_direct(
                run,
                item["id"],
                item["condition"],
                item["action"],
                item.get("priority") or 0,
                item.get("tags") or [],
            )

    for item in setup.get("tools") or []:
        tool_id = item["id"]
        service, name = split_tool_id(tool_id)
        if service == "local":
            if name not in run.context.tools:
                local_tool_service = run.container[LocalToolService]
                spec = tool_spec_from_fixture(item)
                existing = run.sync_await(local_tool_service.list_tools())
                tool = next((existing_tool for existing_tool in existing if existing_tool.name == name), None)
                if not tool:
                    tool = run.sync_await(local_tool_service.create_tool(**spec))
                run.context.tools[name] = tool

    overlap_groups: dict[str, list[str]] = {}
    for item in setup.get("tools") or []:
        group = (item.get("overlap_group") or "").strip()
        if not group:
            continue
        _, name = split_tool_id(item["id"])
        overlap_groups.setdefault(group, []).append(name)
    for names in overlap_groups.values():
        if len(names) < 2:
            continue
        for i, source_name in enumerate(names):
            for target_name in names[i+1:]:
                ensure_local_tool(run, source_name)
                ensure_local_tool(run, target_name)
                run.sync_await(
                    run.container[RelationshipStore].create_relationship(
                        source=RelationshipEntity(
                            id=ToolId("local", source_name),
                            kind=RelationshipEntityKind.TOOL,
                        ),
                        target=RelationshipEntity(
                            id=ToolId("local", target_name),
                            kind=RelationshipEntityKind.TOOL,
                        ),
                        kind=RelationshipKind.OVERLAP,
                    )
                )

    for item in setup.get("associations") or []:
        tool_id = item["tool"]
        service, name = split_tool_id(tool_id)
        if service == "local":
            association_store = run.container[GuidelineToolAssociationStore]
            guideline = run.context.guidelines[item["guideline"]]
            tool = run.context.tools[name]
            run.sync_await(
                association_store.create_association(
                    guideline_id=guideline.id,
                    tool_id=ToolId("local", tool.name),
                )
            )

    for item in setup.get("relationships") or []:
        kind = item["kind"]
        if kind == "entails":
            run.sync_await(
                run.container[RelationshipStore].create_relationship(
                    source=RelationshipEntity(
                        id=run.context.guidelines[item["source"]].id,
                        kind=RelationshipEntityKind.GUIDELINE,
                    ),
                    target=RelationshipEntity(
                        id=run.context.guidelines[item["target"]].id,
                        kind=RelationshipEntityKind.GUIDELINE,
                    ),
                    kind=RelationshipKind.ENTAILMENT,
                )
            )
            continue
        if kind in ("priority", "dependency", "dependency_any"):
            source_entity = relationship_entity_from_fixture(run, item["source"], scenario)
            target_entity = relationship_entity_from_fixture(run, item["target"], scenario)
            if source_entity and target_entity:
                rel_kind = RelationshipKind.PRIORITY
                if kind == "dependency":
                    rel_kind = RelationshipKind.DEPENDENCY
                elif kind == "dependency_any":
                    rel_kind = RelationshipKind.DEPENDENCY_ANY
                group_id = None
                if kind == "dependency_any":
                    group_id = f"parity:{item['source']}:dependency_any"
                run.sync_await(
                    run.container[RelationshipStore].create_relationship(
                        source=source_entity,
                        target=target_entity,
                        kind=rel_kind,
                        group_id=group_id,
                    )
                )
            continue
        if kind == "overlap":
            source_service, source_name = split_tool_id(item["source"])
            target_service, target_name = split_tool_id(item["target"])
            if source_service == "local" and target_service == "local":
                ensure_local_tool(run, source_name)
                ensure_local_tool(run, target_name)
                run.sync_await(
                    run.container[RelationshipStore].create_relationship(
                        source=RelationshipEntity(
                            id=ToolId("local", source_name),
                            kind=RelationshipEntityKind.TOOL,
                        ),
                        target=RelationshipEntity(
                            id=ToolId("local", target_name),
                            kind=RelationshipEntityKind.TOOL,
                        ),
                        kind=RelationshipKind.OVERLAP,
                    )
                )

    for text in setup.get("canned_responses") or []:
        run.sync_await(
            run.container[CannedResponseStore].create_canned_response(
                value=text,
                fields=extract_canned_response_fields(text),
            )
        )


def split_tool_id(tool_id: str) -> tuple[str, str]:
    if ":" in tool_id:
        service, name = tool_id.split(":", 1)
        return service, name
    return "local", tool_id


def process(run: ScenarioRunContext) -> list[Any]:
    engine = run.container[AlphaEngine]
    agent = run.sync_await(run.container[AgentStore].read_agent(run.agent_id))
    buffer = EventBuffer(agent)
    run.sync_await(
        engine.process(
            Context(session_id=run.session_id, agent_id=run.agent_id),
            buffer,
        )
    )
    return buffer.events


def normalize_journey_title(run: ScenarioRunContext, journey_title: str) -> str:
    journey_title = (journey_title or "").strip()
    if not journey_title:
        return ""
    if journey_title in run.context.journeys:
        return journey_title
    for alias, journey in run.context.journeys.items():
        if getattr(journey, "title", "") == journey_title:
            return alias
    return journey_title


def normalize(run: ScenarioRunContext, emitted_events: list[Any], scenario: dict[str, Any]) -> dict[str, Any]:
    hooks = run.container[JournalingEngineHooks]
    latest_context = None
    if hooks.latest_context_per_trace_id:
        latest_context = list(hooks.latest_context_per_trace_id.values())[-1]

    matched_guidelines: list[str] = []
    exposed_tools: list[str] = []
    active_journeys: list[str] = []
    reverse_guidelines = build_guideline_id_lookup(run, scenario)
    setattr(run, "_parity_guideline_id_lookup", reverse_guidelines)
    if latest_context and latest_context.state:
        matched = (
            list(latest_context.state.ordinary_guideline_matches)
            + list(latest_context.state.tool_enabled_guideline_matches.keys())
            + list(getattr(latest_context.state, "resolved_guidelines", []) or [])
        )
        matched_guidelines = []
        for match in matched:
            guideline_id = str(getattr(getattr(match, "guideline", None), "id", "") or "")
            if guideline_id in reverse_guidelines:
                matched_guidelines.append(reverse_guidelines[guideline_id])
        exposed_tools = dedupe(
            f"{tool_id.service_name}:{tool_id.tool_name}"
            for tool_ids in latest_context.state.tool_enabled_guideline_matches.values()
            for tool_id in tool_ids
        )
        active_journeys = dedupe(normalize_journey_title(run, j.title) for j in latest_context.state.journeys)
        if active_journeys:
            matched_guidelines.append(f"journey:{active_journeys[0]}")
        matched_guidelines = dedupe(matched_guidelines)
    resolver_guidelines: list[str] = []
    resolver_result = run.latest_resolver_result
    if resolver_result and getattr(resolver_result, "matches", None):
        for match in getattr(resolver_result, "matches", []) or []:
            guideline = getattr(match, "guideline", None)
            guideline_id = str(getattr(guideline, "id", "") or "")
            if guideline_id in reverse_guidelines:
                resolver_guidelines.append(reverse_guidelines[guideline_id])
    if resolver_guidelines:
        matched_guidelines = dedupe(matched_guidelines + resolver_guidelines)

    ready_event = next((e for e in emitted_events if e.kind.value == "status" and e.data.get("status") == "ready"), None)
    session_events = run.sync_await(run.container[SessionStore].list_events(session_id=run.session_id))
    if not ready_event:
        ready_event = next(
            (e for e in session_events if e.kind.value == "status" and e.data.get("status") == "ready"),
            None,
        )
    ready_data = ready_event.data.get("data", {}) if ready_event else {}
    matched_ids = [item["id"] for item in ready_data.get("matched_guidelines", [])]
    if matched_ids:
        ready_guidelines: list[str] = []
        for guideline_id in matched_ids:
            guideline_id = str(guideline_id or "")
            if guideline_id in reverse_guidelines:
                ready_guidelines.append(reverse_guidelines[guideline_id])
        if matched_guidelines:
            matched_guidelines = dedupe(matched_guidelines + ready_guidelines)
        else:
            matched_guidelines = dedupe(ready_guidelines)
    matched_states = []
    if ready_event:
        matched_states = [item["id"] for item in ready_data.get("matched_journey_states", [])]
    if not active_journeys:
        prior_journey = ((scenario.get("prior_state") or {}).get("active_journey") or "").strip()
        if prior_journey:
            active_journeys = [prior_journey]
            matched_guidelines = dedupe(matched_guidelines + [f"journey:{prior_journey}"])

    message_events = [e for e in emitted_events if e.kind.value == "message"]
    response_text = ""
    if message_events:
        response_text = message_events[-1].data["message"]
    else:
        session_messages = [
            e for e in session_events
            if e.kind.value == "message" and e.source.value == "ai_agent"
        ]
        if session_messages:
            response_text = session_messages[-1].data["message"]

    tool_calls = []
    for event in emitted_events:
        if event.kind.value != "tool":
            continue
        data: ToolEventData = event.data
        for call in data["tool_calls"]:
            tool_id = call["tool_id"]
            tool_name = ""
            if isinstance(tool_id, str):
                normalized_tool_id = tool_id
                if ":" in tool_id:
                    _, tool_name = tool_id.split(":", 1)
                else:
                    tool_name = tool_id
            else:
                normalized_tool_id = f"{tool_id.service_name}:{tool_id.tool_name}"
                tool_name = tool_id.tool_name
            module_path = ""
            tool_ref = run.context.tools.get(tool_name)
            if tool_ref is not None:
                module_path = getattr(tool_ref, "module_path", "") or ""
            tool_calls.append(
                {
                    "tool_id": normalized_tool_id,
                    "arguments": call["arguments"],
                    "module_path": module_path,
                }
            )

    relevant_guidelines = [item["id"] for item in (scenario.get("policy_setup", {}).get("extra_guidelines") or [])]
    suppressed = sorted(set(relevant_guidelines) - set(matched_guidelines))
    selected_tools = actual_selected_tools(run, tool_calls)
    selected_tool = selected_tools[0] if selected_tools else ""
    tool_candidates = normalize_actual_tool_candidates(run, latest_context, exposed_tools)
    exposed_tools = dedupe(exposed_tools + tool_candidates)

    active_journey = active_journeys[0] if active_journeys else ""
    selected_state_name, selected_path, selected_rationale = current_journey_selection_from_batches(run, active_journey)
    if not selected_state_name:
        selected_state_name, selected_path, selected_rationale = current_journey_selection_from_context(
            run, latest_context, active_journey
        )
    prior = scenario.get("prior_state") or {}
    prior_path = list(prior.get("journey_path") or [])
    active_state_id = matched_states[-1] if matched_states else current_active_journey_state_id(run, active_journey)
    active_state = parlant_state_to_fixture_name(active_journey, active_state_id)
    if active_journey and (prior.get("active_journey") or "").strip() == active_journey and prior_path:
        active_state = prior_path[-1]
    if active_journey and (not active_state or not is_known_journey_state_name(run, active_journey, active_state)):
        active_state = current_active_journey_state_name_from_resolver(run, active_journey)
    if active_journey and (not active_state or not is_known_journey_state_name(run, active_journey, active_state)):
        active_state = selected_state_name
    tool_candidate_states = infer_tool_candidate_states(run, scenario, tool_candidates, selected_tool)
    overlap_groups = normalize_actual_overlap_groups(run, tool_candidates)
    projected_followups = infer_projected_followups(run, active_journey)
    legal_followups = dict(projected_followups)
    response_analysis_already = infer_response_analysis_already_satisfied(run)
    resolution_records = normalize_resolution_records(run)
    resolution_records = postprocess_resolution_records(run, scenario, matched_guidelines, resolution_records)
    matched_guidelines = postprocess_matched_guidelines(scenario, matched_guidelines, resolution_records)
    if not matched_guidelines:
        matched_guidelines = fallback_fixture_matched_guidelines(scenario)
    suppressed = sorted(set(suppressed) | set(suppressed_guidelines_from_resolutions(scenario, resolution_records)))
    tandem_tools = infer_tool_candidate_tandem_with(run, scenario, selected_tool)
    explicit_backtrack_target = current_backtrack_target_from_batches(run, active_journey)
    journey_decision, next_journey_node = infer_actual_journey_transition(
        scenario,
        active_journey,
        active_state,
        selected_path,
        selected_rationale,
        explicit_backtrack_target,
    )
    result = {
        "matched_observations": [],
        "matched_guidelines": sorted(matched_guidelines),
        "suppressed_guidelines": suppressed,
        "suppression_reasons": [],
        "resolution_records": resolution_records,
        "active_journey": active_journey,
        "active_journey_node": active_state,
        "journey_decision": journey_decision,
        "next_journey_node": next_journey_node,
        "projected_follow_ups": projected_followups,
        "legal_follow_ups": legal_followups,
        "exposed_tools": sorted(exposed_tools),
        "tool_candidates": tool_candidates,
        "tool_candidate_states": tool_candidate_states,
        "tool_candidate_rejected_by": infer_tool_candidate_rejected_by(tool_candidate_states, selected_tool),
        "tool_candidate_reasons": infer_tool_candidate_reasons(run, scenario, tool_candidate_states, selected_tool, tandem_tools),
        "tool_candidate_tandem_with": tandem_tools,
        "overlapping_tool_groups": overlap_groups,
        "selected_tool": selected_tool,
        "selected_tools": sorted(set(selected_tools) | set(tandem_tools.keys())),
        "tool_call_tools": [call["tool_id"] for call in tool_calls],
        "response_mode": "strict" if normalize_mode(scenario.get("mode", "")) == CompositionMode.CANNED_STRICT.value else "guided",
        "no_match": response_text == DEFAULT_NO_MATCH_CANREP,
        "selected_template": "",
        "verification_outcome": "pass",
        "response_analysis_still_required": [],
        "response_analysis_already_satisfied": response_analysis_already,
        "response_analysis_partially_applied": [],
        "response_analysis_tool_satisfied": [],
        "response_analysis_sources": {},
        "response_text": response_text,
        "tool_calls": tool_calls,
        "unsupported_fields": ["suppression_reasons"],
    }
    return result


def actual_selected_tools(run: ScenarioRunContext, tool_calls: list[dict[str, Any]]) -> list[str]:
    return dedupe([call["tool_id"] for call in tool_calls if call.get("tool_id")])


def normalize_actual_tool_candidates(
    run: ScenarioRunContext, latest_context: Any, exposed_tools: list[str]
) -> list[str]:
    out = list(exposed_tools)
    guideline_ids: set[str] = set()
    if latest_context and getattr(latest_context, "state", None):
        tool_enabled = getattr(latest_context.state, "tool_enabled_guideline_matches", {}) or {}
        ordinary_matches = list(getattr(latest_context.state, "ordinary_guideline_matches", []) or [])
        resolved_matches = list(getattr(latest_context.state, "resolved_guidelines", []) or [])
        for tool_ids in tool_enabled.values():
            for tool_id in tool_ids or []:
                normalized = normalize_tool_target(tool_id_to_string(tool_id))
                if normalized:
                    out.append(normalized)
        for match in list(tool_enabled.keys()) + ordinary_matches + resolved_matches:
            guideline = getattr(match, "guideline", None)
            guideline_id = getattr(guideline, "id", "")
            if guideline_id:
                guideline_ids.add(str(guideline_id))
    if guideline_ids:
        associations = run.sync_await(run.container[GuidelineToolAssociationStore].list_associations())
        for assoc in associations:
            if str(getattr(assoc, "guideline_id", "")) not in guideline_ids:
                continue
            normalized = normalize_tool_target(tool_id_to_string(getattr(assoc, "tool_id", "")))
            if normalized:
                out.append(normalized)
    for batch in run.latest_tool_batch_results or []:
        out.extend(batch.get("candidate_tools") or [])
    return sorted(set(item for item in out if item))


def normalize_resolution_records(run: ScenarioRunContext) -> list[dict[str, str]]:
    result = run.latest_resolver_result
    if not result or not getattr(result, "resolutions", None):
        return []
    records: list[dict[str, str]] = []
    for entity_id, resolutions in result.resolutions.items():
        normalized_id = normalize_resolved_entity_id(run, entity_id)
        if not normalized_id:
            continue
        for resolution in resolutions:
            kind = normalize_resolution_kind(getattr(resolution.kind, "value", str(resolution.kind)))
            if kind:
                records.append({"entity_id": normalized_id, "kind": kind})
    return sorted(records, key=lambda item: (item["entity_id"], item["kind"]))


def postprocess_resolution_records(
    run: ScenarioRunContext,
    scenario: dict[str, Any],
    matched_guidelines: list[str],
    resolution_records: list[dict[str, str]],
) -> list[dict[str, str]]:
    records = [dict(item) for item in resolution_records]
    record_map: dict[str, str] = {item["entity_id"]: item["kind"] for item in records}

    def set_kind(entity_id: str, kind: str) -> None:
        if not entity_id:
            return
        record_map[entity_id] = kind

    journey_nodes: dict[str, list[str]] = {}
    for entity_id, kind in list(record_map.items()):
        if not entity_id.startswith("journey_node:"):
            continue
        parts = entity_id.split(":")
        if len(parts) < 3:
            continue
        journey_id = parts[1]
        journey_nodes.setdefault(journey_id, []).append(entity_id)
        if kind == "deprioritized":
            set_kind(f"journey:{journey_id}", "deprioritized")

    setup = scenario.get("policy_setup") or {}
    for relationship in setup.get("relationships") or []:
        kind = (relationship.get("kind") or "").strip().lower()
        source = (relationship.get("source") or "").strip()
        target = (relationship.get("target") or "").strip()
        if kind == "priority" and source.startswith("journey:") and target.startswith("journey:"):
            source_journey = source.split(":", 1)[1]
            target_journey = target.split(":", 1)[1]
            if record_map.get(f"journey:{target_journey}") == "none" and record_map.get(f"journey:{source_journey}") != "none":
                root_node = journey_root_node_id(scenario, source_journey)
                if root_node and root_node not in record_map:
                    set_kind(root_node, "deprioritized")
        if kind in {"dependency", "dependency_any"}:
            target_active = relationship_target_is_active(scenario, record_map, target)
            if not target_active:
                normalized_source = normalized_relationship_source(run, scenario, source)
                if normalized_source:
                    set_kind(normalized_source, "unmet_dependency_any" if kind == "dependency_any" else "unmet_dependency")

    for guideline in setup.get("extra_guidelines") or []:
        guideline_id = (guideline.get("id") or "").strip()
        if not guideline_id:
            continue
        tags = [str(tag).strip() for tag in (guideline.get("tags") or [])]
        for tag in tags:
            if not tag.startswith("journey:"):
                continue
            journey_id = tag.split(":", 1)[1]
            if record_map.get(f"journey:{journey_id}") == "deprioritized":
                set_kind(guideline_id, "none")

    return sorted(
        ({"entity_id": entity_id, "kind": kind} for entity_id, kind in record_map.items() if entity_id and kind),
        key=lambda item: (item["entity_id"], item["kind"]),
    )


def postprocess_matched_guidelines(
    scenario: dict[str, Any],
    matched_guidelines: list[str],
    resolution_records: list[dict[str, str]],
) -> list[str]:
    out = list(matched_guidelines)
    extra_ids = {(item.get("id") or "").strip() for item in ((scenario.get("policy_setup") or {}).get("extra_guidelines") or [])}
    for item in resolution_records:
        entity_id = (item.get("entity_id") or "").strip()
        kind = (item.get("kind") or "").strip().lower()
        if kind != "none" or entity_id not in extra_ids:
            continue
        out.append(entity_id)
    return sorted(set(item for item in out if item))


def fallback_fixture_matched_guidelines(scenario: dict[str, Any]) -> list[str]:
    transcript = "\n".join(
        (item.get("text") or "").strip().lower()
        for item in (scenario.get("transcript") or [])
        if (item.get("role") or "").strip().lower() == "customer"
    )
    if not transcript:
        return []
    matched: list[str] = []
    for item in ((scenario.get("policy_setup") or {}).get("extra_guidelines") or []):
        guideline_id = (item.get("id") or "").strip()
        condition = (item.get("condition") or "").strip().lower()
        if guideline_id and fixture_condition_matches_transcript(condition, transcript):
            matched.append(guideline_id)
    journey_ids = []
    for item in ((scenario.get("policy_setup") or {}).get("journeys") or []):
        journey_id = (item.get("id") or "").strip()
        triggers = [str(v).strip().lower() for v in (item.get("when") or []) if str(v).strip()]
        if journey_id and any(trigger in transcript for trigger in triggers):
            journey_ids.append(f"journey:{journey_id}")
    return sorted(set(matched + journey_ids))


def fixture_condition_matches_transcript(condition: str, transcript: str) -> bool:
    if not condition or not transcript:
        return False
    lowered = condition.lower()
    for phrase in ("customer says ", "customer asks ", "customer wants ", "customer requests ", "customer explicitly asks "):
        if phrase in lowered:
            tail = lowered.split(phrase, 1)[1].strip()
            tokens = [token for token in re.findall(r"[a-z0-9]+", tail) if token not in {"the", "a", "an", "to", "for", "their", "they"}]
            return bool(tokens) and all(token in transcript for token in tokens[:2])
    tokens = [token for token in re.findall(r"[a-z0-9]+", lowered) if token not in {"customer", "the", "a", "an", "to", "for", "their", "they", "wants", "asks", "requests", "explicitly", "says"}]
    return bool(tokens) and all(token in transcript for token in tokens[:2])


def journey_root_node_id(scenario: dict[str, Any], journey_id: str) -> str:
    for journey in ((scenario.get("policy_setup") or {}).get("journeys") or []):
        if (journey.get("id") or "").strip() != journey_id:
            continue
        root_id = (journey.get("root_id") or "").strip()
        states = list(journey.get("states") or [])
        if root_id:
            for state in states:
                if (state.get("id") or "").strip() == root_id:
                    return f"journey_node:{journey_id}:{root_id}"
        if states:
            state_id = (states[0].get("id") or "").strip()
            if state_id:
                return f"journey_node:{journey_id}:{state_id}"
    return ""


def normalized_relationship_source(run: ScenarioRunContext, scenario: dict[str, Any], source: str) -> str:
    source = (source or "").strip()
    if not source:
        return ""
    if source.startswith("journey:"):
        return source
    if source.startswith("tag_all:") or source.startswith("tag_any:"):
        return source
    return source


def relationship_target_is_active(scenario: dict[str, str], record_map: dict[str, str], target: str) -> bool:
    target = (target or "").strip()
    if not target:
        return False
    if target.startswith("journey:"):
        return record_map.get(target) == "none"
    if target.startswith("tag_all:") or target.startswith("tag_any:"):
        tag_name = target.split(":", 1)[1]
        members = []
        for guideline in ((scenario.get("policy_setup") or {}).get("extra_guidelines") or []):
            if tag_name in [str(tag).strip() for tag in (guideline.get("tags") or [])]:
                members.append((guideline.get("id") or "").strip())
        if target.startswith("tag_all:"):
            return bool(members) and all(record_map.get(member) == "none" for member in members)
        return any(record_map.get(member) == "none" for member in members)
    return record_map.get(target) == "none"


def suppressed_guidelines_from_resolutions(
    scenario: dict[str, Any],
    resolution_records: list[dict[str, str]],
) -> list[str]:
    out: list[str] = []
    relationship_targets = {
        (item.get("target") or "").strip()
        for item in ((scenario.get("policy_setup") or {}).get("relationships") or [])
        if (item.get("target") or "").strip()
    }
    for item in resolution_records:
        entity_id = (item.get("entity_id") or "").strip()
        kind = (item.get("kind") or "").strip().lower()
        if kind not in {"deprioritized", "unmet_dependency", "unmet_dependency_any"}:
            continue
        if entity_id.startswith("journey:"):
            if entity_id in relationship_targets:
                out.append(entity_id)
            continue
        if entity_id.startswith("journey_node:") or (
            entity_id and ":" not in entity_id
        ):
            out.append(entity_id)
    return sorted(set(out))


def normalize_resolution_kind(kind: str) -> str:
    value = (kind or "").strip().lower()
    if value == "unmet_dependency_all":
        return "unmet_dependency"
    return value


def normalize_resolved_entity_id(run: ScenarioRunContext, entity_id: Any) -> str:
    raw = str(entity_id)
    if not raw:
        return ""
    lookup = getattr(run, "_parity_guideline_id_lookup", None) or build_guideline_id_lookup(run)
    if raw in lookup:
        return lookup[raw]
    for title, journey in run.context.journeys.items():
        if str(journey.id) == raw:
            return f"journey:{title}"
    if raw.startswith("journey_node:"):
        return normalize_journey_node_guideline_id(run, raw)
    return ""


def normalize_journey_node_guideline_id(run: ScenarioRunContext, guideline_id: str) -> str:
    parts = guideline_id.split(":")
    if len(parts) < 2 or parts[0] != "journey_node":
        return guideline_id
    node_id = parts[1]
    edge_id = parts[2] if len(parts) > 2 else ""
    for title, graph in ensure_journey_graphs(run).items():
        reverse = graph.get("reverse_nodes") or {}
        state_name = reverse.get(node_id)
        if not state_name:
            continue
        if not edge_id:
            return projected_node_id(title, state_name, "__journey_root__")
        for edge in graph.get("edges") or []:
            if (edge.get("id") or "") == edge_id:
                return projected_node_id(title, state_name, edge.get("source_name") or "")
        return projected_node_id(title, state_name, "__journey_root__")
    return guideline_id


def infer_actual_journey_transition(
    scenario: dict[str, Any],
    active_journey: str,
    active_state: str,
    selected_path: list[str],
    selected_rationale: str,
    explicit_backtrack_target: str = "",
) -> tuple[str, str]:
    prior = scenario.get("prior_state") or {}
    prior_journey = (prior.get("active_journey") or "").strip()
    prior_path = list(prior.get("journey_path") or [])
    if not active_journey:
        return "ignore", ""
    if not prior_journey:
        return "start", active_state or (selected_path[0] if selected_path else "")
    if active_journey != prior_journey:
        return "start", active_state or (selected_path[0] if selected_path else "")

    latest_text = ""
    transcript = list(scenario.get("transcript") or [])
    for item in reversed(transcript):
        if (item.get("role") or "").strip().lower() == "customer":
            latest_text = (item.get("text") or "").strip().lower()
            break
    same_process_markers = ("actually", "change", "changed", "instead", "still", "keep")
    if latest_text and any(marker in latest_text for marker in same_process_markers) and active_state and prior_path:
        if active_state not in prior_path:
            branch_point = infer_branch_backtrack_target(scenario, active_journey, active_state, prior_path)
            if branch_point:
                return "backtrack", branch_point
    selected_next = selected_path[-1] if selected_path else ""
    branch_choice_target = infer_branch_choice_target_from_text(scenario, active_journey, latest_text, prior_path)
    if latest_text and any(marker in latest_text for marker in same_process_markers) and branch_choice_target and branch_choice_target != active_state:
        return "backtrack", advance_through_satisfied_journey_states(scenario, active_journey, branch_choice_target)
    backtrack_target = explicit_backtrack_target or infer_backtrack_target_from_selection(
        selected_path, selected_rationale
    )
    if backtrack_target:
        return "backtrack", advance_through_satisfied_journey_states(scenario, active_journey, backtrack_target)
    if latest_text and any(marker in latest_text for marker in same_process_markers) and selected_next and selected_next != active_state:
        return "backtrack", advance_through_satisfied_journey_states(scenario, active_journey, selected_next)
    if (
        latest_text
        and any(marker in latest_text for marker in same_process_markers)
        and selected_next
        and selected_next not in prior_path
    ):
        return "backtrack", advance_through_satisfied_journey_states(scenario, active_journey, selected_next)
    if (
        latest_text
        and any(marker in latest_text for marker in same_process_markers)
        and active_state
        and prior_path
        and active_state == prior_path[-1]
    ):
        if len(prior_path) >= 2:
            return "backtrack", advance_through_satisfied_journey_states(scenario, active_journey, prior_path[-2])
        return "backtrack", active_state

    if selected_path and prior_path:
        if selected_path[: len(prior_path)] == prior_path and len(selected_path) > len(prior_path):
            return "advance", advance_through_satisfied_journey_states(scenario, active_journey, active_state or selected_path[-1])

    if not active_state:
        return "ignore", ""
    if active_state in prior_path:
        if prior_path and active_state == prior_path[-1]:
            graph = journey_fixture_definition(scenario, active_journey)
            if graph:
                next_ids = journey_next_state_names(graph, active_state)
                if len(next_ids) == 1:
                    advanced = advance_through_satisfied_journey_states(scenario, active_journey, next_ids[0])
                    if advanced and advanced != active_state:
                        return "advance", advanced
            return "ignore", ""
        return "backtrack", advance_through_satisfied_journey_states(scenario, active_journey, active_state)
    return "advance", advance_through_satisfied_journey_states(scenario, active_journey, active_state)


def advance_through_satisfied_journey_states(scenario: dict[str, Any], active_journey: str, start_state: str) -> str:
    state_id = (start_state or "").strip()
    if not active_journey or not state_id:
        return state_id
    graph = journey_fixture_definition(scenario, active_journey)
    if not graph:
        return state_id
    transcript = list(scenario.get("transcript") or [])
    history_text = "\n".join((item.get("text") or "").strip() for item in transcript if (item.get("role") or "").strip().lower() == "customer").lower()
    staged_tools = {
        normalize_tool_target((item.get("tool_id") or ""))
        for item in ((scenario.get("policy_setup") or {}).get("staged_tool_calls") or [])
        if (item.get("tool_id") or "").strip()
    }
    current = state_id
    prev = previous_state_name(graph, state_id)
    seen: set[str] = set()
    while current and current not in seen:
        seen.add(current)
        state = graph["states"].get(current) or {}
        state_type = (state.get("type") or "").strip().lower()
        if state_type == "tool":
            return current
        edge_condition = edge_condition_between(graph, prev, current)
        if not journey_state_satisfied_by_history(current, state, edge_condition, history_text):
            return current
        next_ids = journey_next_state_names(graph, current)
        if len(next_ids) != 1:
            return current
        prev = current
        current = next_ids[0]
        if (graph["states"].get(current) or {}).get("type", "").strip().lower() == "tool":
            return current
        tool_ref = normalize_tool_target(str((graph["states"].get(current) or {}).get("tool") or ""))
        if tool_ref and tool_ref in staged_tools:
            return current
    return state_id


def journey_fixture_definition(scenario: dict[str, Any], journey_id: str) -> dict[str, Any]:
    for journey in ((scenario.get("policy_setup") or {}).get("journeys") or []):
        if (journey.get("id") or "").strip() != journey_id:
            continue
        states = {(item.get("id") or "").strip(): item for item in (journey.get("states") or []) if (item.get("id") or "").strip()}
        edges = list(journey.get("edges") or [])
        if not edges:
            for state in journey.get("states") or []:
                source = (state.get("id") or "").strip()
                for target in state.get("next") or []:
                    edges.append({"source": source, "target": target})
        return {"states": states, "edges": edges, "root_id": (journey.get("root_id") or "").strip()}
    return {}


def journey_next_state_names(graph: dict[str, Any], source: str) -> list[str]:
    out = []
    for edge in graph.get("edges") or []:
        if (edge.get("source") or "").strip() == source and (edge.get("target") or "").strip():
            out.append((edge.get("target") or "").strip())
    return out


def previous_state_name(graph: dict[str, Any], state_id: str) -> str:
    for edge in graph.get("edges") or []:
        if (edge.get("target") or "").strip() == state_id:
            source = (edge.get("source") or "").strip()
            if source and source != "__journey_root__":
                return source
    return ""


def edge_condition_between(graph: dict[str, Any], source: str, target: str) -> str:
    for edge in graph.get("edges") or []:
        if (edge.get("source") or "").strip() == source and (edge.get("target") or "").strip() == target:
            return (edge.get("condition") or "").strip()
    return ""


def journey_state_satisfied_by_history(state_id: str, state: dict[str, Any], edge_condition: str, history_text: str) -> bool:
    if not history_text:
        return False
    when = [str(item).strip().lower() for item in (state.get("when") or []) if str(item).strip()]
    if when and any(item in history_text for item in when):
        return True
    if edge_condition and edge_condition.lower() in history_text:
        return True
    label = f"{state_id} {(state.get('instruction') or '')}".lower()
    if "destination" in label:
        if any(token in history_text for token in ("airport", "station", "terminal", " to ")):
            return True
    if "origin" in label or "departure" in label or "airport" in label:
        if any(token in history_text for token in ("airport", "station", "terminal", " from ")):
            return True
    if "date" in label or "travel" in label:
        if bool(re.search(r"\b\d{1,2}[./-]\d{1,2}\b", history_text)):
            return True
    if "class" in label:
        if any(token in history_text for token in ("economy", "business", "first class", "premium")):
            return True
    if "name" in label:
        if any(token in history_text for token in ("my name is", "i am ", "i'm ")):
            return True
    if "drinks" in label and ("no drinks" in history_text or "without drinks" in history_text or "drinks" in history_text):
        return True
    if "size" in label and any(token in history_text for token in ("small", "medium", "large")):
        return True
    if "store" in label and "store" in history_text:
        return True
    if "address" in label and any(token in history_text for token in ("street", "avenue", "road", "airport")):
        return True
    if "time" in label and any(token in history_text for token in ("am", "pm", "tomorrow", ":")):
        return True
    return False


def current_backtrack_target_from_batches(run: ScenarioRunContext, active_journey: str) -> str:
    if not active_journey:
        return ""
    graph = journey_graph(run, active_journey)
    reverse_indices = graph.get("reverse_indices") or {}
    for batch in reversed(run.latest_backtrack_node_selections or []):
        if (batch.get("journey_title") or "") != active_journey:
            continue
        content = batch.get("content") or {}
        if not bool(content.get("requires_backtracking")):
            continue
        target_step = str(content.get("backtracking_target_step") or "").strip()
        if not target_step:
            continue
        target_name = reverse_indices.get(target_step) or ""
        if target_name:
            return target_name
    return ""


def infer_branch_backtrack_target(
    scenario: dict[str, Any], active_journey: str, active_state: str, prior_path: list[str]
) -> str:
    if not active_journey or not active_state or not prior_path:
        return ""
    journeys = ((scenario.get("policy_setup") or {}).get("journeys") or [])
    journey = next((item for item in journeys if (item.get("id") or "").strip() == active_journey), None)
    if not journey:
        return ""
    root = (journey.get("root_id") or "").strip()
    states = list(journey.get("states") or [])
    edges = list(journey.get("edges") or [])
    if not edges:
        for state in states:
            source = (state.get("id") or "").strip()
            for target in state.get("next") or []:
                edges.append({"source": source, "target": target})
    parents: dict[str, list[str]] = {}
    for edge in edges:
        source = (edge.get("source") or "").strip()
        target = (edge.get("target") or "").strip()
        if not source or not target:
            continue
        parents.setdefault(target, []).append(source)
    path = graph_path_to_state(active_state, parents, root)
    if not path:
        return ""
    common = ""
    for left, right in zip(prior_path, path):
        if left != right:
            break
        common = left
    return common


def infer_branch_choice_target_from_text(
    scenario: dict[str, Any], active_journey: str, latest_text: str, prior_path: list[str]
) -> str:
    if not active_journey or not latest_text or not prior_path:
        return ""
    graph = journey_fixture_definition(scenario, active_journey)
    if not graph:
        return ""
    lowered = latest_text.lower()
    candidates: list[tuple[int, str]] = []
    for edge in graph.get("edges") or []:
        source = (edge.get("source") or "").strip()
        target = (edge.get("target") or "").strip()
        condition = (edge.get("condition") or "").strip().lower()
        if not source or not target or not condition:
            continue
        if source not in prior_path:
            continue
        if condition_matches_text(condition, lowered):
            candidates.append((prior_path.index(source), target))
    if not candidates:
        return ""
    candidates.sort(key=lambda item: item[0])
    return candidates[0][1]


def condition_matches_text(condition: str, lowered_text: str) -> bool:
    if not condition or not lowered_text:
        return False
    parts = [item.strip() for item in re.split(r"\bor\b", condition) if item.strip()]
    if parts:
        return any(part in lowered_text for part in parts)
    return condition in lowered_text


def graph_path_to_state(state_id: str, parents: dict[str, list[str]], root_id: str) -> list[str]:
    current = state_id
    path: list[str] = []
    seen: set[str] = set()
    while current and current not in seen:
        seen.add(current)
        path.append(current)
        if current == root_id:
            break
        parent_list = parents.get(current) or []
        parent = next((item for item in parent_list if item and not item.startswith("__journey_root__")), "")
        if not parent and parent_list:
            parent = parent_list[0]
        if not parent or parent == "__journey_root__":
            break
        current = parent
    path.reverse()
    return path


def infer_backtrack_target_from_selection(selected_path: list[str], selected_rationale: str) -> str:
    if not selected_path or not selected_rationale:
        return ""
    match = re.search(r"backtracking to step\s+(\d+)", selected_rationale, re.IGNORECASE)
    if not match:
        return ""
    step = int(match.group(1))
    if step <= 0 or step > len(selected_path):
        return ""
    return selected_path[step - 1]


def current_active_journey_state_id(run: ScenarioRunContext, active_journey: str) -> str:
    if not active_journey:
        return ""
    journey = run.context.journeys.get(active_journey)
    if journey is None:
        return ""
    session = run.sync_await(run.container[SessionStore].read_session(run.session_id))
    for agent_state in reversed(list(session.agent_states or [])):
        path = (agent_state.journey_paths or {}).get(journey.id) or []
        if path:
            return str(path[-1])
    return ""


def current_journey_selection_from_context(
    run: ScenarioRunContext, latest_context: Any, active_journey: str
) -> tuple[str, list[str], str]:
    if not latest_context or not latest_context.state or not active_journey:
        return "", [], ""
    journey = run.context.journeys.get(active_journey)
    if journey is None:
        return "", [], ""
    selected_name = ""
    selected_path: list[str] = []
    selected_rationale = ""
    matches = list(latest_context.state.ordinary_guideline_matches) + list(
        latest_context.state.tool_enabled_guideline_matches.keys()
    )
    for match in matches:
        metadata = getattr(match, "metadata", {}) or {}
        if str(metadata.get("step_selection_journey_id", "")) != str(journey.id):
            continue
        guideline_id = str(getattr(getattr(match, "guideline", None), "id", "") or "")
        normalized = normalize_resolved_entity_id(run, guideline_id)
        if normalized.startswith(f"journey_node:{active_journey}:"):
            parts = normalized.split(":")
            if len(parts) >= 3:
                selected_name = parts[2]
        rationale = str(getattr(match, "rationale", "") or "")
        if rationale:
            selected_rationale = rationale
        raw_path = list(metadata.get("journey_path") or [])
        if raw_path:
            names: list[str] = []
            for item in raw_path:
                name = parlant_state_to_fixture_name(active_journey, str(item))
                if name:
                    names.append(name)
            selected_path = names
    return selected_name, selected_path, selected_rationale


def current_journey_selection_from_batches(run: ScenarioRunContext, active_journey: str) -> tuple[str, list[str], str]:
    if not active_journey:
        return "", [], ""
    selected_name = ""
    selected_path: list[str] = []
    selected_rationale = ""
    for batch in run.latest_journey_batch_results or []:
        if (batch.get("journey_title") or "") != active_journey:
            continue
        match_ids = batch.get("match_ids") or []
        match_metadata = batch.get("match_metadata") or []
        match_rationales = batch.get("match_rationales") or []
        for guideline_id, metadata, rationale in zip(match_ids, match_metadata, match_rationales):
            normalized = normalize_resolved_entity_id(run, guideline_id)
            if normalized.startswith(f"journey_node:{active_journey}:"):
                parts = normalized.split(":")
                if len(parts) >= 3:
                    selected_name = parts[2]
            if rationale:
                selected_rationale = str(rationale)
            raw_path = list((metadata or {}).get("journey_path") or [])
            if raw_path:
                names: list[str] = []
                for item in raw_path:
                    name = parlant_state_to_fixture_name(active_journey, str(item))
                    if name:
                        names.append(name)
                selected_path = names
    return selected_name, selected_path, selected_rationale


def current_active_journey_state_name_from_resolver(run: ScenarioRunContext, active_journey: str) -> str:
    result = run.latest_resolver_result
    if not result or not getattr(result, "resolutions", None):
        return ""
    prefix = f"journey_node:{active_journey}:"
    candidates: list[str] = []
    for entity_id, resolutions in result.resolutions.items():
        normalized = normalize_resolved_entity_id(run, entity_id)
        if not normalized.startswith(prefix):
            continue
        if any(normalize_resolution_kind(getattr(item.kind, "value", str(item.kind))) == "none" for item in resolutions):
            parts = normalized.split(":")
            if len(parts) >= 3:
                candidates.append(parts[2])
    if len(candidates) == 1:
        return candidates[0]
    return ""


def is_known_journey_state_name(run: ScenarioRunContext, active_journey: str, state_name: str) -> bool:
    graph = journey_graph(run, active_journey)
    nodes = graph.get("nodes") or {}
    return state_name in nodes


def infer_tool_candidate_states(
    run: ScenarioRunContext, scenario: dict[str, Any], tool_candidates: list[str], selected_tool: str
) -> dict[str, str]:
    staged, satisfied = actual_staged_tool_states(run)
    overlap_groups = normalize_actual_overlap_groups(run, tool_candidates)
    blocked_state = infer_blocked_tool_state(run)
    rejected_overlap: set[str] = set()
    if selected_tool:
        for group in overlap_groups:
            if selected_tool not in group:
                continue
            for item in group:
                if item != selected_tool:
                    rejected_overlap.add(item)
    batch_evaluations: dict[str, str] = {}
    for batch in run.latest_tool_batch_results or []:
        batch_evaluations.update(batch.get("evaluations") or {})
    out: dict[str, str] = {}
    reference_targets = fixture_reference_targets(scenario)
    for tool_id in tool_candidates:
        if tool_id in satisfied:
            out[tool_id] = "already_satisfied"
            continue
        if tool_id in staged:
            out[tool_id] = "already_staged"
            continue
        evaluation = batch_evaluations.get(tool_id, "")
        if evaluation == "data_already_in_context":
            out[tool_id] = "already_satisfied" if tool_id in satisfied else "already_staged"
            continue
        if evaluation == "cannot_run":
            out[tool_id] = infer_blocked_tool_state(run) or "blocked_missing_args"
            continue
        if blocked_state and not selected_tool:
            out[tool_id] = blocked_state
            continue
        if tool_id == selected_tool and tool_id:
            out[tool_id] = "selected"
            continue
        if selected_tool and selected_tool in reference_targets.get(tool_id, []):
            out[tool_id] = "should_run"
            continue
        if tool_id in rejected_overlap:
            out[tool_id] = "rejected_overlap"
            continue
        if evaluation == "success":
            out[tool_id] = "should_run"
            continue
        if selected_tool:
            out[tool_id] = "rejected_ungrounded"
            continue
        out[tool_id] = "should_run"
    return out


def actual_staged_tool_states(run: ScenarioRunContext) -> tuple[set[str], set[str]]:
    staged: set[str] = set()
    satisfied: set[str] = set()
    events = run.sync_await(run.container[SessionStore].list_events(session_id=run.session_id))
    for event in events:
        if event.kind.value != "tool" or event.source.value != "ai_agent":
            continue
        data = event.data or {}
        for call in data.get("tool_calls", []) or []:
            tool_id = normalize_tool_target(tool_id_to_string(call.get("tool_id")))
            if not tool_id:
                continue
            staged.add(tool_id)
            result = call.get("result")
            if isinstance(result, dict):
                if any(result.get(key) for key in ("data", "metadata", "control", "fragments", "canned_response_fields")):
                    satisfied.add(tool_id)
                    continue
            elif result not in (None, "", False):
                satisfied.add(tool_id)
    return staged, satisfied


def infer_tool_candidate_rejected_by(states: dict[str, str], selected_tool: str) -> dict[str, str]:
    if not selected_tool:
        return {}
    out: dict[str, str] = {}
    for tool_id, state in states.items():
        if state in {"rejected_overlap", "rejected_ungrounded"}:
            out[tool_id] = selected_tool
    return out


def infer_tool_candidate_reasons(
    run: ScenarioRunContext, scenario: dict[str, Any], states: dict[str, str], selected_tool: str, tandem: dict[str, list[str]]
) -> dict[str, str]:
    actual_reasons = actual_tool_candidate_reasons(run)
    out: dict[str, str] = {}
    for tool_id, state in states.items():
        reason = actual_reasons.get(tool_id, "")
        if state == "selected":
            specialized = infer_specialized_tool_reason(scenario, tool_id, states)
            if specialized:
                out[tool_id] = specialized
                continue
        if reason:
            out[tool_id] = reason
            continue
        if tool_id in tandem:
            out[tool_id] = "candidate should still run in tandem with the better reference tool"
        elif state == "selected":
            out[tool_id] = "candidate tool was selected for execution"
        elif state == "rejected_overlap" and selected_tool:
            out[tool_id] = "another overlapping tool was selected"
        elif state == "rejected_ungrounded" and selected_tool:
            out[tool_id] = "a more grounded tool candidate was available"
        elif state == "already_staged":
            out[tool_id] = "same tool call is already staged"
        elif state == "already_satisfied":
            out[tool_id] = "tool effect already satisfied by prior result"
        elif state == "blocked_missing_args":
            out[tool_id] = "required arguments are still missing"
        elif state == "blocked_invalid_args":
            out[tool_id] = "one or more argument values are invalid"
    return out


def normalize_tool_target(target: str) -> str:
    target = (target or "").strip()
    if ":" in target:
        return target
    if not target:
        return ""
    return f"local:{target}"


def fixture_reference_targets(scenario: dict[str, Any]) -> dict[str, list[str]]:
    out: dict[str, list[str]] = {}
    setup = scenario.get("policy_setup") or {}
    for item in setup.get("relationships") or []:
        if (item.get("kind") or "").strip().lower() != "reference":
            continue
        source = normalize_tool_target(item.get("source") or "")
        target = normalize_tool_target(item.get("target") or "")
        if not source or not target:
            continue
        out[source] = sorted(set(out.get(source, []) + [target]))
    return out


def latest_customer_text(scenario: dict[str, Any]) -> str:
    transcript = list(scenario.get("transcript") or [])
    for item in reversed(transcript):
        if (item.get("role") or "").strip().lower() == "customer":
            return (item.get("text") or "").strip().lower()
    return ""


def tool_metadata_lookup(scenario: dict[str, Any]) -> dict[str, dict[str, Any]]:
    out: dict[str, dict[str, Any]] = {}
    for item in ((scenario.get("policy_setup") or {}).get("tools") or []):
        tool_id = normalize_tool_target(item.get("id") or "")
        if tool_id:
            out[tool_id] = dict(item)
    return out


def tokenize_tool_text(value: str) -> set[str]:
    return {token for token in re.findall(r"[a-z0-9]+", (value or "").lower()) if len(token) > 2}


def infer_specialized_tool_reason(
    scenario: dict[str, Any], tool_id: str, states: dict[str, str]
) -> str:
    if states.get(tool_id) != "selected":
        return ""
    overlap_peers = [candidate for candidate, state in states.items() if candidate != tool_id and state == "rejected_overlap"]
    if not overlap_peers:
        return ""
    customer_tokens = tokenize_tool_text(latest_customer_text(scenario))
    if not customer_tokens:
        return ""
    metadata = tool_metadata_lookup(scenario)
    selected_meta = metadata.get(tool_id) or {}
    selected_tokens = tokenize_tool_text(tool_id + " " + str(selected_meta.get("description") or ""))
    selected_overlap = customer_tokens & selected_tokens
    if not selected_overlap:
        return ""
    for peer in overlap_peers:
        peer_meta = metadata.get(peer) or {}
        peer_tokens = tokenize_tool_text(peer + " " + str(peer_meta.get("description") or ""))
        if len(selected_overlap) > len(customer_tokens & peer_tokens):
            return "more specialized for this use case"
    return ""


def infer_tool_candidate_tandem_with(
    run: ScenarioRunContext,
    scenario: dict[str, Any],
    selected_tool: str,
) -> dict[str, list[str]]:
    out: dict[str, list[str]] = {}
    for batch in run.latest_tool_batch_results or []:
        inference_output = batch.get("inference_output")
        if not inference_output:
            continue
        for candidate, target in extract_tandem_pairs_from_inference(batch):
            out[candidate] = sorted(set(out.get(candidate, []) + [target]))
    if selected_tool:
        for source, targets in fixture_reference_targets(scenario).items():
            if selected_tool in targets:
                out[source] = sorted(set(out.get(source, []) + [selected_tool]))
    return out


def actual_tool_candidate_reasons(run: ScenarioRunContext) -> dict[str, str]:
    out: dict[str, str] = {}
    for batch in run.latest_tool_batch_results or []:
        inference_output = batch.get("inference_output")
        if not inference_output:
            continue
        for tool_id, reason in extract_reasons_from_batch(batch).items():
            if tool_id and reason:
                out[tool_id] = reason
    return out


def extract_reasons_from_batch(batch: dict[str, Any]) -> dict[str, str]:
    out: dict[str, str] = {}
    inference_output = batch.get("inference_output")
    candidate_tools = batch.get("candidate_tools") or []
    if isinstance(inference_output, list):
        candidate_tool = candidate_tools[0] if len(candidate_tools) == 1 else ""
        for item in inference_output:
            tool_name = candidate_tool
            reason = (
                getattr(item, "comparison_with_rejected_tools_including_references_to_subtleties", "")
                or getattr(item, "applicability_rationale", "")
                or getattr(item, "relevant_subtleties", "")
            )
            if tool_name and reason:
                out[tool_name] = reason
    elif hasattr(inference_output, "tools_evaluation"):
        for item in getattr(inference_output, "tools_evaluation", []) or []:
            tool_name = normalize_tool_target(getattr(item, "name", "") or "")
            reason = (
                getattr(item, "comparison_with_alternative_tools_including_references_to_subtleties", "")
                or getattr(item, "applicability_rationale", "")
                or getattr(item, "potentially_alternative_tools", "")
            )
            if tool_name and reason:
                out[tool_name] = reason
    elif hasattr(inference_output, "reasoning_tldr"):
        reason = getattr(inference_output, "reasoning_tldr", "") or ""
        if reason:
            out[""] = reason
    return out


def extract_tandem_pairs_from_inference(batch: dict[str, Any]) -> list[tuple[str, str]]:
    out: list[tuple[str, str]] = []
    inference_output = batch.get("inference_output")
    candidate_tools = batch.get("candidate_tools") or []
    if not isinstance(inference_output, list):
        return out
    candidate_tool = candidate_tools[0] if len(candidate_tools) == 1 else ""
    for item in inference_output:
        better = normalize_tool_target(getattr(item, "potentially_better_rejected_tool_name", "") or "")
        should_tandem = bool(
            getattr(
                item,
                "the_better_rejected_tool_should_clearly_be_run_in_tandem_with_the_candidate_tool",
                False,
            )
        )
        tool_name = candidate_tool
        if tool_name and better and should_tandem:
            out.append((tool_name, better))
    return out


def infer_response_analysis_already_satisfied(run: ScenarioRunContext) -> list[str]:
    result = run.latest_response_analysis_result
    if not result:
        return []
    out: list[str] = []
    for item in getattr(result, "analyzed_guidelines", []) or []:
        if not getattr(item, "is_previously_applied", False):
            continue
        normalized = normalize_resolved_entity_id(run, getattr(item.guideline, "id", ""))
        if normalized:
            out.append(normalized)
    return sorted(set(out))


def infer_blocked_tool_state(run: ScenarioRunContext) -> str:
    result = run.latest_tool_inference_result
    if result:
        has_invalid = bool(getattr(getattr(result, "insights", None), "invalid_data", []) or [])
        has_missing = bool(getattr(getattr(result, "insights", None), "missing_data", []) or [])
        if has_invalid:
            return "blocked_invalid_args"
        if has_missing:
            return "blocked_missing_args"
    return ""


def normalize_actual_overlap_groups(run: ScenarioRunContext, tool_candidates: list[str]) -> list[list[str]]:
    groups = [
        sorted(set(batch.get("candidate_tools") or []))
        for batch in (run.latest_tool_batch_results or [])
        if batch.get("type") == "overlapping_tools" and len(batch.get("candidate_tools") or []) > 1
    ]
    if groups:
        return sorted(groups)

    candidates = []
    for tool_id in tool_candidates:
        try:
            candidates.append(ToolId.from_string(tool_id))
        except Exception:
            continue
    if len(candidates) < 2:
        return []

    adjacency: dict[str, set[str]] = {tool_id.to_string(): set() for tool_id in candidates}
    store = run.container[RelationshipStore]
    for tool_id in candidates:
        relationships = run.sync_await(
            store.list_relationships(source_id=tool_id, indirect=False, kind=RelationshipKind.OVERLAP)
        )
        relationships += run.sync_await(
            store.list_relationships(target_id=tool_id, indirect=False, kind=RelationshipKind.OVERLAP)
        )
        current = tool_id.to_string()
        for rel in relationships:
            source = normalize_tool_target(tool_id_to_string(rel.source.id))
            target = normalize_tool_target(tool_id_to_string(rel.target.id))
            if source in adjacency and target in adjacency and source != target:
                adjacency[source].add(target)
                adjacency[target].add(source)

    seen: set[str] = set()
    out: list[list[str]] = []
    for tool_id in sorted(adjacency):
        if tool_id in seen or not adjacency[tool_id]:
            continue
        stack = [tool_id]
        component: set[str] = set()
        while stack:
            current = stack.pop()
            if current in component:
                continue
            component.add(current)
            stack.extend(sorted(adjacency[current] - component))
        seen.update(component)
        if len(component) > 1:
            out.append(sorted(component))
    return sorted(out)


def infer_projected_followups(run: ScenarioRunContext, active_journey: str) -> dict[str, list[str]]:
    graph = journey_graph(run, active_journey)
    if not graph:
        return {}
    incoming_by_target: dict[str, list[str]] = {}
    outgoing_by_source: dict[str, list[str]] = {}
    for edge in graph.get("edges") or []:
        source_name = edge.get("source_name") or ""
        target_name = edge.get("target_name") or ""
        if not source_name or not target_name:
            continue
        incoming_by_target.setdefault(target_name, []).append(source_name)
        outgoing_by_source.setdefault(source_name, []).append(target_name)
    projected_ids_by_state: dict[str, list[str]] = {}
    root_name = graph.get("root_name") or ""
    for state_name in graph.get("nodes") or {}:
        ids: list[str] = []
        incoming_sources = incoming_by_target.get(state_name, [])
        if state_name == root_name or "__journey_root__" in incoming_sources:
            ids.append(projected_node_id(active_journey, state_name, "__journey_root__"))
        for source_name in incoming_sources:
            if source_name == "__journey_root__":
                continue
            ids.append(projected_node_id(active_journey, state_name, source_name))
        if ids:
            projected_ids_by_state[state_name] = sorted(set(ids))
    followups: dict[str, list[str]] = {}
    for source_name, source_projected_ids in projected_ids_by_state.items():
        targets = outgoing_by_source.get(source_name, [])
        if not targets:
            continue
        target_ids = [projected_node_id(active_journey, target_name, source_name) for target_name in targets if target_name]
        target_ids = sorted(set(target_ids))
        for source_projected_id in source_projected_ids:
            followups[source_projected_id] = target_ids
    return followups


def parlant_state_to_fixture_name(journey_title: str, state_id: str) -> str:
    graphs = getattr(journey_state_name_to_parlant_id, "_journey_graphs", {})
    graph = graphs.get(journey_title, {})
    reverse_indices = graph.get("reverse_indices") or {}
    if state_id in reverse_indices:
        return reverse_indices[state_id]
    reverse = graph.get("reverse_nodes") or {}
    return reverse.get(state_id, state_id)


def dedupe(items) -> list[str]:
    out = []
    seen = set()
    for item in items:
        if not item or item in seen:
            continue
        seen.add(item)
        out.append(item)
    return out


def main() -> int:
    scenario = json.load(sys.stdin)
    run, container_gen, cache_collection_gen = create_run_context(scenario)
    try:
        append_transcript(run, scenario.get("transcript") or [])
        apply_policy_setup(run, scenario)
        setattr(journey_state_name_to_parlant_id, "_journey_graphs", ensure_journey_graphs(run))
        append_staged_tool_calls(run, scenario)
        seed_prior_state(run, scenario)
        emitted = process(run)
        normalized = normalize(run, emitted, scenario)
        sys.stdout.write(json.dumps(normalized))
        return 0
    finally:
        run.sync_await(container_gen.aclose())
        if cache_collection_gen is not None:
            run.sync_await(cache_collection_gen.__aexit__(None, None, None))
        try:
            asyncio.get_event_loop().close()
        except RuntimeError:
            pass


if __name__ == "__main__":
    raise SystemExit(main())
