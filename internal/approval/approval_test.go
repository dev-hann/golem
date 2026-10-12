package approval_test

import (
	"testing"

	"github.com/dev-hann/golem/internal/approval"
)

func TestApproval_AutoAllowsWhitelistedPrefixes(t *testing.T) {
	t.Parallel()
	policy := approval.NewPolicy(approval.ModePrompt, approval.DefaultPrefixes)
	tests := []struct {
		name string
		cmd  string
	}{
		{name: "bare ls", cmd: "ls"},
		{name: "ls with flags", cmd: "ls -la /tmp"},
		{name: "git status short", cmd: "git status -s"},
		{name: "git diff cached", cmd: "git diff --cached"},
		{name: "multi-segment all whitelisted", cmd: "cat a.txt && grep x b.txt"},
		{name: "pipeline of whitelisted", cmd: "cat big.log | grep ERROR"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := policy.Check(tt.cmd); got != approval.DecisionAllow {
				t.Errorf("Check(%q) = %v, want allow", tt.cmd, got)
			}
		})
	}
}

func TestApproval_PrefixMatchRequiresWordBoundary(t *testing.T) {
	t.Parallel()
	policy := approval.NewPolicy(approval.ModePrompt, approval.DefaultPrefixes)
	tests := []struct {
		name string
		cmd  string
	}{
		{name: "lsx is not ls", cmd: "lsx -la"},
		{name: "git statusx is not git status", cmd: "git statusx"},
		{name: "prefix word must be complete", cmd: "grepping file"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := policy.Check(tt.cmd); got != approval.DecisionDeny {
				t.Errorf("Check(%q) = %v, want deny", tt.cmd, got)
			}
		})
	}
}

func TestApproval_DeniesNonWhitelistedInNonInteractive(t *testing.T) {
	t.Parallel()
	policy := approval.NewPolicy(approval.ModePrompt, approval.DefaultPrefixes)
	tests := []struct {
		name string
		cmd  string
	}{
		{name: "rm", cmd: "rm -rf /tmp/x"},
		{name: "make", cmd: "make build"},
		{name: "compound with one bad segment", cmd: "ls && rm -rf /"},
		{name: "newline as separator", cmd: "ls\nrm -rf /"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := policy.Check(tt.cmd); got != approval.DecisionDeny {
				t.Errorf("Check(%q) = %v, want deny", tt.cmd, got)
			}
		})
	}
}

func TestApproval_DangerousConstructsAlwaysDeny(t *testing.T) {
	t.Parallel()
	policy := approval.NewPolicy(approval.ModePrompt, approval.DefaultPrefixes)
	tests := []struct {
		name string
		cmd  string
	}{
		{name: "command substitution", cmd: "cat $(echo /etc/passwd)"},
		{name: "backticks", cmd: "cat `echo x`"},
		{name: "output redirection", cmd: "ls > /etc/passwd"},
		{name: "input redirection", cmd: "cat < /etc/passwd"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := policy.Check(tt.cmd); got != approval.DecisionDeny {
				t.Errorf("Check(%q) = %v, want deny", tt.cmd, got)
			}
		})
	}
}

func TestApproval_AllAutoModeAllowsEverything(t *testing.T) {
	t.Parallel()
	policy := approval.NewPolicy(approval.ModeAllAuto, nil)
	if got := policy.Check("rm -rf /"); got != approval.DecisionAllow {
		t.Errorf("all_auto should allow, got %v", got)
	}
}

func TestApproval_ModeReportsConfiguredMode(t *testing.T) {
	t.Parallel()
	policy := approval.NewPolicy(approval.ModePrompt, nil)
	if policy.Mode() != approval.ModePrompt {
		t.Errorf("Mode() = %q, want prompt", policy.Mode())
	}
}
