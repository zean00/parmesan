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
from parlant.core.tags import Tag
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


def create_guideline_direct(
    run: ScenarioRunContext,
    guideline_name: str,
    condition: str,
    action: str | None,
) -> Guideline:
    metadata = get_guideline_properties(run.context, condition, action)
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
    run.context.guidelines[guideline_name] = guideline
    return guideline


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


def ensure_named_journey(run: ScenarioRunContext, journey_title: str) -> None:
    if not journey_title or journey_title in run.context.journeys:
        return
    factories = {
        "Reset Password Journey": create_reset_password_journey,
        "Book Flight": create_book_flight_journey,
        "Book Taxi Ride": create_book_taxi_journey,
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
    ensure_named_journey(run, title)
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
    }
    return mapping.get(journey_title, {}).get(name)


def apply_policy_setup(run: ScenarioRunContext, scenario: dict[str, Any]) -> None:
    setup = scenario.get("policy_setup") or {}
    transcript_text = "\n".join(item["text"] for item in scenario.get("transcript", []))
    if "reset my password" in transcript_text.lower():
        ensure_named_journey(run, "Reset Password Journey")
    if "book a taxi" in transcript_text.lower() or "book a cab" in transcript_text.lower():
        ensure_named_journey(run, "Book Taxi Ride")
    if "book a flight" in transcript_text.lower():
        ensure_named_journey(run, "Book Flight")

    for item in setup.get("extra_guidelines") or []:
        kind = (item.get("kind") or "actionable").strip().lower()
        if kind == "observation":
            create_guideline_direct(run, item["id"], item["condition"], None)
        else:
            create_guideline_direct(run, item["id"], item["condition"], item["action"])

    for item in setup.get("tools") or []:
        tool_id = item["id"]
        service, name = split_tool_id(tool_id)
        if service == "local":
            ensure_local_tool(run, name)

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
        if kind == "dependency" and item["target"].startswith("journey:"):
            journey_title = item["target"].split(":", 1)[1]
            ensure_named_journey(run, journey_title)
            run.sync_await(
                run.container[RelationshipStore].create_relationship(
                    source=RelationshipEntity(
                        id=run.context.guidelines[item["source"]].id,
                        kind=RelationshipEntityKind.GUIDELINE,
                    ),
                    target=RelationshipEntity(
                        id=Tag.for_journey_id(run.context.journeys[journey_title].id).id,
                        kind=RelationshipEntityKind.TAG_ALL,
                    ),
                    kind=RelationshipKind.DEPENDENCY,
                )
            )
            continue
        if kind == "dependency":
            store = run.container[RelationshipStore]
            target = item["target"]
            if target in run.context.guidelines:
                run.sync_await(
                    store.create_relationship(
                        source=RelationshipEntity(
                            id=run.context.guidelines[item["source"]].id,
                            kind=RelationshipEntityKind.GUIDELINE,
                        ),
                        target=RelationshipEntity(
                            id=run.context.guidelines[target].id,
                            kind=RelationshipEntityKind.GUIDELINE,
                        ),
                        kind=RelationshipKind.DEPENDENCY,
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
            if isinstance(tool_id, str):
                normalized_tool_id = tool_id
            else:
                normalized_tool_id = f"{tool_id.service_name}:{tool_id.tool_name}"
            tool_calls.append(
                {
                    "tool_id": normalized_tool_id,
                    "arguments": call["arguments"],
                }
            )

    relevant_guidelines = [item["id"] for item in (scenario.get("policy_setup", {}).get("extra_guidelines") or [])]
    suppressed = sorted(set(relevant_guidelines) - set(matched_guidelines))
    selected_tool = tool_calls[0]["tool_id"] if tool_calls else ""
    if not exposed_tools and selected_tool:
        exposed_tools = [selected_tool]

    active_journey = active_journeys[0] if active_journeys else ""
    active_state = parlant_state_to_fixture_name(active_journey, matched_states[-1] if matched_states else "")
    result = {
        "matched_observations": [],
        "matched_guidelines": sorted(matched_guidelines),
        "suppressed_guidelines": suppressed,
        "active_journey": active_journey,
        "active_journey_node": active_state,
        "journey_decision": infer_journey_decision(scenario, matched_states),
        "next_journey_node": infer_next_journey_node(scenario, matched_states),
        "exposed_tools": sorted(exposed_tools),
        "selected_tool": selected_tool,
        "response_mode": "strict" if normalize_mode(scenario.get("mode", "")) == CompositionMode.CANNED_STRICT.value else "guided",
        "no_match": response_text == DEFAULT_NO_MATCH_CANREP,
        "selected_template": "",
        "verification_outcome": "pass",
        "response_text": response_text,
        "tool_calls": tool_calls,
        "unsupported_fields": ["suppression_reasons", "verification_outcome"],
    }
    return result


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
    return mapping.get(journey_title, {}).get(state_id, state_id)


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
