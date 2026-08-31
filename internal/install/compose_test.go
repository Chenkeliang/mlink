package install

import "testing"

func TestComposeChangeSetsBuildsStableDistinctPlanAndRejectsDuplicateTargets(t *testing.T) {
	left := ChangeSet{PlanID: "plan_left", SelectedConnection: "local", Operations: []Operation{{ID: "op_left", Target: "remote:left"}}}
	right := ChangeSet{PlanID: "plan_right", SelectedConnection: "local", Operations: []Operation{{ID: "op_right", Target: "service:right"}}}
	first, err := ComposeChangeSets(left, right)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ComposeChangeSets(left, right)
	if err != nil || first.PlanID != second.PlanID || first.PlanID == left.PlanID || first.PlanID == right.PlanID || len(first.Operations) != 2 {
		t.Fatalf("composed plans = %#v/%#v, %v", first, second, err)
	}
	right.Operations[0].Target = left.Operations[0].Target
	if _, err := ComposeChangeSets(left, right); err == nil {
		t.Fatal("duplicate target was accepted")
	}
}
