package ssm

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/jmurray2011/testease/internal/target/fakeexec"
)

// The aws CLI is faked by the test binary itself, through fakeexec, so the
// same fakes run on every platform.
func TestMain(m *testing.M) {
	fakeexec.Main(m, map[string]fakeexec.Behaviour{
		"aws-transfer": fakeAWSTransfer,
	})
}

// fakeAWSTransfer logs every command to the calls file, reports a bucket in
// us-east-1 as get-bucket-location does - no constraint - and presigns a URL.
func fakeAWSTransfer(c fakeexec.Call) int {
	f, err := os.OpenFile(c.Params["calls"], os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return 1
	}
	_, _ = fmt.Fprintln(f, strings.Join(c.Args, " "))
	_ = f.Close()
	switch joined := c.Joined(); {
	case strings.Contains(joined, "get-bucket-location"):
		_, _ = fmt.Fprintln(c.Stdout, "None")
	case strings.Contains(joined, "presign"):
		_, _ = fmt.Fprintln(c.Stdout, "https://lab-media.s3.amazonaws.com/x?sig")
	}
	return 0
}
