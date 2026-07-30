package nologsecrets_test

import (
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"

	"callmemaybe/tools/nologsecrets"
)

func TestAnalyzer(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), nologsecrets.Analyzer, "lobby")
}
