#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

REPORT_DIR="${PLATFORM_VALIDATION_REPORT_DIR:-/tmp/parmesan-platform-validation-live}"
PROVIDER="${DEFAULT_REASONING_PROVIDER:-openrouter}"
EXPECTED_SCENARIOS="ecommerce_knowledge_grounding_damaged_toaster_replacem,ecommerce_knowledge_grounding_refund_timing_question,pet_store_topic_scope_human_cooking_question,pet_store_topic_scope_pet_food_question,support_multilingual_english_fallback,support_multilingual_respond_in_indonesian,support_preference_call_me_rina,support_preference_prefer_email,support_refusal_escalation_operator_handoff,support_refusal_escalation_unsafe_request"
SEED_FILE="${QUALITY_SCENARIO_SEEDS:-artifacts/regression-scenario-seeds.json}"

if [[ "$PROVIDER" == "openrouter" && -z "${OPENROUTER_API_KEY:-}" ]]; then
  echo "OPENROUTER_API_KEY is required for OpenRouter live validation." >&2
  exit 2
fi

if [[ "$PROVIDER" == "openai" && -z "${OPENAI_API_KEY:-}" ]]; then
  echo "OPENAI_API_KEY is required for OpenAI live validation." >&2
  exit 2
fi

export DEFAULT_REASONING_PROVIDER="${DEFAULT_REASONING_PROVIDER:-openrouter}"
export DEFAULT_STRUCTURED_PROVIDER="${DEFAULT_STRUCTURED_PROVIDER:-$DEFAULT_REASONING_PROVIDER}"
export DEFAULT_EMBEDDING_PROVIDER="${DEFAULT_EMBEDDING_PROVIDER:-$DEFAULT_REASONING_PROVIDER}"
export PLATFORM_VALIDATION_REPORT_DIR="$REPORT_DIR"

if [[ -f "$SEED_FILE" ]]; then
  echo "Using reviewed seed file: $SEED_FILE"
  go run ./cmd/quality-seed-check -in "$SEED_FILE"
  export QUALITY_SCENARIO_SEEDS="$SEED_FILE"
fi

mkdir -p "$REPORT_DIR"
rm -f "$REPORT_DIR"/TestPlatformValidation*.json

echo "[1/3] Production-readiness catalog summary"
go run ./cmd/quality-catalog -summary

echo
echo "[2/3] Live-gate scenario catalog"
go run ./cmd/quality-catalog -summary -live-only

echo
echo "[3/3] Live platform validation using $DEFAULT_REASONING_PROVIDER"
go test -count=1 ./internal/api/http -run 'TestPlatformValidation(EcommerceLifecycle|PendingPreferenceReviewFlow|LanguagePreferenceLearning|PetStoreScopeQuality|LiveGateCatalog)$' -v

echo
echo "Quality gate check"
go run ./cmd/quality-report-check -dir "$REPORT_DIR" -expect-scenarios "$EXPECTED_SCENARIOS"

echo
echo "Scorecard summary from $REPORT_DIR"
if command -v jq >/dev/null 2>&1; then
  for report in "$REPORT_DIR"/TestPlatformValidation*.json; do
    [[ -e "$report" ]] || continue
    echo "== $report =="
    jq '{
      test_name,
      live_provider,
      providers: [.provider_stats[]? | select(.name | startswith("openrouter") or startswith("openai")) | {name, capability, healthy, success_count, failure_count}],
      scorecards: [.sessions[]?.scorecards | to_entries[]? | {
        execution_id: .key,
        overall: .value.overall,
        passed: .value.passed,
        hard_failed: .value.hard_failed,
        claim_count: (.value.claims // [] | length),
        evidence_match_count: (.value.evidence_matches // [] | length),
        hard_failures: (.value.hard_failures // [])
      }],
      preferences: [.preferences[]? | {key, value, status}]
    }' "$report"
  done
else
  echo "jq not found; raw reports are available in $REPORT_DIR"
fi
