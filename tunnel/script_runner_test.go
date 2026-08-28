package tunnel_test

import (
	"context"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/barahn/remotekit/bark"
	"github.com/barahn/remotekit/tunnel"
)

func TestScriptRunner_EchoExecution(t *testing.T) {
	runner := tunnel.NewScriptRunner()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	script := "echo 'BARAHN_SCRIPT_TEST_OK'"
	interpreter := "sh"
	if runtime.GOOS == "windows" {
		script = "Write-Output 'BARAHN_SCRIPT_TEST_OK'"
		interpreter = "powershell"
	}

	req := bark.ScriptExecutionRequest{
		ExecutionID:    "test-exec-1",
		Interpreter:    interpreter,
		ScriptBody:     script,
		TimeoutSeconds: 5,
	}

	var outputLines []string
	res := runner.Execute(ctx, req, func(chunk bark.ScriptExecutionChunk) {
		outputLines = append(outputLines, chunk.Data)
	})

	if res.ExitCode != 0 {
		t.Fatalf("Expected exit code 0, got %d (err: %s)", res.ExitCode, res.Error)
	}

	joined := strings.Join(outputLines, " ")
	if !strings.Contains(joined, "BARAHN_SCRIPT_TEST_OK") {
		t.Fatalf("Expected output to contain BARAHN_SCRIPT_TEST_OK, got: %s", joined)
	}
}
