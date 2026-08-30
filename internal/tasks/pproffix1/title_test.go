package pproffix1

import "testing"

// Пример из задачи: вставка «с практикой и» перед «внесением в ФИС ФРДО». Правило обязано
// вывестись само и примениться к любому заголовку с тем же якорем.
func TestTitleRuleAppliesInsertionFromExample(t *testing.T) {
	rule, err := NewTitleRule(
		"Медсестра в санэпидконтроле - дистанционное обучение с внесением в ФИС ФРДО",
		"Медсестра в санэпидконтроле - дистанционное обучение с практикой и внесением в ФИС ФРДО")
	if err != nil {
		t.Fatalf("NewTitleRule: %v", err)
	}
	got, err := rule.Apply("Фельдшер скорой помощи - дистанционное обучение с внесением в ФИС ФРДО")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	want := "Фельдшер скорой помощи - дистанционное обучение с практикой и внесением в ФИС ФРДО"
	if got != want {
		t.Fatalf("Apply = %q, want %q", got, want)
	}
}

// Повторный прогон не имеет права дать «с практикой и с практикой и»: обрыв случается, и
// команду запускают заново.
func TestTitleRuleLeavesRenamedTitleAlone(t *testing.T) {
	rule, err := NewTitleRule("Курс с внесением в ФИС ФРДО", "Курс с практикой и внесением в ФИС ФРДО")
	if err != nil {
		t.Fatalf("NewTitleRule: %v", err)
	}
	renamed := "Другой курс с практикой и внесением в ФИС ФРДО"
	got, err := rule.Apply(renamed)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got != renamed {
		t.Fatalf("Apply = %q, want без изменений %q", got, renamed)
	}
}

// Заголовок без якоря — ошибка, а не молчаливый пропуск: человек должен увидеть статью,
// которую правило не берёт, до того как её текст уйдёт в блог.
func TestTitleRuleFailsWithoutAnchor(t *testing.T) {
	rule, err := NewTitleRule("Курс с внесением в ФИС ФРДО", "Курс с практикой и внесением в ФИС ФРДО")
	if err != nil {
		t.Fatalf("NewTitleRule: %v", err)
	}
	if _, err := rule.Apply("Совсем другая статья про профессии"); err == nil {
		t.Fatal("Apply вернул успех для заголовка без якоря")
	}
}

// Замена, а не вставка: изменившееся слово становится правилом целиком.
func TestTitleRuleReplacesChangedWords(t *testing.T) {
	rule, err := NewTitleRule("Обучение по повышению квалификации", "Обучение по программе")
	if err != nil {
		t.Fatalf("NewTitleRule: %v", err)
	}
	got, err := rule.Apply("Заочное обучение по повышению квалификации для врачей")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if want := "Заочное обучение по программе для врачей"; got != want {
		t.Fatalf("Apply = %q, want %q", got, want)
	}
}

func TestNewTitleRuleRejectsExampleWithoutChange(t *testing.T) {
	if _, err := NewTitleRule("Один и тот же заголовок", "Один и тот же заголовок"); err == nil {
		t.Fatal("правило выведено из примера, который ничего не меняет")
	}
}
