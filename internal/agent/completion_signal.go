package agent

// CompletionSignal carries the payload an agent sends when it explicitly
// declares a workflow task finished via the "complete task" tool, instead of
// the driver inferring completion from the session going idle.
type CompletionSignal struct {
	Action   string // "complete" (worker) | "review" (critic)
	Summary  string // worker's final summary; becomes the task output
	Decision string // critic decision: "approve" | "reject" (review only)
	Reason   string // critic reason (review only)
}
