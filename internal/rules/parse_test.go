package rules

import "testing"

const multilineOutput = `Running 2 tests

----------- Case Details -----------
[Noisy] Expect ZZZ to report no issue, but some issues found in:

  x = [
  1,
  	2]

[Missing] Expect rule ZZZ to report issues, but none found in:

  y = (
  1,)

PASS AAA  ..
FAIL ZZZ  NM

Error: test failed. 1 passed; 1 failed;
`

func TestMisbehavingPreservesExampleWhitespace(t *testing.T) {
	got := misbehaving(multilineOutput)
	want := []misbehavior{
		{rule: "ZZZ", kind: "valid", code: "x = [\n1,\n\t2]", problem: "is reported by the rule"},
		{rule: "ZZZ", kind: "invalid", code: "y = (\n1,)", problem: "is not reported by the rule"},
	}
	if len(got) != len(want) {
		t.Fatalf("misbehaving = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("example %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestCaseDetailsKeepsUnindentedLines(t *testing.T) {
	const text = "[Noisy] Expect ZZZ to report no issue, but some issues found in:\n\n  x = [\n1,\n2]\n\nFAIL ZZZ  N\n"
	if got, want := caseDetails(text), "[Noisy] Expect ZZZ to report no issue, but some issues found in: x = [ 1, 2]"; got != want {
		t.Errorf("caseDetails = %q, want %q", got, want)
	}
}
