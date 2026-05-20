update policy_bundles
set
  source_yaml = replace(
    source_yaml,
    'then: offer to involve a human operator and summarize the issue for handover.',
    'then: Tell the customer that a human operator can review the conversation, and ask whether they want you to hand it over.'
  ),
  bundle_json = jsonb_set(
    bundle_json,
    '{guidelines}',
    (
      select jsonb_agg(
        case
          when guideline->>'id' = 'handover_when_stuck' then
            jsonb_set(
              guideline,
              '{then}',
              to_jsonb('Tell the customer that a human operator can review the conversation, and ask whether they want you to hand it over.'::text)
            )
          else guideline
        end
      )
      from jsonb_array_elements(bundle_json->'guidelines') as guideline
    )
  )
where source_yaml like '%then: offer to involve a human operator and summarize the issue for handover.%'
   or bundle_json @? '$.guidelines[*] ? (@.id == "handover_when_stuck" && @.then == "offer to involve a human operator and summarize the issue for handover.")';
