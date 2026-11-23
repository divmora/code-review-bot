package app

import (
	"encoding/json"
	"testing"

	"github.com/divmora/code-review-bot/internal/review"
	"github.com/divmora/code-review-bot/pkg/vcs"
)

func TestAnchorTo(t *testing.T) {
	st, _ := json.Marshal(review.State{Scores: &review.Scores{Quality: 80, Security: 90, Maintainability: 70}, ScoresSHA: "s1", OpenFindings: map[string]int{"quality": 2}})
	var in review.Input
	anchorTo(&in, &vcs.BotState{State: st, ReviewedSHA: "other"})
	if in.PrevScores == nil || in.PrevScores.Security != 90 || in.PrevScoresSHA != "s1" || in.PrevOpen["quality"] != 2 {
		t.Errorf("from the state: %+v", in)
	}
	// An older summary keeps no scores in its state: read its table.
	old := "## Scores\n\n| Quality | Security | Maintainability |\n|:---:|:---:|:---:|\n| 🟢 **82** | 🟢 **95** | 🟠 **71** |\n\n"
	in = review.Input{}
	anchorTo(&in, &vcs.BotState{SummaryBody: old, ReviewedSHA: "r1", State: []byte(`{"file_summaries":{}}`)})
	if in.PrevScores == nil || *in.PrevScores != (review.Scores{Quality: 82, Security: 95, Maintainability: 71}) || in.PrevScoresSHA != "r1" || in.PrevOpen != nil {
		t.Errorf("from an older summary: %+v %+v", in, in.PrevScores)
	}
	in = review.Input{}
	anchorTo(&in, nil)
	anchorTo(&in, &vcs.BotState{})
	if in.PrevScores != nil {
		t.Error("no earlier review: from scratch")
	}
}
