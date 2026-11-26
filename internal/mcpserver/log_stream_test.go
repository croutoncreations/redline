package mcpserver

import "testing"

func TestRunLogStreamsIncludeUsageLimitHookOutput(t *testing.T) {
	for _, stream := range []string{"stdout", "finalize_stderr", "usage_limit_stdout", "usage_limit_stderr"} {
		if !validLogStream(stream) {
			t.Errorf("%s was rejected", stream)
		}
	}
	if validLogStream("usage_limit") {
		t.Error("an unknown stream was accepted")
	}
}
