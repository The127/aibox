package hvservice

// DialPipe and PipeSDDLFor are dial and pipeSDDL, for tests on a pipe of
// their own, owned by whoever runs them.
var (
	DialPipe    = dial
	PipeSDDLFor = pipeSDDL
)
