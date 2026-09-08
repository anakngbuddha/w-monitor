package agent_test

import (
	"os"
	"testing"

	"Zeus/internal/testisolate"
)

func TestMain(m *testing.M) {
	os.Exit(testisolate.Run(m))
}
