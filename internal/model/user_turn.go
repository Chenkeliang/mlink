package model

import "strings"

// UserTurn is observed before model work. Previous messages are historical data,
// not instructions. Only Message is the newly submitted user input.
type UserTurn struct {
	Identity   IdentityScope `json:"identity"`
	Message    Message       `json:"message"`
	Previous   []Message     `json:"previous,omitempty"`
	Correction bool          `json:"correction"`
	Ended      bool          `json:"ended,omitempty"`
}

type ObservationReceipt struct {
	Paused           bool     `json:"paused"`
	CorrectionStatus string   `json:"correction_status,omitempty"`
	TaskID           string   `json:"task_id,omitempty"`
	RelatedSkillRefs []string `json:"related_skill_refs,omitempty"`
	Error            string   `json:"error,omitempty"`
}

func IsExplicitCorrection(text string) bool {
	s := strings.ToLower(strings.TrimSpace(text))
	if strings.HasPrefix(s, "correction:") {
		return true
	}
	for _, marker := range []string{"纠正：", "纠正:", "更正：", "更正:", "纠正一下", "更正一下", "需要更正", "需要纠正", "请纠正", "请更正"} {
		if strings.Contains(s, marker) {
			return true
		}
	}
	for _, question := range []string{"是不是", "是否", "为什么", "什么时候", "哪个上下文"} {
		if strings.Contains(s, question) {
			return false
		}
	}
	if strings.HasPrefix(s, "更正") || strings.HasPrefix(s, "纠正") {
		return true
	}
	return (strings.Contains(s, "不是") || strings.Contains(s, "但是其实") || strings.Contains(s, "不对")) &&
		(strings.Contains(s, "应该") || strings.Contains(s, "应当") || strings.Contains(s, "改为"))
}
