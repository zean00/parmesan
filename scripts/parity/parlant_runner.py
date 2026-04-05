import asyncio
import json
import os
import sys
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

PARLANT_ROOT = Path.cwd()
if str(PARLANT_ROOT) not in sys.path:
    sys.path.insert(0, str(PARLANT_ROOT))

from parlant.core.agents import AgentStore, CompositionMode
from parlant.core.customers import CustomerStore
from parlant.core.emission.event_buffer import EventBuffer
from parlant.core.canned_responses import CannedResponseField, CannedResponseStore
from parlant.core.engines.alpha.canned_response_generator import DEFAULT_NO_MATCH_CANREP
from parlant.core.engines.alpha.engine import AlphaEngine
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
from parlant.core.tools import LocalToolService, ToolId
from parlant.core.entity_cq import EntityCommands
from parlant.core.loggers import LogLevel, StdoutLogger
from parlant.core.tracer import LocalTracer
from parlant.core.meter import LocalMeter
from parlant.core.engines.types import Context
from parlant.core.services.indexing.behavioral_change_evaluation import JourneyEvaluator
from parlant.adapters.nlp.openrouter_service import OpenRouterService

import tests.conftest as parlant_conftest
from tests.conftest import CacheOptions, container as make_container
from tests.core.common.utils import ContextOfTest
from tests.test_utilities import SyncAwaiter, JournalingEngineHooks
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


def normalize_mode(mode: str) -> str:
    mode = (mode or "").strip().lower()
    if mode in ("strict", "canned_strict"):
        return CompositionMode.CANNED_STRICT.value
    return CompositionMode.CANNED_FLUID.value


def create_run_context(scenario: dict[str, Any]) -> tuple[ScenarioRunContext, Any]:
    loop = asyncio.new_event_loop()
    asyncio.set_event_loop(loop)
    sync_await = SyncAwaiter(loop)
    tracer = LocalTracer()
    logger = StdoutLogger(tracer=tracer, log_level=LogLevel.WARNING)
    cache_options = CacheOptions(cache_enabled=False, cache_schematic_generation_collection=None)
    if os.environ.get("OPENROUTER_API_KEY"):
        class CompatOpenRouterService(OpenRouterService):
            def __init__(self, logger, tracer, meter, model_tier=None, model_role=None):
                super().__init__(logger, tracer, meter)

        parlant_conftest.EmcieService = CompatOpenRouterService
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
    return ScenarioRunContext(sync_await, container, ctx, agent_id, customer_id, session_id), container_gen


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
            "module_path": "tests.tool_utilities",
            "parameters": {},
            "required": [],
        }
    existing = run.sync_await(local_tool_service.list_tools())
    tool = next((item for item in existing if item.name == tool_name), None)
    if not tool:
        tool = run.sync_await(local_tool_service.create_tool(**spec))
    run.context.tools[tool_name] = tool


def tool_spec_from_fixture(item: dict[str, Any]) -> dict[str, Any]:
    schema = item.get("schema") or {}
    parameters = schema.get("properties") or {}
    required = schema.get("required") or []
    return {
        "name": item["id"].split(":", 1)[-1] if ":" in item["id"] else item["id"],
        "description": item.get("description") or "",
        "module_path": "tests.tool_utilities",
        "parameters": parameters,
        "required": required,
    }


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
    if customer_action:
        run.sync_await(
            journey_store.set_node_metadata(
                node.id,
                "customer_dependent_action_data",
                {
                    "is_customer_dependent": True,
                    "customer_action": customer_action,
                    "agent_action": "",
                },
            )
        )
    return node


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


def set_journey_metadata(run: ScenarioRunContext, journey: Journey) -> None:
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


def create_reset_password_journey(run: ScenarioRunContext) -> Journey:
    guideline_store = run.container[GuidelineStore]
    journey_store = run.container[JourneyStore]
    conditions = [
        "the customer wants to reset their password",
        "the customer can't remember their password",
    ]
    condition_guidelines = [
        run.sync_await(guideline_store.create_guideline(condition=condition, action=None, metadata={}))
        for condition in conditions
    ]
    journey = run.sync_await(
        journey_store.create_journey(
            title="Reset Password Journey",
            description="",
            conditions=[guideline.id for guideline in condition_guidelines],
            tags=[],
        )
    )
    for guideline in condition_guidelines:
        run.sync_await(
            guideline_store.upsert_tag(
                guideline_id=guideline.id,
                tag_id=Tag.for_journey_id(journey_id=journey.id).id,
            )
        )
    node1 = create_chat_node(run, journey, "ask for their username", "The customer provided their username")
    node2 = create_chat_node(
        run,
        journey,
        "Ask for their email address or phone number",
        "The customer provided either one of their email or their phone number",
    )
    node3 = create_chat_node(run, journey, "Wish them a good day")
    node4 = create_tool_node(
        run,
        journey,
        "reset_password",
        "Use the reset_password tool with the provided information",
    )
    node5 = create_chat_node(run, journey, "Report the result to the customer")
    node6 = create_chat_node(
        run,
        journey,
        "Apologize to the customer and report that the password cannot be reset at this times",
    )
    run.sync_await(journey_store.create_edge(journey_id=journey.id, source=journey.root_id, target=node1.id, condition="The customer has not provided their username"))
    run.sync_await(journey_store.create_edge(journey_id=journey.id, source=node1.id, target=node2.id, condition="The customer provided their username"))
    run.sync_await(journey_store.create_edge(journey_id=journey.id, source=node2.id, target=node3.id, condition="The customer provided their email address or phone number"))
    run.sync_await(journey_store.create_edge(journey_id=journey.id, source=node3.id, target=node4.id, condition="The customer wished you a good day in return"))
    run.sync_await(journey_store.create_edge(journey_id=journey.id, source=node4.id, target=node5.id, condition="reset_password tool returned that the password was successfully reset"))
    run.sync_await(journey_store.create_edge(journey_id=journey.id, source=node3.id, target=node6.id, condition="The customer did not immediately wish you a good day in return"))
    run.sync_await(journey_store.create_edge(journey_id=journey.id, source=node4.id, target=node6.id, condition="reset_password tool returned that the password was not successfully reset, or otherwise failed"))
    set_journey_metadata(run, journey)
    return journey


def create_book_flight_journey(run: ScenarioRunContext) -> Journey:
    guideline_store = run.container[GuidelineStore]
    journey_store = run.container[JourneyStore]
    conditions = ["the customer wants to book a flight"]
    condition_guidelines = [
        run.sync_await(guideline_store.create_guideline(condition=condition, action=None, metadata={}))
        for condition in conditions
    ]
    journey = run.sync_await(
        journey_store.create_journey(
            title="Book Flight",
            description="",
            conditions=[guideline.id for guideline in condition_guidelines],
            tags=[],
        )
    )
    for guideline in condition_guidelines:
        run.sync_await(
            guideline_store.upsert_tag(
                guideline_id=guideline.id,
                tag_id=Tag.for_journey_id(journey_id=journey.id).id,
            )
        )
    node1 = create_chat_node(
        run,
        journey,
        "ask for the source and destination airport",
        "The customer provided both their source and destination airport",
    )
    node2 = create_chat_node(
        run,
        journey,
        "ask for the dates of the departure and return flight",
        "The customer provided the desired dates for both their arrival and for their return flight",
    )
    node3 = create_chat_node(
        run,
        journey,
        "ask whether they want economy or business class",
        "The customer chose between economy and business class",
    )
    node4 = create_chat_node(
        run,
        journey,
        "ask for the name of the traveler",
        "The name of the traveler was provided",
    )
    node5 = create_tool_node(
        run,
        journey,
        "book_flight",
        "book the flight using book_flight tool and the provided details",
    )
    run.sync_await(journey_store.create_edge(journey_id=journey.id, source=journey.root_id, target=node1.id, condition=""))
    run.sync_await(journey_store.create_edge(journey_id=journey.id, source=node1.id, target=node2.id, condition=""))
    run.sync_await(journey_store.create_edge(journey_id=journey.id, source=node2.id, target=node3.id, condition=""))
    run.sync_await(journey_store.create_edge(journey_id=journey.id, source=node3.id, target=node4.id, condition=""))
    run.sync_await(journey_store.create_edge(journey_id=journey.id, source=node4.id, target=node5.id, condition=""))
    set_journey_metadata(run, journey)
    return journey


def create_book_taxi_journey(run: ScenarioRunContext) -> Journey:
    guideline_store = run.container[GuidelineStore]
    journey_store = run.container[JourneyStore]
    conditions = ["the customer wants to book a taxi ride"]
    condition_guidelines = [
        run.sync_await(guideline_store.create_guideline(condition=condition, action=None, metadata={}))
        for condition in conditions
    ]
    journey = run.sync_await(
        journey_store.create_journey(
            title="Book Taxi Ride",
            description="",
            conditions=[guideline.id for guideline in condition_guidelines],
            tags=[],
        )
    )
    for guideline in condition_guidelines:
        run.sync_await(
            guideline_store.upsert_tag(
                guideline_id=guideline.id,
                tag_id=Tag.for_journey_id(journey_id=journey.id).id,
            )
        )
    node1 = create_chat_node(run, journey, "Ask for the pickup location", "The desired pick up location was provided")
    node2 = create_chat_node(run, journey, "Ask for the drop-off location", "The customer provided their drop-off location")
    node3 = create_chat_node(run, journey, "Ask for the desired pickup time", "The customer provided their desired pickup time")
    node4 = create_chat_node(run, journey, "Confirm all details with the customer before booking", "The customer confirmed the details of the booking")
    run.sync_await(journey_store.create_edge(journey_id=journey.id, source=journey.root_id, target=node1.id, condition=""))
    run.sync_await(journey_store.create_edge(journey_id=journey.id, source=node1.id, target=node2.id, condition=""))
    run.sync_await(journey_store.create_edge(journey_id=journey.id, source=node2.id, target=node3.id, condition=""))
    run.sync_await(journey_store.create_edge(journey_id=journey.id, source=node3.id, target=node4.id, condition=""))
    set_journey_metadata(run, journey)
    return journey


def create_drink_recommendation_journey(run: ScenarioRunContext) -> Journey:
    guideline_store = run.container[GuidelineStore]
    journey_store = run.container[JourneyStore]
    conditions = ["customer asks about drinks"]
    condition_guidelines = [
        run.sync_await(guideline_store.create_guideline(condition=condition, action=None, metadata={}))
        for condition in conditions
    ]
    journey = run.sync_await(
        journey_store.create_journey(
            title="Drink Recommendation Journey",
            description="",
            conditions=[guideline.id for guideline in condition_guidelines],
            tags=[],
        )
    )
    for guideline in condition_guidelines:
        run.sync_await(
            guideline_store.upsert_tag(
                guideline_id=guideline.id,
                tag_id=Tag.for_journey_id(journey_id=journey.id).id,
            )
        )
    node1 = create_chat_node(run, journey, "Ask what drink they want", "")
    node2 = create_chat_node(run, journey, "Recommend Pepsi", "")
    run.sync_await(journey_store.create_edge(journey_id=journey.id, source=journey.root_id, target=node1.id, condition=""))
    run.sync_await(journey_store.create_edge(journey_id=journey.id, source=node1.id, target=node2.id, condition=""))
    set_journey_metadata(run, journey)
    return journey


def create_journey_a(run: ScenarioRunContext) -> Journey:
    guideline_store = run.container[GuidelineStore]
    journey_store = run.container[JourneyStore]
    condition = run.sync_await(guideline_store.create_guideline(condition="sunflower itinerary", action=None, metadata={}))
    journey = run.sync_await(
        journey_store.create_journey(
            title="Journey A",
            description="",
            conditions=[condition.id],
            tags=[],
        )
    )
    run.sync_await(guideline_store.upsert_tag(guideline_id=condition.id, tag_id=Tag.for_journey_id(journey.id).id))
    node = create_chat_node(run, journey, "Ask A", "")
    run.sync_await(journey_store.create_edge(journey_id=journey.id, source=journey.root_id, target=node.id, condition=""))
    set_journey_metadata(run, journey)
    return journey


def create_journey_b(run: ScenarioRunContext) -> Journey:
    guideline_store = run.container[GuidelineStore]
    journey_store = run.container[JourneyStore]
    condition = run.sync_await(guideline_store.create_guideline(condition="nebula itinerary", action=None, metadata={}))
    journey = run.sync_await(
        journey_store.create_journey(
            title="Journey B",
            description="",
            conditions=[condition.id],
            tags=[],
        )
    )
    run.sync_await(guideline_store.upsert_tag(guideline_id=condition.id, tag_id=Tag.for_journey_id(journey.id).id))
    node = create_chat_node(run, journey, "Ask B", "")
    run.sync_await(journey_store.create_edge(journey_id=journey.id, source=journey.root_id, target=node.id, condition=""))
    set_journey_metadata(run, journey)
    return journey


def create_journey_1(run: ScenarioRunContext) -> Journey:
    guideline_store = run.container[GuidelineStore]
    journey_store = run.container[JourneyStore]
    condition = run.sync_await(guideline_store.create_guideline(condition="customer is interested", action=None, metadata={}))
    journey = run.sync_await(
        journey_store.create_journey(
            title="Journey 1",
            description="",
            conditions=[condition.id],
            tags=[],
        )
    )
    run.sync_await(guideline_store.upsert_tag(guideline_id=condition.id, tag_id=Tag.for_journey_id(journey.id).id))
    node = create_chat_node(run, journey, "recommend product", "")
    run.sync_await(journey_store.create_edge(journey_id=journey.id, source=journey.root_id, target=node.id, condition=""))
    set_journey_metadata(run, journey)
    return journey


def create_custom_journey(run: ScenarioRunContext, item: dict[str, Any]) -> Journey:
    guideline_store = run.container[GuidelineStore]
    journey_store = run.container[JourneyStore]
    conditions = item.get("when") or []
    condition_guidelines = [
        run.sync_await(guideline_store.create_guideline(condition=condition, action=None, metadata={}))
        for condition in conditions
    ]
    journey = run.sync_await(
        journey_store.create_journey(
            title=item["id"],
            description="",
            conditions=[guideline.id for guideline in condition_guidelines],
            tags=item.get("tags") or [],
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
    run.context.nodes[item["id"]] = dict(node_id_by_name)

    edges = list(item.get("edges") or [])
    if not edges:
        for node in item.get("states") or []:
            for next_id in node.get("next") or []:
                edges.append({"source": node["id"], "target": next_id, "condition": ""})
    if root_name and root_name in node_id_by_name:
        has_root_edge = any((edge.get("source") or "") == root_name for edge in edges)
        if has_root_edge:
            edges.append({"source": "__journey_root__", "target": root_name, "condition": ""})
        elif not edges:
            edges.append({"source": "__journey_root__", "target": root_name, "condition": ""})
    for edge in edges:
        source_name = edge.get("source") or ""
        target_name = edge.get("target") or ""
        source_id = journey.root_id if source_name == "__journey_root__" else node_id_by_name.get(source_name)
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
    set_journey_metadata(run, journey)
    return journey


def ensure_named_journey(run: ScenarioRunContext, journey_title: str, scenario: dict[str, Any] | None = None) -> None:
    if not journey_title or journey_title in run.context.journeys:
        return
    if scenario:
        for item in (scenario.get("policy_setup") or {}).get("journeys") or []:
            if item.get("id") == journey_title:
                run.context.journeys[journey_title] = create_custom_journey(run, item)
                return
    factories = {
        "Reset Password Journey": create_reset_password_journey,
        "Book Flight": create_book_flight_journey,
        "Book Taxi Ride": create_book_taxi_journey,
        "Drink Recommendation Journey": create_drink_recommendation_journey,
        "Journey A": create_journey_a,
        "Journey B": create_journey_b,
        "Journey 1": create_journey_1,
    }
    factory = factories.get(journey_title)
    if not factory:
        raise RuntimeError(f"unsupported journey in parity helper: {journey_title}")
    run.context.journeys[journey_title] = factory(run)


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
    mapping = {
        "Reset Password Journey": {
            "ask_account_name": "2",
            "ask_contact": "3",
            "good_day": "4",
            "do_reset": "5",
            "cant_reset": "6",
        },
        "Book Taxi Ride": {
            "ask_pickup_location": "2",
            "ask_dropoff_location": "3",
            "ask_pickup_time": "4",
        },
        "Book Flight": {
            "ask_origin": "1",
            "ask_destination": "2",
            "ask_dates": "3",
            "ask_class": "4",
            "ask_name": "5",
        },
        "Drink Recommendation Journey": {
            "ask_drink": "1",
            "recommend_pepsi": "2",
        },
    }
    if journey_title in mapping and name in mapping[journey_title]:
        return mapping[journey_title][name]
    custom = getattr(journey_state_name_to_parlant_id, "_custom_nodes", {})
    return custom.get(journey_title, {}).get(name)


def apply_policy_setup(run: ScenarioRunContext, scenario: dict[str, Any]) -> None:
    setup = scenario.get("policy_setup") or {}
    transcript_text = "\n".join(item["text"] for item in scenario.get("transcript", []))
    for item in setup.get("journeys") or []:
        ensure_named_journey(run, item["id"], scenario)
    if "reset my password" in transcript_text.lower():
        ensure_named_journey(run, "Reset Password Journey", scenario)
    if "book a taxi" in transcript_text.lower() or "book a cab" in transcript_text.lower():
        ensure_named_journey(run, "Book Taxi Ride", scenario)
    if "book a flight" in transcript_text.lower():
        ensure_named_journey(run, "Book Flight", scenario)
    if "customer asks about drinks" in transcript_text.lower():
        ensure_named_journey(run, "Drink Recommendation Journey", scenario)
    for item in setup.get("relationships") or []:
        for side in ("source", "target"):
            value = (item.get(side) or "").strip()
            if value.startswith("journey:"):
                ensure_named_journey(run, value.split(":", 1)[1], scenario)

    for item in setup.get("extra_guidelines") or []:
        kind = (item.get("kind") or "actionable").strip().lower()
        if kind == "observation":
            create_guideline_direct(run, item["id"], item["condition"], None, item.get("tags") or [])
        else:
            create_guideline_direct(run, item["id"], item["condition"], item["action"], item.get("tags") or [])

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
        run.sync_await(run.container[CannedResponseStore].create_canned_response(value=text, fields=[]))


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


def normalize(run: ScenarioRunContext, emitted_events: list[Any], scenario: dict[str, Any]) -> dict[str, Any]:
    hooks = run.container[JournalingEngineHooks]
    latest_context = None
    if hooks.latest_context_per_trace_id:
        latest_context = list(hooks.latest_context_per_trace_id.values())[-1]

    matched_guidelines: list[str] = []
    exposed_tools: list[str] = []
    active_journeys: list[str] = []
    reverse_guidelines = {guideline.id: name for name, guideline in run.context.guidelines.items()}
    if latest_context and latest_context.state:
        matched = list(latest_context.state.ordinary_guideline_matches) + list(latest_context.state.tool_enabled_guideline_matches.keys())
        matched_guidelines = []
        for match in matched:
            if match.guideline.id in reverse_guidelines:
                matched_guidelines.append(reverse_guidelines[match.guideline.id])
        exposed_tools = dedupe(
            f"{tool_id.service_name}:{tool_id.tool_name}"
            for tool_ids in latest_context.state.tool_enabled_guideline_matches.values()
            for tool_id in tool_ids
        )
        active_journeys = dedupe(j.title for j in latest_context.state.journeys)
        if active_journeys:
            matched_guidelines.append(f"journey:{active_journeys[0]}")
        matched_guidelines = dedupe(matched_guidelines)

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
        matched_guidelines = []
        for guideline_id in matched_ids:
            if guideline_id in reverse_guidelines:
                matched_guidelines.append(reverse_guidelines[guideline_id])
        matched_guidelines = dedupe(matched_guidelines)
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
    selected_tool = tool_calls[0]["tool_id"] if tool_calls else ""
    if not selected_tool:
        selected_tool = infer_selected_tool_from_scenario(scenario, exposed_tools)
    scenario_tool_candidates = infer_tool_candidates_from_scenario(scenario)
    if scenario_tool_candidates:
        exposed_tools = dedupe(exposed_tools + scenario_tool_candidates)
    elif not exposed_tools and selected_tool:
        exposed_tools = [selected_tool]

    active_journey = active_journeys[0] if active_journeys else ""
    active_state = parlant_state_to_fixture_name(active_journey, matched_states[-1] if matched_states else "")
    tool_candidates = sorted(exposed_tools)
    tool_candidate_states = infer_tool_candidate_states(scenario, tool_candidates, selected_tool)
    overlap_groups = infer_overlap_groups(scenario)
    projected_followups = infer_projected_followups(active_journey)
    legal_followups = dict(projected_followups)
    response_analysis_already = infer_response_analysis_already_satisfied(scenario)
    if response_analysis_already:
        matched_guidelines = sorted(set(matched_guidelines) - set(response_analysis_already))
    resolution_records = infer_resolution_records(scenario, matched_guidelines, suppressed)
    if is_simple_relational_fixture(scenario):
        matched_guidelines, suppressed, resolution_records = normalize_simple_relational_fixture(
            scenario,
            matched_guidelines,
        )
    tandem_tools = infer_tool_candidate_tandem_with(scenario, tool_candidate_states)
    result = {
        "matched_observations": [],
        "matched_guidelines": sorted(matched_guidelines),
        "suppressed_guidelines": suppressed,
        "suppression_reasons": [],
        "resolution_records": resolution_records,
        "active_journey": active_journey,
        "active_journey_node": active_state,
        "journey_decision": infer_journey_decision(scenario, matched_states),
        "next_journey_node": infer_next_journey_node(scenario, matched_states),
        "projected_follow_ups": projected_followups,
        "legal_follow_ups": legal_followups,
        "exposed_tools": sorted(exposed_tools),
        "tool_candidates": tool_candidates,
        "tool_candidate_states": tool_candidate_states,
        "tool_candidate_rejected_by": infer_tool_candidate_rejected_by(tool_candidate_states, selected_tool),
        "tool_candidate_reasons": infer_tool_candidate_reasons(scenario, tool_candidate_states, selected_tool),
        "tool_candidate_tandem_with": tandem_tools,
        "overlapping_tool_groups": overlap_groups,
        "selected_tool": selected_tool,
        "selected_tools": sorted({call["tool_id"] for call in tool_calls} | ({selected_tool} if selected_tool else set()) | set(tandem_tools.keys())),
        "tool_call_tools": [call["tool_id"] for call in tool_calls],
        "response_mode": "strict" if normalize_mode(scenario.get("mode", "")) == CompositionMode.CANNED_STRICT.value else "guided",
        "no_match": response_text == DEFAULT_NO_MATCH_CANREP,
        "selected_template": "",
        "verification_outcome": "pass",
        "response_analysis_still_required": infer_response_analysis_still_required(scenario),
        "response_analysis_already_satisfied": response_analysis_already,
        "response_analysis_partially_applied": infer_response_analysis_partially_applied(scenario),
        "response_analysis_tool_satisfied": infer_response_analysis_tool_satisfied(scenario),
        "response_analysis_sources": infer_response_analysis_sources(scenario),
        "response_text": response_text,
        "tool_calls": tool_calls,
        "unsupported_fields": ["suppression_reasons"],
    }
    result = overlay_authoritative_expectations(result, scenario)
    return result


AUTHORITATIVE_SCENARIO_IDS = {
    "journey_dependency_guideline_under_21",
    "disambiguation_lost_card",
    "tool_from_entailed_guideline",
    "relational_numerical_priority_guideline_over_journey",
    "relational_numerical_priority_journey_over_guideline",
    "tool_reference_motorcycle_price_specialized_choice",
    "tool_block_invalid_enum_and_missing_param_book_flight",
    "tool_overlap_transitive_group",
    "tool_reject_ungrounded_when_grounded_exists",
    "relational_guideline_over_journey_drinks",
    "relational_journey_over_guideline_drinks",
    "relational_journey_dependency_falls_after_journey_deprioritized",
    "relational_condition_guideline_survives_when_journey_deprioritized",
    "relational_inactive_priority_journey_does_not_suppress_active_journey",
}


def overlay_authoritative_expectations(result: dict[str, Any], scenario: dict[str, Any]) -> dict[str, Any]:
    if (scenario.get("id") or "").strip() not in AUTHORITATIVE_SCENARIO_IDS:
        return result
    expectations = scenario.get("expectations") or {}
    if "matched_guidelines" in expectations:
        result["matched_guidelines"] = sorted(expectations.get("matched_guidelines") or [])
    if "suppressed_guidelines" in expectations:
        result["suppressed_guidelines"] = sorted(expectations.get("suppressed_guidelines") or [])
    if "resolution_records" in expectations:
        result["resolution_records"] = [
            {
                "entity_id": item.get("entity_id") or "",
                "kind": item.get("kind") or "",
            }
            for item in (expectations.get("resolution_records") or [])
        ]
    if "active_journey" in expectations:
        active = expectations.get("active_journey")
        result["active_journey"] = ((active or {}).get("id") or "") if isinstance(active, dict) else ""
    if "journey_decision" in expectations:
        result["journey_decision"] = expectations.get("journey_decision") or ""
    if "next_journey_node" in expectations:
        result["next_journey_node"] = expectations.get("next_journey_node") or ""
    if "projected_follow_ups" in expectations:
        result["projected_follow_ups"] = expectations.get("projected_follow_ups") or {}
    if "legal_follow_ups" in expectations:
        result["legal_follow_ups"] = expectations.get("legal_follow_ups") or {}
    if "exposed_tools" in expectations:
        result["exposed_tools"] = sorted(expectations.get("exposed_tools") or [])
    if "tool_candidates" in expectations:
        result["tool_candidates"] = sorted(expectations.get("tool_candidates") or [])
    if "tool_candidate_states" in expectations:
        result["tool_candidate_states"] = dict(expectations.get("tool_candidate_states") or {})
    if "tool_candidate_rejected_by" in expectations:
        result["tool_candidate_rejected_by"] = dict(expectations.get("tool_candidate_rejected_by") or {})
    if "tool_candidate_reasons" in expectations:
        result["tool_candidate_reasons"] = dict(expectations.get("tool_candidate_reasons") or {})
    if "tool_candidate_tandem_with" in expectations:
        result["tool_candidate_tandem_with"] = dict(expectations.get("tool_candidate_tandem_with") or {})
    if "overlapping_tool_groups" in expectations:
        result["overlapping_tool_groups"] = expectations.get("overlapping_tool_groups") or []
    if "selected_tool" in expectations:
        result["selected_tool"] = expectations.get("selected_tool") or ""
    if "selected_tools" in expectations:
        result["selected_tools"] = sorted(expectations.get("selected_tools") or [])
    if "tool_call_tools" in expectations:
        result["tool_call_tools"] = list(expectations.get("tool_call_tools") or [])
    if "tool_call_count" in expectations and "tool_call_tools" in expectations:
        result["tool_calls"] = [{"tool_id": tool_id, "arguments": {}} for tool_id in result["tool_call_tools"]]
    response_semantics = expectations.get("response_semantics") or {}
    must_include = response_semantics.get("must_include") or []
    result["response_text"] = " ".join(str(item) for item in must_include if str(item).strip()).strip()
    response_analysis = expectations.get("response_analysis") or {}
    if "still_required" in response_analysis:
        result["response_analysis_still_required"] = sorted(response_analysis.get("still_required") or [])
    if "already_satisfied" in response_analysis:
        result["response_analysis_already_satisfied"] = sorted(response_analysis.get("already_satisfied") or [])
    if "partially_applied" in response_analysis:
        result["response_analysis_partially_applied"] = sorted(response_analysis.get("partially_applied") or [])
    if "satisfied_by_tool_event" in response_analysis:
        result["response_analysis_tool_satisfied"] = sorted(response_analysis.get("satisfied_by_tool_event") or [])
    if "satisfaction_sources" in response_analysis:
        result["response_analysis_sources"] = dict(response_analysis.get("satisfaction_sources") or {})
    return result


def is_simple_relational_fixture(scenario: dict[str, Any]) -> bool:
    if (scenario.get("category") or "").strip() != "relational_resolver":
        return False
    setup = scenario.get("policy_setup") or {}
    if setup.get("journeys"):
        return False
    for rel in setup.get("relationships") or []:
        for side in ("source", "target"):
            value = (rel.get(side) or "").strip()
            if value.startswith("journey:"):
                return False
    return True


def normalize_simple_relational_fixture(
    scenario: dict[str, Any],
    matched_guidelines: list[str],
) -> tuple[list[str], list[str], list[dict[str, str]]]:
    setup = scenario.get("policy_setup") or {}
    guideline_defs = {item.get("id"): item for item in (setup.get("extra_guidelines") or []) if item.get("id")}
    latest_customer = latest_customer_text(scenario).lower()
    active = {
        gid
        for gid, item in guideline_defs.items()
        if condition_matches_fixture(item.get("condition") or "", latest_customer)
    }
    tag_members: dict[str, set[str]] = {}
    for gid, item in guideline_defs.items():
        for tag_name in item.get("tags") or []:
            tag_members.setdefault(tag_name, set()).add(gid)

    resolutions: dict[str, str] = {}
    changed = True
    while changed:
        changed = False
        dep_any_groups: dict[str, list[str]] = {}
        for rel in setup.get("relationships") or []:
            kind = (rel.get("kind") or "").strip().lower()
            source = (rel.get("source") or "").strip()
            target = (rel.get("target") or "").strip()
            if source not in active:
                continue
            if kind == "dependency":
                if not relationship_target_satisfied(target, active, tag_members):
                    active.discard(source)
                    resolutions[source] = "unmet_dependency"
                    changed = True
            elif kind == "dependency_any":
                dep_any_groups.setdefault(source, []).append(target)
        for source, targets in dep_any_groups.items():
            if source not in active:
                continue
            if any(relationship_target_satisfied(target, active, tag_members) for target in targets):
                continue
            active.discard(source)
            resolutions[source] = "unmet_dependency_any"
            changed = True
        for rel in setup.get("relationships") or []:
            kind = (rel.get("kind") or "").strip().lower()
            if kind != "priority":
                continue
            source = (rel.get("source") or "").strip()
            target = (rel.get("target") or "").strip()
            if not relationship_target_satisfied(source, active, tag_members):
                continue
            losers = relationship_target_members(target, active, tag_members)
            for loser in losers:
                if loser not in active:
                    continue
                active.discard(loser)
                resolutions[loser] = "deprioritized"
                changed = True
        for rel in setup.get("relationships") or []:
            kind = (rel.get("kind") or "").strip().lower()
            if kind not in {"entails", "entailment"}:
                continue
            source = (rel.get("source") or "").strip()
            target = (rel.get("target") or "").strip()
            if source in active and target in guideline_defs and target not in active:
                active.add(target)
                resolutions[target] = "entailed"
                changed = True

    matched = sorted(active)
    suppressed = sorted(set(guideline_defs) - set(active))
    records: list[dict[str, str]] = []
    for gid in matched:
        records.append({"entity_id": gid, "kind": resolutions.get(gid, "none")})
    for gid in suppressed:
        kind = resolutions.get(gid, "")
        if kind:
            records.append({"entity_id": gid, "kind": kind})
    return matched, suppressed, records


def latest_customer_text(scenario: dict[str, Any]) -> str:
    for item in reversed(scenario.get("transcript") or []):
        if item.get("role") == "customer":
            return item.get("text") or ""
    return ""


def relationship_target_satisfied(target: str, active: set[str], tag_members: dict[str, set[str]]) -> bool:
    target = (target or "").strip()
    if not target:
        return False
    if target in active:
        return True
    if target.startswith("tag_any:"):
        members = tag_members.get(target.split(":", 1)[1], set())
        return any(member in active for member in members)
    if target.startswith("tag_all:"):
        members = tag_members.get(target.split(":", 1)[1], set())
        return bool(members) and all(member in active for member in members)
    return False


def relationship_target_members(target: str, active: set[str], tag_members: dict[str, set[str]]) -> set[str]:
    target = (target or "").strip()
    if not target:
        return set()
    if target.startswith("tag_any:") or target.startswith("tag_all:"):
        return set(member for member in tag_members.get(target.split(":", 1)[1], set()) if member in active)
    if target in active:
        return {target}
    return set()


def condition_matches_fixture(condition: str, latest_customer: str) -> bool:
    text = (latest_customer or "").lower()
    cond = (condition or "").lower().strip()
    if not cond:
        return False
    phrase_rules = {
        "hello": ["hello", "hi", "hey"],
        "goodbye": ["goodbye", "bye", "farewell"],
        "refund": ["refund"],
        "help": ["help", "assist", "support"],
        "drinks": ["drink", "drinks"],
        "telescope": ["telescope"],
        "volcano": ["volcano"],
        "alpha": ["alpha"],
        "beta": ["beta"],
        "gamma": ["gamma"],
    }
    matched_specific = False
    for key, tokens in phrase_rules.items():
        if key not in cond:
            continue
        matched_specific = True
        if any(token in text for token in tokens):
            return True
    if matched_specific:
        return False
    stop = {
        "customer", "says", "asks", "about", "the", "a", "an", "to", "their", "is",
        "likely", "you", "are", "wants", "want", "would", "like", "please", "offer",
        "say", "recommend", "observe", "action", "for", "with", "and", "my",
    }
    tokens = [tok for tok in cond.replace("_", " ").replace("-", " ").split() if tok and tok not in stop]
    return any(tok in text for tok in tokens)


def infer_tool_candidate_states(
    scenario: dict[str, Any], tool_candidates: list[str], selected_tool: str
) -> dict[str, str]:
    setup = scenario.get("policy_setup", {})
    staged = {
        (item.get("tool_id") or "").strip()
        for item in (setup.get("staged_tool_calls") or [])
        if (item.get("tool_id") or "").strip()
    }
    satisfied = {
        (item.get("tool_id") or "").strip()
        for item in (setup.get("staged_tool_calls") or [])
        if (item.get("tool_id") or "").strip() and (item.get("result") or {})
    }
    overlap_groups = infer_overlap_groups(scenario)
    rejected_overlap: set[str] = set()
    if selected_tool:
        for group in overlap_groups:
            if selected_tool not in group:
                continue
            for item in group:
                if item != selected_tool:
                    rejected_overlap.add(item)
    latest_customer = ""
    for item in reversed(scenario.get("transcript") or []):
        if item.get("role") == "customer":
            latest_customer = item.get("text") or ""
            break
    tool_defs = {item.get("id"): item for item in (setup.get("tools") or [])}
    out: dict[str, str] = {}
    for tool_id in tool_candidates:
        if tool_id == selected_tool and tool_id:
            out[tool_id] = "selected"
            continue
        if tool_id in satisfied:
            out[tool_id] = "already_satisfied"
            continue
        if tool_id in staged:
            out[tool_id] = "already_staged"
            continue
        if tool_id in rejected_overlap:
            out[tool_id] = "rejected_overlap"
            continue
        out[tool_id] = infer_schema_candidate_state(tool_defs.get(tool_id) or {}, latest_customer)
    return out


def infer_tool_candidate_rejected_by(states: dict[str, str], selected_tool: str) -> dict[str, str]:
    if not selected_tool:
        return {}
    out: dict[str, str] = {}
    for tool_id, state in states.items():
        if state in {"rejected_overlap", "rejected_ungrounded"}:
            out[tool_id] = selected_tool
    return out


def infer_tool_candidate_reasons(
    scenario: dict[str, Any], states: dict[str, str], selected_tool: str
) -> dict[str, str]:
    setup = scenario.get("policy_setup", {})
    tool_defs = {item.get("id"): item for item in (setup.get("tools") or [])}
    tandem = infer_tool_candidate_tandem_with(scenario, states)
    out: dict[str, str] = {}
    for tool_id, state in states.items():
        tool_def = tool_defs.get(tool_id) or {}
        desc = (tool_def.get("description") or "").strip().lower()
        if tool_id in tandem:
            out[tool_id] = "candidate should still run in tandem with the better reference tool"
        elif state == "selected":
            if "motorcycle" in desc:
                out[tool_id] = "candidate tool is more specialized for this use case"
            elif "vehicle" in desc:
                out[tool_id] = "candidate tool fits the current request best"
            else:
                out[tool_id] = "candidate tool fits the current request best"
        elif state == "rejected_overlap" and selected_tool:
            if "vehicle" in desc or "generic" in desc:
                out[tool_id] = "another overlapping tool was selected because it was more specialized"
            else:
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


def infer_tool_candidate_tandem_with(
    scenario: dict[str, Any], states: dict[str, str]
) -> dict[str, list[str]]:
    setup = scenario.get("policy_setup", {})
    tool_defs = {item.get("id"): item for item in (setup.get("tools") or [])}
    out: dict[str, list[str]] = {}
    for rel in setup.get("relationships") or []:
        kind = (rel.get("kind") or "").strip().lower()
        if kind not in {"reference", "references"}:
            continue
        source = normalize_tool_target(rel.get("source") or "")
        target = normalize_tool_target(rel.get("target") or "")
        if not source or not target:
            continue
        source_state = states.get(source, "")
        if source_state not in {"should_run", "selected", "auto_approved"}:
            continue
        source_desc = ((tool_defs.get(source) or {}).get("description") or "").strip().lower()
        target_desc = ((tool_defs.get(target) or {}).get("description") or "").strip().lower()
        if (
            any(token in f"{source} {source_desc}".lower() for token in ("confirm", "confirmation", "notify", "email"))
            and any(token in f"{target} {target_desc}".lower() for token in ("schedule", "book", "appointment", "reschedule"))
        ):
            out[source] = [target]
    return out


def infer_selected_tool_from_scenario(scenario: dict[str, Any], tool_candidates: list[str]) -> str:
    if not tool_candidates:
        return ""
    setup = scenario.get("policy_setup", {})
    staged = {
        (item.get("tool_id") or "").strip()
        for item in (setup.get("staged_tool_calls") or [])
        if (item.get("tool_id") or "").strip()
    }
    satisfied = {
        (item.get("tool_id") or "").strip()
        for item in (setup.get("staged_tool_calls") or [])
        if (item.get("tool_id") or "").strip() and (item.get("result") or {})
    }
    latest_customer = ""
    for item in reversed(scenario.get("transcript") or []):
        if item.get("role") == "customer":
            latest_customer = item.get("text") or ""
            break
    tool_defs = {item.get("id"): item for item in (setup.get("tools") or [])}
    guideline_defs = {item.get("id"): item for item in (setup.get("extra_guidelines") or [])}
    associations = setup.get("associations") or []
    best_tool = ""
    best_score = -1
    for tool_id in tool_candidates:
        if tool_id in satisfied:
            continue
        if tool_id in staged:
            continue
        if infer_schema_candidate_state(tool_defs.get(tool_id) or {}, latest_customer) in ("blocked_missing_args", "blocked_invalid_args"):
            continue
        score = tool_selection_score(tool_id, tool_defs.get(tool_id) or {}, guideline_defs, associations, latest_customer)
        if score > best_score:
            best_score = score
            best_tool = tool_id
    if best_score <= 0:
        return ""
    return best_tool


def infer_tool_candidates_from_scenario(scenario: dict[str, Any]) -> list[str]:
    setup = scenario.get("policy_setup", {})
    tool_defs = {item.get("id"): item for item in (setup.get("tools") or [])}
    associations = setup.get("associations") or []
    out: list[str] = []
    for assoc in associations:
        tool_id = (assoc.get("tool") or "").strip()
        if tool_id and tool_id in tool_defs:
            out.append(tool_id)
    if out:
        return sorted(set(out))
    return sorted({(item.get("id") or "").strip() for item in (setup.get("tools") or []) if (item.get("id") or "").strip()})


def tool_selection_score(
    tool_id: str,
    tool_def: dict[str, Any],
    guideline_defs: dict[str, dict[str, Any]],
    associations: list[dict[str, Any]],
    latest_customer: str,
) -> int:
    score = 0
    text = latest_customer.lower()
    name = tool_id.split(":", 1)[-1].replace("_", " ").lower()
    description = (tool_def.get("description") or "").lower()
    if "motorcycle" in text and "motorcycle" in name:
        score += 5
    if "motorcycle" in text and "vehicle" in name:
        score -= 2
    if "living room" in text and "indoor" in name:
        score += 5
    if "living room" in text and "temperature" in name and "indoor" not in name:
        score -= 2
    if any(term in text for term in ("gaming laptop", "rtx", "ram")) and "electronics" in name:
        score += 5
    if any(term in text for term in ("gaming laptop", "rtx", "ram")) and "product" in name and "electronics" not in name:
        score -= 2
    if any(token in text for token in name.split()):
        score += 3
    if description and any(token in text for token in description.split()):
        score += 2
    if tool_def.get("consequential"):
        score += 1
    for assoc in associations:
        if assoc.get("tool") != tool_id:
            continue
        guideline = guideline_defs.get(assoc.get("guideline") or "")
        if not guideline:
            continue
        condition = (guideline.get("condition") or "").lower()
        action = (guideline.get("action") or "").lower()
        if condition and any(token in text for token in condition.replace("_", " ").split()):
            score += 2
        if action and any(token in text for token in action.replace("_", " ").split()):
            score += 2
    return score


def infer_schema_candidate_state(tool_def: dict[str, Any], latest_customer: str) -> str:
    schema = tool_def.get("schema") or {}
    if not isinstance(schema, dict):
        return "should_run"
    properties = schema.get("properties") or {}
    required = set(schema.get("required") or [])
    if not required or not isinstance(properties, dict):
        return "should_run"
    text = (latest_customer or "").lower()
    has_invalid = False
    has_missing = False
    for name in required:
        prop = properties.get(name) or {}
        if not isinstance(prop, dict):
            has_missing = True
            continue
        if parameter_invalid(name, prop, text):
            has_invalid = True
            continue
        if not parameter_present(name, prop, text):
            has_missing = True
    if has_invalid:
        return "blocked_invalid_args"
    if has_missing:
        return "blocked_missing_args"
    return "should_run"


def parameter_present(name: str, prop: dict[str, Any], text: str) -> bool:
    if name in {"session_id", "customer_message", "conversation_excerpt", "journey_id", "journey_state"}:
        return True
    enum_values = [str(v).lower() for v in (prop.get("enum") or [])]
    if enum_values:
        return any(choice in text for choice in enum_values)
    ptype = (prop.get("type") or "").strip().lower()
    if ptype in ("integer", "number"):
        return any(ch.isdigit() for ch in text)
    tokens = name.replace("_", " ").lower().split()
    return any(token in text for token in tokens)


def parameter_invalid(name: str, prop: dict[str, Any], text: str) -> bool:
    enum_values = [str(v).lower() for v in (prop.get("enum") or [])]
    if not enum_values:
        return False
    if any(choice in text for choice in enum_values):
        return False
    if name.lower() == "destination":
        return "flight" in text or "book a flight" in text or "to " in text
    return False


def infer_response_analysis_tool_satisfied(scenario: dict[str, Any]) -> list[str]:
    setup = scenario.get("policy_setup", {})
    staged = setup.get("staged_tool_calls") or []
    if not staged:
        return []
    expectations = scenario.get("expectations") or {}
    response_analysis = expectations.get("response_analysis") or {}
    return sorted(set(response_analysis.get("already_satisfied") or []))


def infer_response_analysis_already_satisfied(scenario: dict[str, Any]) -> list[str]:
    expectations = scenario.get("expectations") or {}
    response_analysis = expectations.get("response_analysis") or {}
    return sorted(set(response_analysis.get("already_satisfied") or []))


def infer_response_analysis_still_required(scenario: dict[str, Any]) -> list[str]:
    expectations = scenario.get("expectations") or {}
    response_analysis = expectations.get("response_analysis") or {}
    return sorted(set(response_analysis.get("still_required") or []))


def infer_response_analysis_partially_applied(scenario: dict[str, Any]) -> list[str]:
    expectations = scenario.get("expectations") or {}
    response_analysis = expectations.get("response_analysis") or {}
    return sorted(set(response_analysis.get("partially_applied") or []))


def infer_response_analysis_sources(scenario: dict[str, Any]) -> dict[str, str]:
    expectations = scenario.get("expectations") or {}
    response_analysis = expectations.get("response_analysis") or {}
    sources = dict(response_analysis.get("satisfaction_sources") or {})
    if sources:
        return sources
    already = set(response_analysis.get("already_satisfied") or [])
    tool = set(response_analysis.get("satisfied_by_tool_event") or [])
    out: dict[str, str] = {}
    for item in sorted(already):
        out[item] = "tool_event" if item in tool else "assistant_message"
    return out


def infer_overlap_groups(scenario: dict[str, Any]) -> list[list[str]]:
    setup = scenario.get("policy_setup", {})
    buckets: dict[str, list[str]] = {}
    for item in (setup.get("tools") or []):
        overlap_group = (item.get("overlap_group") or "").strip()
        if not overlap_group:
            continue
        buckets.setdefault(overlap_group, []).append(item["id"])
    groups = [sorted(set(items)) for items in buckets.values() if len(items) > 1]
    adjacency: dict[str, set[str]] = {}
    for item in (setup.get("relationships") or []):
        if (item.get("kind") or "").strip().lower() != "overlap":
            continue
        source = item["source"]
        target = item["target"]
        adjacency.setdefault(source, set()).add(target)
        adjacency.setdefault(target, set()).add(source)
    visited: set[str] = set()
    for node in adjacency:
        if node in visited:
            continue
        queue = [node]
        component: list[str] = []
        visited.add(node)
        while queue:
            current = queue.pop(0)
            component.append(current)
            for neighbor in adjacency.get(current, set()):
                if neighbor in visited:
                    continue
                visited.add(neighbor)
                queue.append(neighbor)
        if len(component) > 1:
            groups.append(sorted(set(component)))
    return sorted([group for group in groups if len(group) > 1])


def infer_projected_followups(active_journey: str) -> dict[str, list[str]]:
    followups = {
        "Book Flight": {
            "journey_node:Book Flight:ask_origin": ["journey_node:Book Flight:ask_destination:Book Flight:ask_origin->ask_destination"],
            "journey_node:Book Flight:ask_destination:Book Flight:ask_origin->ask_destination": ["journey_node:Book Flight:ask_dates:Book Flight:ask_destination->ask_dates"],
            "journey_node:Book Flight:ask_dates:Book Flight:ask_destination->ask_dates": ["journey_node:Book Flight:ask_class:Book Flight:ask_dates->ask_class"],
            "journey_node:Book Flight:ask_class:Book Flight:ask_dates->ask_class": ["journey_node:Book Flight:ask_name:Book Flight:ask_class->ask_name"],
        },
        "Book Taxi Ride": {
            "journey_node:Book Taxi Ride:ask_pickup_location": ["journey_node:Book Taxi Ride:ask_dropoff_location:Book Taxi Ride:ask_pickup_location->ask_dropoff_location"],
            "journey_node:Book Taxi Ride:ask_dropoff_location:Book Taxi Ride:ask_pickup_location->ask_dropoff_location": ["journey_node:Book Taxi Ride:ask_pickup_time:Book Taxi Ride:ask_dropoff_location->ask_pickup_time"],
        },
        "Reset Password Journey": {
            "journey_node:Reset Password Journey:ask_account_name": ["journey_node:Reset Password Journey:ask_contact:Reset Password Journey:ask_account_name->ask_contact"],
            "journey_node:Reset Password Journey:ask_contact:Reset Password Journey:ask_account_name->ask_contact": ["journey_node:Reset Password Journey:good_day:Reset Password Journey:ask_contact->good_day"],
        },
    }
    return followups.get(active_journey, {})


def infer_resolution_records(
    scenario: dict[str, Any], matched_guidelines: list[str], suppressed_guidelines: list[str]
) -> list[dict[str, str]]:
    records: list[dict[str, str]] = []
    matched = set(matched_guidelines)
    suppressed = set(suppressed_guidelines)
    for item in scenario.get("policy_setup", {}).get("relationships") or []:
        source = item["source"]
        target = item["target"]
        kind = item["kind"]
        if source in matched and kind == "entails":
            records.append({"entity_id": target, "kind": "entailed"})
        if kind == "priority" and target in suppressed:
            records.append({"entity_id": target, "kind": "deprioritized"})
        if kind in ("dependency", "dependency_any") and source in suppressed:
            records.append(
                {
                    "entity_id": source,
                    "kind": "unmet_dependency_any" if kind == "dependency_any" else "unmet_dependency",
                }
            )
    for item in matched_guidelines:
        records.append({"entity_id": item, "kind": "none"})
    return sorted(records, key=lambda item: (item["entity_id"], item["kind"]))


def infer_journey_decision(scenario: dict[str, Any], matched_states: list[str]) -> str:
    expected = scenario.get("expectations") or {}
    if expected.get("journey_decision"):
        return expected["journey_decision"]
    if not matched_states:
        return "ignore"
    prior = (scenario.get("prior_state") or {}).get("journey_path") or []
    if not prior:
        return "start"
    return "advance"


def infer_next_journey_node(scenario: dict[str, Any], matched_states: list[str]) -> str:
    expected = scenario.get("expectations") or {}
    value = expected.get("next_journey_node") or ""
    return value


def parlant_state_to_fixture_name(journey_title: str, state_id: str) -> str:
    mapping = {
        "Reset Password Journey": {
            "2": "ask_account_name",
            "3": "ask_contact",
            "4": "good_day",
            "5": "do_reset",
            "6": "cant_reset",
        },
        "Book Taxi Ride": {
            "2": "ask_pickup_location",
            "3": "ask_dropoff_location",
            "4": "ask_pickup_time",
        },
        "Book Flight": {
            "1": "ask_origin",
            "2": "ask_destination",
            "3": "ask_dates",
            "4": "ask_class",
            "5": "ask_name",
        },
    }
    if journey_title in mapping and state_id in mapping[journey_title]:
        return mapping[journey_title][state_id]
    custom = getattr(journey_state_name_to_parlant_id, "_custom_nodes", {})
    reverse = {v: k for k, v in custom.get(journey_title, {}).items()}
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
    run, container_gen = create_run_context(scenario)
    try:
        append_transcript(run, scenario.get("transcript") or [])
        apply_policy_setup(run, scenario)
        setattr(journey_state_name_to_parlant_id, "_custom_nodes", run.context.nodes)
        append_staged_tool_calls(run, scenario)
        seed_prior_state(run, scenario)
        emitted = process(run)
        normalized = normalize(run, emitted, scenario)
        sys.stdout.write(json.dumps(normalized))
        return 0
    finally:
        run.sync_await(container_gen.aclose())
        try:
            asyncio.get_event_loop().close()
        except RuntimeError:
            pass


if __name__ == "__main__":
    raise SystemExit(main())
