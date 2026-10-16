package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"spendly/internal/expense"
)

// No tool takes a user id: the store passed to the agent is already scoped to
// the author of the message.
var tools = func() []Tool {
	cats, _ := json.Marshal(expense.Categories)
	def := func(name, desc, params string) Tool {
		params = strings.ReplaceAll(params, "CATEGORIES", string(cats))
		return Tool{Type: "function", Function: FunctionDef{Name: name, Description: desc, Parameters: json.RawMessage(params)}}
	}
	const period = `
		"from": {"type": "string", "description": "Начало периода YYYY-MM-DD включительно"},
		"to": {"type": "string", "description": "Конец периода YYYY-MM-DD включительно"},
		"category": {"type": "string", "enum": CATEGORIES},
		"text": {"type": "string", "description": "Подстрока комментария, например «такси»"}`
	return []Tool{
		def("add_expense", "Записать новую трату пользователя.", `{
			"type": "object",
			"properties": {
				"amount": {"type": "number", "description": "Сумма в рублях, больше нуля"},
				"category": {"type": "string", "enum": CATEGORIES},
				"spent_at": {"type": "string", "description": "Дата траты YYYY-MM-DD, не позже сегодняшней"},
				"comment": {"type": "string", "description": "Что купили, одним-двумя словами: «кофе», «такси»"}
			},
			"required": ["amount", "category", "spent_at", "comment"]
		}`),
		def("find_expenses", "Найти траты пользователя. Вызывай перед изменением или удалением, чтобы узнать id.", `{
			"type": "object",
			"properties": {`+period+`,
				"order": {"type": "string", "enum": ["spent_at", "created_at"], "description": "created_at — сначала последние добавленные"},
				"limit": {"type": "integer", "description": "Сколько вернуть, по умолчанию 20, максимум 50"}
			}
		}`),
		def("sum_expenses", "Посчитать сумму трат пользователя за период и/или по категории. Вызывай на вопросы «сколько потратил».", `{
			"type": "object",
			"properties": {`+period+`}
		}`),
		def("update_expense", "Изменить трату по id. Передавай только меняемые поля.", `{
			"type": "object",
			"properties": {
				"id": {"type": "integer"},
				"amount": {"type": "number"},
				"category": {"type": "string", "enum": CATEGORIES},
				"spent_at": {"type": "string", "description": "YYYY-MM-DD"},
				"comment": {"type": "string"}
			},
			"required": ["id"]
		}`),
		def("delete_expense", "Удалить трату по id.", `{
			"type": "object",
			"properties": {"id": {"type": "integer"}},
			"required": ["id"]
		}`),
	}
}()

type filterArgs struct {
	From     string           `json:"from"`
	To       string           `json:"to"`
	Category expense.Category `json:"category"`
	Text     string           `json:"text"`
	Order    string           `json:"order"`
	Limit    int              `json:"limit"`
}

func (a filterArgs) filter() (expense.Filter, error) {
	limit := a.Limit
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	f := expense.Filter{From: a.From, To: a.To, Category: a.Category, Text: a.Text, OrderBy: a.Order, Limit: limit}
	return f, f.Validate()
}

type updateArgs struct {
	ID       int64             `json:"id"`
	Amount   *expense.Money    `json:"amount"`
	Category *expense.Category `json:"category"`
	SpentAt  *string           `json:"spent_at"`
	Comment  *string           `json:"comment"`
}

// listResult tells the model how many expenses matched, so a long list can end
// with «и ещё N на X ₽» instead of overflowing a Telegram message.
type listResult struct {
	Items       []expense.Expense `json:"items"`
	TotalCount  int               `json:"total_count"`
	TotalAmount expense.Money     `json:"total_amount"`
}

var changeVerbs = map[string]string{"add_expense": "записано", "update_expense": "изменено", "delete_expense": "удалено"}

// runTool executes one tool call. Bad arguments and missing expenses go back
// to the model as {"error": ...} so it can ask the user; other errors abort the reply.
// change describes a successful add/update/delete for the user, "" otherwise.
func runTool(ctx context.Context, s *expense.Store, today string, call ToolCall) (out, change string, err error) {
	result, err := execTool(ctx, s, today, call.Function.Name, call.Function.Arguments)
	var verr *expense.ValidationError
	switch {
	case errors.As(err, &verr):
		return errorJSON(verr.Error()), "", nil
	case errors.Is(err, expense.ErrNotFound):
		return errorJSON("трата с таким id не найдена среди трат пользователя"), "", nil
	case err != nil:
		return "", "", fmt.Errorf("tool %s: %w", call.Function.Name, err)
	}
	if e, ok := result.(expense.Expense); ok {
		change = fmt.Sprintf("%s: %s — %s ₽, %s, %s", changeVerbs[call.Function.Name], e.Comment, e.Amount, e.Category, e.SpentAt)
	}
	b, err := json.Marshal(result)
	if err != nil {
		return "", change, fmt.Errorf("encode tool result: %w", err)
	}
	return string(b), change, nil
}

func execTool(ctx context.Context, s *expense.Store, today, name, args string) (any, error) {
	decode := func(v any) error {
		if strings.TrimSpace(args) == "" {
			args = "{}"
		}
		if err := json.Unmarshal([]byte(args), v); err != nil {
			var verr *expense.ValidationError
			if errors.As(err, &verr) {
				return verr
			}
			return &expense.ValidationError{Field: "arguments", Msg: "некорректный JSON аргументов: " + err.Error()}
		}
		return nil
	}

	switch name {
	case "add_expense":
		var in expense.Input
		if err := decode(&in); err != nil {
			return nil, err
		}
		in.UserID = 0 // the store sets the owner
		if err := in.Validate(today); err != nil {
			return nil, err
		}
		return s.Create(ctx, in)

	case "find_expenses":
		var fa filterArgs
		if err := decode(&fa); err != nil {
			return nil, err
		}
		f, err := fa.filter()
		if err != nil {
			return nil, err
		}
		items, err := s.List(ctx, f)
		if err != nil {
			return nil, err
		}
		f.Limit = 0
		sum, err := s.Sum(ctx, f)
		if err != nil {
			return nil, err
		}
		return listResult{Items: items, TotalCount: sum.Count, TotalAmount: sum.Total}, nil

	case "sum_expenses":
		var fa filterArgs
		if err := decode(&fa); err != nil {
			return nil, err
		}
		f, err := fa.filter()
		if err != nil {
			return nil, err
		}
		return s.Sum(ctx, f)

	case "update_expense":
		var ua updateArgs
		if err := decode(&ua); err != nil {
			return nil, err
		}
		p := expense.Patch{Amount: ua.Amount, Category: ua.Category, SpentAt: ua.SpentAt, Comment: ua.Comment}
		if err := p.Validate(today); err != nil {
			return nil, err
		}
		return s.Update(ctx, ua.ID, p)

	case "delete_expense":
		var da struct {
			ID int64 `json:"id"`
		}
		if err := decode(&da); err != nil {
			return nil, err
		}
		return s.Delete(ctx, da.ID)
	}
	return nil, &expense.ValidationError{Field: "tool", Msg: "неизвестный инструмент " + name}
}

func errorJSON(msg string) string {
	b, _ := json.Marshal(map[string]string{"error": msg})
	return string(b)
}
