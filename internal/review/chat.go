package review

import (
	"context"
	"fmt"
	"strings"

	"github.com/divmora/code-review-bot/internal/config"
	"github.com/divmora/code-review-bot/internal/llm"
	"github.com/divmora/code-review-bot/pkg/diff"
	"github.com/divmora/code-review-bot/pkg/vcs"
)

// Answer replies to a question about a PR using its diff as context.
func (e *Engine) Answer(ctx context.Context, pr *vcs.PR, files []diff.File, rc config.RepoConfig, question string) (string, error) {
	budget := e.Limits.BatchTokens * 4 // chars
	var b strings.Builder
	b.WriteString(prContext(pr))
	b.WriteString("\n<diff>\n")
	used := b.Len()
	omitted := 0
	for i := range files {
		f := &files[i]
		if skipReason(f, &rc) != "" {
			continue
		}
		block := fmt.Sprintf("--- %s (%s)\n%s\n", f.Path, f.Status, f.Annotated())
		if used+len(block) > budget {
			omitted++
			continue
		}
		b.WriteString(block)
		used += len(block)
	}
	b.WriteString("</diff>\n")
	if omitted > 0 {
		fmt.Fprintf(&b, "(%d more files omitted for size)\n", omitted)
	}
	fmt.Fprintf(&b, "\n<question>\n%s\n</question>", question)
	system := chatSystem
	if rc.Language != "" && !strings.EqualFold(rc.Language, "english") {
		system += "\nAnswer in " + rc.Language + "."
	}
	resp, err := e.LLM.Complete(ctx, llm.Request{
		System:    system,
		Messages:  []llm.Message{{Role: llm.User, Content: b.String()}},
		MaxTokens: 4096,
	})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(resp.Text), nil
}
