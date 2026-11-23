package translate

// RequestOptions describes upstream capabilities known by the gateway operator.
// An empty effort list leaves validation to the upstream provider.
type RequestOptions struct {
	Model            string
	ReasoningEfforts []string
}

type PreparedRequest struct {
	Body          []byte
	ToolNames     map[string]string // upstream function name -> original client name
	StopSequences []string
	Warnings      []string
	OmitThinking  bool
}
