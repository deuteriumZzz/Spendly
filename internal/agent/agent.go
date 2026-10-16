// Package agent turns a user's free-text message into expense operations via
// LLM tool calling.
package agent

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"spendly/internal/expense"
)

type Agent struct {
	llm      LLM
	loc      *time.Location
	now      func() time.Time
	maxSteps int
}

func New(llm LLM, loc *time.Location) *Agent {
	return &Agent{llm: llm, loc: loc, now: time.Now, maxSteps: 6}
}

const fallbackReply = "Не получилось разобрать запрос. Попробуй сформулировать иначе, например: «кофе 350» или «сколько потратил за неделю»."

// Reply answers one message. store must be scoped to its author (db.ForUser):
// it is the agent's only way to reach data, and no tool takes a user id.
//
// If it fails after changing data, it returns both an error and a reply listing
// the changes: telling the user only «не получилось» would make them repeat
// the message and duplicate the expense.
func (a *Agent) Reply(ctx context.Context, store *expense.Store, history []Message, text string) (string, error) {
	now := a.now().In(a.loc)
	today := now.Format(time.DateOnly)

	msgs := []Message{{Role: "system", Content: systemPrompt(now)}}
	for _, h := range history {
		msgs = append(msgs, Message{Role: h.Role, Content: h.Content})
	}
	msgs = append(msgs, Message{Role: "user", Content: text})

	var changes []string
	for range a.maxSteps {
		resp, err := a.llm.Chat(ctx, msgs, tools)
		if err != nil {
			return partialReply(changes), err
		}
		if len(resp.ToolCalls) == 0 {
			if strings.TrimSpace(resp.Content) == "" {
				return fallbackReply, nil
			}
			return resp.Content, nil
		}
		msgs = append(msgs, Message{Role: "assistant", Content: resp.Content, ToolCalls: resp.ToolCalls})
		for _, call := range resp.ToolCalls {
			out, change, err := runTool(ctx, store, today, call)
			if err != nil {
				return partialReply(changes), err
			}
			if change != "" {
				changes = append(changes, change)
			}
			slog.Debug("tool", "name", call.Function.Name, "args", call.Function.Arguments, "result", out)
			msgs = append(msgs, Message{Role: "tool", ToolCallID: call.ID, Content: out})
		}
	}
	slog.Warn("agent step limit reached", "steps", a.maxSteps)
	if len(changes) > 0 {
		return partialReply(changes), nil
	}
	return fallbackReply, nil
}

func partialReply(changes []string) string {
	if len(changes) == 0 {
		return ""
	}
	return "Сделано:\n• " + strings.Join(changes, "\n• ") +
		"\nОтвет не удалось сформировать до конца — проверь траты, прежде чем повторять сообщение."
}

var weekdays = [...]string{"воскресенье", "понедельник", "вторник", "среда", "четверг", "пятница", "суббота"}

func systemPrompt(now time.Time) string {
	return fmt.Sprintf(promptTemplate, now.Format(time.DateOnly), weekdays[now.Weekday()], now.Location())
}

const promptTemplate = `Ты — бот учёта семейных расходов в Telegram. Пользователь пишет обычным текстом, ты ведёшь его траты через инструменты.

Сегодня %s, %s. Часовой пояс %s.

Категории: еда, транспорт, жильё, развлечения, здоровье, покупки, другое.

Как работать:
- «кофе 350» → add_expense: amount 350, категория по смыслу, comment — что купили одним-двумя словами («кофе»), spent_at — сегодня, если дата не названа. Если категорию не определить, ставь «другое».
- Несколько трат в одном сообщении («метро 70 и кофе 200») — отдельный add_expense на каждую.
- Если сумма не названа, спроси её. Не придумывай суммы и даты.
- Относительные даты считай от сегодняшней: «вчера», «позавчера», «в субботу» (ближайшая прошедшая). «За неделю» — последние 7 дней включая сегодня, «на этой неделе» — с понедельника, «в этом месяце» — с 1-го числа, «в прошлом месяце» — весь прошлый календарный месяц.
- Суммы вроде «1,5к», «полторы тысячи», «350р», «1.200» переводи в число рублей.
- Не складывай числа сам: на «сколько потратил» вызывай sum_expenses. В его ответе есть разбивка by_category — по ней отвечай на «на что больше всего».
- Чтобы изменить или удалить трату, сначала найди её через find_expenses и возьми id из результата. «Последняя» трата — последняя добавленная: order=created_at, limit=1. Если по тексту ничего не нашлось, поищи по категории или за период.
- Если найденная трата не совпадает с описанием (пользователь пишет «было не 500», а последняя трата — 350 ₽), ничего не меняй и переспроси.
- Если под описание подходит несколько трат и неясно, какую менять или удалять, перечисли их (сумма, дата, комментарий) и спроси. Не угадывай. Когда пользователь ответит, найди траты снова и выбери нужную по сумме и дате.
- Не удаляй и не меняй несколько трат сразу без явного подтверждения: сначала перечисли их и спроси.
- Траты в будущем не записывай. Если дата старше года — переспроси.
- В списке показывай не больше 20 трат. Если total_count больше показанного, допиши «и ещё N на X ₽».
- После любого изменения коротко скажи, что сделано: «Записано: кофе — 350 ₽, еда, сегодня».
- Если сообщение не о расходах, коротко объясни, что умеешь, и дай 2–3 примера фраз.
- Ты видишь только траты этого пользователя. Чужие траты недоступны — так и скажи, если о них просят.

Формат ответа: по-русски, коротко, обычным текстом без Markdown. Суммы — «1 200 ₽», даты — «15.10».`
