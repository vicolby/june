package check_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vicolby/june/internal/check"
	"github.com/vicolby/june/internal/diag"
)

func TestCoverageNotesAcrossFormats(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		changed, symlink, sparse bool
		want                     string
	}{
		{"empty", false, false, false, "checked 0 files, 0 changed lines" + noPython},
		{"ordinary", true, false, false, "checked 1 file, 1 changed line"},
		{"symlink-only", false, true, false, "checked 0 files, 0 changed lines; 1 changed .py path skipped, not regular files (symlink or submodule)" + noPython},
		{"symlink", true, true, false, "checked 1 file, 1 changed line; 1 changed .py path skipped, not regular files (symlink or submodule)"},
		{"sparse-only", false, false, true, "checked 0 files, 0 changed lines; 1 changed .py path skipped, absent from sparse checkout" + noPython},
		{"sparse", true, false, true, "checked 1 file, 1 changed line; 1 changed .py path skipped, absent from sparse checkout"},
		{"both", true, true, true, "checked 1 file, 1 changed line; 1 changed .py path skipped, not regular files (symlink or submodule); 1 changed .py path skipped, absent from sparse checkout"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := repo(t, map[string]string{"app.py": "x = 1\n", "keep/lib.py": "y = 1\n"})
			if tc.changed {
				write(t, filepath.Join(env.Dir, "keep", "lib.py"), "y = 2\n")
			}
			if tc.sparse {
				write(t, filepath.Join(env.Dir, "app.py"), "x = 2\n")
				commit(t, env)
				git(t, env.Dir, "sparse-checkout", "init", "--no-cone")
				git(t, env.Dir, "sparse-checkout", "set", "--no-cone", "/.june/", "/keep/")
			}
			if tc.symlink {
				if err := os.Symlink("keep/lib.py", filepath.Join(env.Dir, "alias.py")); err != nil {
					t.Fatal(err)
				}
			}
			for _, format := range []diag.Format{diag.Text, diag.GitHub, diag.GitLab, diag.JSON, diag.SARIF} {
				t.Run(string(format), func(t *testing.T) {
					res, err := check.Change(context.Background(), env, check.Request{Base: "main", Format: format})
					if err != nil {
						t.Fatal(err)
					}
					if res.Exit != 0 || len(res.Diags) != 0 {
						t.Fatalf("Exit = %d, diagnostics = %+v, want a clean verdict", res.Exit, res.Diags)
					}
					var summary string
					switch format {
					case diag.JSON:
						var doc struct{ Summary string }
						if err := json.Unmarshal(res.Out, &doc); err != nil {
							t.Fatal(err)
						}
						summary = doc.Summary
					case diag.SARIF:
						var doc struct {
							Runs []struct {
								Invocations []struct {
									ExecutionSuccessful        bool
									ToolExecutionNotifications []struct {
										Level   string
										Message struct{ Text string }
									}
								}
								Results []json.RawMessage
							}
						}
						if err := json.Unmarshal(res.Out, &doc); err != nil {
							t.Fatal(err)
						}
						if len(doc.Runs) != 1 || len(doc.Runs[0].Invocations) != 1 || len(doc.Runs[0].Results) != 0 {
							t.Fatalf("invalid clean run: %s", res.Out)
						}
						inv := doc.Runs[0].Invocations[0]
						if !inv.ExecutionSuccessful || len(inv.ToolExecutionNotifications) != 1 || inv.ToolExecutionNotifications[0].Level != "note" {
							t.Fatalf("invalid coverage notification: %s", res.Out)
						}
						summary = inv.ToolExecutionNotifications[0].Message.Text
					default:
						summary = strings.TrimSuffix(string(res.Out), "\n")
					}
					if want := "june: 0 errors, 0 warnings, 0 opt-outs; " + tc.want + "; base main (--base)"; summary != want {
						t.Errorf("summary = %q, want %q", summary, want)
					}
				})
			}
		})
	}
}
