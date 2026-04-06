package policyruntime

type stageShot struct {
	Input  string
	Output string
}

var observationShots = []stageShot{
	{
		Input:  `Latest message: "Can I upload a photo for the damaged package?" Condition: "damaged order".`,
		Output: `applies=true because the customer is still on a follow-up detail of the same damage issue.`,
	},
	{
		Input:  `Latest message: "Also, update my email address." Condition: "refund request".`,
		Output: `applies=false because the conversation has shifted to an unrelated topic.`,
	},
}

var actionableShots = []stageShot{
	{
		Input:  `Latest message: "When will the refund arrive?" Guideline: when="refund eligibility" then="Explain the refund process".`,
		Output: `applies=true because refund timing is still part of the same unresolved refund topic.`,
	},
	{
		Input:  `Latest message: "Actually I need to change my shipping address." Guideline: when="return order" then="Help with the return".`,
		Output: `applies=false because the customer switched to a different topic.`,
	},
	{
		Input:  `Latest message: "It's 199877". Tool facts: "tool local:get_user_age result data 16 age 16". Guideline: when="drink under 21" then="Suggest a sweet non-alcoholic drink".`,
		Output: `applies=true because staged tool facts are authoritative conversation context and establish that the customer is under 21.`,
	},
}

var lowCriticalityShots = []stageShot{
	{
		Input:  `Latest message: "I still need help with the damaged item." Guideline: "Offer a concise follow-up suggestion".`,
		Output: `applies=true because it still supports the same unresolved request.`,
	},
	{
		Input:  `Latest message: "Why was I charged twice?" Guideline: "Suggest premium upgrade".`,
		Output: `applies=false because the customer is focused on fixing a problem.`,
	},
}

var disambiguationShots = []stageShot{
	{
		Input:  `Matched guidelines imply "cancel the return" and "continue the return", and the user said "do that".`,
		Output: `is_ambiguous=true and ask a concise clarification question.`,
	},
}

var journeyProgressShots = []stageShot{
	{
		Input:  `Current state has next steps refund_path and damage_path. Latest message mentions a damaged item.`,
		Output: `action=advance and next_state=damage_path because that follow-up best matches the latest turn.`,
	},
}

var journeyBacktrackShots = []stageShot{
	{
		Input:  `Latest message: "actually change the quantity".`,
		Output: `requires_backtracking=true and backtrack_to_same_journey_process=true.`,
	},
	{
		Input:  `Latest message: "I want a different item instead".`,
		Output: `requires_backtracking=true and backtrack_to_same_journey_process=false.`,
	},
}

var responseAnalysisShots = []stageShot{
	{
		Input:  `Previous assistant already asked for the return reason; current matched guideline says to ask for the return reason.`,
		Output: `already_satisfied=true and requires_response=false.`,
	},
	{
		Input:  `Strict mode is active and an approved template is available.`,
		Output: `needs_strict_mode=true and recommend the approved template verbatim.`,
	},
}
