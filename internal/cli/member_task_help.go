package cli

import "cs-cloud/internal/membertask"

type ArgumentCondition struct {
	Argument string `json:"argument"`
	Equals   string `json:"equals"`
}

type ArgumentSpec struct {
	Name           string             `json:"name"`
	Kind           string             `json:"kind"`
	Flag           string             `json:"flag,omitempty"`
	Type           string             `json:"type"`
	ValueStyle     string             `json:"value_style,omitempty"`
	Required       bool               `json:"required"`
	Enum           []string           `json:"enum,omitempty"`
	RequiredWhen   *ArgumentCondition `json:"required_when,omitempty"`
	RequiredUnless string             `json:"required_unless,omitempty"`
	Summary        string             `json:"summary"`
}

type CommandSpec struct {
	Name              string               `json:"name"`
	Summary           string               `json:"summary"`
	Capability        string               `json:"capability"`
	TimeoutSeconds    int                  `json:"timeout_seconds"`
	Arguments         []ArgumentSpec       `json:"arguments"`
	MutuallyExclusive [][]string           `json:"mutually_exclusive,omitempty"`
	ConfirmationMode  string               `json:"confirmation_mode"`
	Outcomes          []membertask.Outcome `json:"outcomes"`
}

type taskHelpCatalog struct {
	SchemaVersion  string             `json:"schema_version"`
	FailureOutcome membertask.Outcome `json:"failure_outcome"`
	Commands       []CommandSpec      `json:"commands"`
}

func memberTaskHelpCatalog() taskHelpCatalog {
	key := ArgumentSpec{Name: "task_key", Kind: "positional", Type: "string", Required: true, Summary: "Stable cloud/workspace/node/role task identity"}
	confirm := ArgumentSpec{Name: "confirm", Kind: "flag", Flag: "--confirm", Type: "string", ValueStyle: "equals_or_unprefixed_separate", Summary: "Previously issued preview id"}
	workDir := ArgumentSpec{Name: "workdir", Kind: "flag", Flag: "--workdir", Type: "string", ValueStyle: "equals_or_unprefixed_separate", Summary: "Existing working directory that will contain .cs-cloud-tasks"}
	deliverable := ArgumentSpec{Name: "deliverable", Kind: "flag", Flag: "--deliverable", Type: "string", ValueStyle: "repeatable_pair", Summary: "Deliverable id paired with the following --file"}
	file := ArgumentSpec{Name: "file", Kind: "flag", Flag: "--file", Type: "string", ValueStyle: "repeatable_pair", Summary: "Prepared task file paired with the preceding --deliverable"}
	catalog := taskHelpCatalog{SchemaVersion: membertask.SchemaVersion, FailureOutcome: membertask.OutcomeNotCompleted, Commands: []CommandSpec{
		{Name: "help", Summary: "Describe the task command contract", Capability: "observe", TimeoutSeconds: 5, Arguments: []ArgumentSpec{}, ConfirmationMode: "none", Outcomes: []membertask.Outcome{membertask.OutcomeObserved}},
		{Name: "list", Summary: "List assigned and local task history", Capability: "observe", TimeoutSeconds: 20, Arguments: []ArgumentSpec{}, ConfirmationMode: "none", Outcomes: []membertask.Outcome{membertask.OutcomeObserved}},
		{Name: "get", Summary: "Get task context and local state", Capability: "observe", TimeoutSeconds: 20, Arguments: []ArgumentSpec{key}, ConfirmationMode: "none", Outcomes: []membertask.Outcome{membertask.OutcomeObserved}},
		{Name: "handle", Summary: "Accept a task and prepare its workspace", Capability: "local_write", TimeoutSeconds: 300, Arguments: []ArgumentSpec{key, workDir}, ConfirmationMode: "none", Outcomes: []membertask.Outcome{membertask.OutcomeCompleted, membertask.OutcomeAlreadyCompleted, membertask.OutcomeRecovered}},
		{Name: "submit", Summary: "Preview or confirm a worker submission", Capability: "remote_write", TimeoutSeconds: 300, Arguments: []ArgumentSpec{key, deliverable, file, confirm}, MutuallyExclusive: [][]string{{"confirm", "deliverable"}, {"confirm", "file"}}, ConfirmationMode: "preview_then_confirm", Outcomes: []membertask.Outcome{membertask.OutcomePreviewed, membertask.OutcomeCompleted, membertask.OutcomeAlreadyCompleted, membertask.OutcomeRecovered}},
		{Name: "review", Summary: "Preview or confirm a critic decision", Capability: "remote_write", TimeoutSeconds: 180, Arguments: []ArgumentSpec{key,
			{Name: "decision", Kind: "flag", Flag: "--decision", Type: "enum", ValueStyle: "equals_or_unprefixed_separate", Enum: []string{"approve", "reject"}, RequiredUnless: "confirm", Summary: "Critic decision"},
			{Name: "reason", Kind: "flag", Flag: "--reason", Type: "string", ValueStyle: "equals_or_unprefixed_separate", RequiredWhen: &ArgumentCondition{Argument: "decision", Equals: "reject"}, Summary: "Required for reject"},
			confirm,
		}, MutuallyExclusive: [][]string{{"confirm", "decision"}, {"confirm", "reason"}}, ConfirmationMode: "preview_then_confirm", Outcomes: []membertask.Outcome{membertask.OutcomePreviewed, membertask.OutcomeCompleted, membertask.OutcomeAlreadyCompleted, membertask.OutcomeRecovered}},
		{Name: "delete", Summary: "Preview or confirm local task deletion", Capability: "local_delete", TimeoutSeconds: 60, Arguments: []ArgumentSpec{key,
			{Name: "force_discard", Kind: "flag", Flag: "--force-discard", Type: "boolean", Summary: "Preview explicit risky discard"},
			confirm,
		}, MutuallyExclusive: [][]string{{"confirm", "force_discard"}}, ConfirmationMode: "preview_then_confirm", Outcomes: []membertask.Outcome{membertask.OutcomePreviewed, membertask.OutcomeCompleted, membertask.OutcomeRecovered}},
		{Name: "recover", Summary: "Recover an interrupted accepted operation", Capability: "remote_write", TimeoutSeconds: 300, Arguments: []ArgumentSpec{key}, ConfirmationMode: "none", Outcomes: []membertask.Outcome{membertask.OutcomeCompleted, membertask.OutcomeRecovered, membertask.OutcomeAlreadyCompleted}},
	}}
	for index := range catalog.Commands {
		catalog.Commands[index].Outcomes = append(catalog.Commands[index].Outcomes, membertask.OutcomeNotCompleted)
	}
	return catalog
}

func taskCommandTimeout(command string) int {
	for _, spec := range memberTaskHelpCatalog().Commands {
		if spec.Name == command {
			return spec.TimeoutSeconds
		}
	}
	return 15
}
