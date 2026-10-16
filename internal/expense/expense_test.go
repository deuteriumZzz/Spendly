package expense

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestParseMoney(t *testing.T) {
	ok := map[string]Money{
		"350": 35000, "350.5": 35050, "350,50": 35050, "1 200": 120000,
		"0.01": 1, " 12 ": 1200, "1 200,5": 120050,
	}
	for in, want := range ok {
		got, err := ParseMoney(in)
		if err != nil || got != want {
			t.Errorf("ParseMoney(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "0", "0.00", "-5", "1.234", "abc", "1e3", "12.", ".5", "99999999999999999999"} {
		if _, err := ParseMoney(in); err == nil {
			t.Errorf("ParseMoney(%q) accepted, want error", in)
		}
	}
}

func TestMoneyJSON(t *testing.T) {
	b, _ := json.Marshal(Money(35050))
	if string(b) != "350.50" {
		t.Errorf("marshal = %s, want 350.50", b)
	}
	var in struct{ A, B Money }
	if err := json.Unmarshal([]byte(`{"A": 350.5, "B": "1 200"}`), &in); err != nil || in.A != 35050 || in.B != 120000 {
		t.Errorf("unmarshal = %+v, %v", in, err)
	}
	var bad struct{ A Money }
	var verr *ValidationError
	if err := json.Unmarshal([]byte(`{"A": -1}`), &bad); !errors.As(err, &verr) {
		t.Errorf("negative amount: err = %v, want ValidationError", err)
	}
}

func TestParseCategory(t *testing.T) {
	for in, want := range map[string]Category{"Жилье": "жильё", " ЕДА ": "еда", "жильё": "жильё"} {
		if got, err := ParseCategory(in); err != nil || got != want {
			t.Errorf("ParseCategory(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := ParseCategory("кофе"); err == nil {
		t.Error("ParseCategory(кофе) accepted, want error")
	}
}

func TestInputValidate(t *testing.T) {
	const today = "2026-10-16"
	valid := Input{Amount: 35000, Category: "еда", SpentAt: today}
	if err := valid.Validate(today); err != nil {
		t.Fatalf("valid input: %v", err)
	}
	cases := map[string]Input{
		"future date":  {Amount: 1, Category: "еда", SpentAt: "2026-10-17"},
		"bad date":     {Amount: 1, Category: "еда", SpentAt: "16.10.2026"},
		"no amount":    {Category: "еда", SpentAt: today},
		"no category":  {Amount: 1, SpentAt: today},
		"long comment": {Amount: 1, Category: "еда", SpentAt: today, Comment: string(make([]rune, 501))},
	}
	for name, in := range cases {
		if err := in.Validate(today); err == nil {
			t.Errorf("%s: accepted, want error", name)
		}
	}
	if err := (Patch{}).Validate(today); err == nil {
		t.Error("empty patch accepted, want error")
	}
}

func TestValidateName(t *testing.T) {
	if got, err := ValidateName("  Мама "); err != nil || got != "Мама" {
		t.Errorf("ValidateName = %q, %v", got, err)
	}
	for _, in := range []string{"", "   ", "оченьдлинноеимяоченьдлинноеимяоченьдлинно"} {
		if _, err := ValidateName(in); err == nil {
			t.Errorf("ValidateName(%q) accepted", in)
		}
	}
}
