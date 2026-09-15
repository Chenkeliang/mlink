package model

import "testing"

func TestOnlyExplicitUserCorrectionsArePrioritized(t *testing.T) {
	for _, text := range []string{"纠正：用新接口", "更正旧说法", "不是这个接口，应该用另一个", "但是其实已经建单就应该走 /ops/exec"} {
		if !IsExplicitCorrection(text) {
			t.Errorf("missed correction %q", text)
		}
	}
	for _, text := range []string{"你好", "怎么改地址？", "查一下订单", "这个是不是已经发货了", "哪个上下文纠正的导致这个说法", "这个是不是应该用另外一个接口？", "什么时候更正的？"} {
		if IsExplicitCorrection(text) {
			t.Errorf("ordinary query treated as correction: %q", text)
		}
	}
}
