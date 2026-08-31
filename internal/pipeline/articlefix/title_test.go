package articlefix

import (
	"os"
	"path/filepath"
	"testing"
)

// Пример из задачи: вставка «с практикой и» перед «внесением в ФИС ФРДО». Правило обязано
// вывестись само и примениться к любому заголовку с тем же якорем.
func TestPairRuleAppliesInsertionFromExample(t *testing.T) {
	rule, err := NewPairRule(
		"Медсестра в санэпидконтроле - дистанционное обучение с внесением в ФИС ФРДО",
		"Медсестра в санэпидконтроле - дистанционное обучение с практикой и внесением в ФИС ФРДО")
	if err != nil {
		t.Fatalf("NewPairRule: %v", err)
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
func TestPairRuleLeavesRenamedTitleAlone(t *testing.T) {
	rule, err := NewPairRule("Курс с внесением в ФИС ФРДО", "Курс с практикой и внесением в ФИС ФРДО")
	if err != nil {
		t.Fatalf("NewPairRule: %v", err)
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
func TestPairRuleFailsWithoutAnchor(t *testing.T) {
	rule, err := NewPairRule("Курс с внесением в ФИС ФРДО", "Курс с практикой и внесением в ФИС ФРДО")
	if err != nil {
		t.Fatalf("NewPairRule: %v", err)
	}
	if _, err := rule.Apply("Совсем другая статья про профессии"); err == nil {
		t.Fatal("Apply вернул успех для заголовка без якоря")
	}
}

// Замена, а не вставка: изменившееся слово становится правилом целиком.
func TestPairRuleReplacesChangedWords(t *testing.T) {
	rule, err := NewPairRule("Обучение по повышению квалификации", "Обучение по программе")
	if err != nil {
		t.Fatalf("NewPairRule: %v", err)
	}
	got, err := rule.Apply("Заочное обучение по повышению квалификации для врачей")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if want := "Заочное обучение по программе для врачей"; got != want {
		t.Fatalf("Apply = %q, want %q", got, want)
	}
}

func TestNewPairRuleRejectsExampleWithoutChange(t *testing.T) {
	if _, err := NewPairRule("Один и тот же заголовок", "Один и тот же заголовок"); err == nil {
		t.Fatal("правило выведено из примера, который ничего не меняет")
	}
}

// KeepTitle возвращает заголовок как есть — и делает это одинаково при повторе: задача,
// которая правит только текст, обязана оставлять название страницы нетронутым.
func TestKeepTitleLeavesTitleAsIs(t *testing.T) {
	const title = "Врач-хирург - дистанционная переподготовка с внесением в ФИС ФРДО"
	got, err := KeepTitle{}.Apply("  " + title + "  ")
	if err != nil {
		t.Fatalf("правило отвергло заголовок: %v", err)
	}
	if got != title {
		t.Fatalf("заголовок изменился: %q → %q", title, got)
	}
	again, err := KeepTitle{}.Apply(got)
	if err != nil || again != title {
		t.Fatalf("повторный проход дал %q, %v", again, err)
	}
}

// Пустой заголовок остаётся ошибкой: это не «нечего менять», а сорванное чтение из блога.
func TestKeepTitleRejectsEmpty(t *testing.T) {
	if _, err := (KeepTitle{}).Apply("   "); err == nil {
		t.Fatal("пустой заголовок принят")
	}
}

// Несколько пар в файле дают набор правил: в пачке встречаются оба написания якоря, и
// длинное обязано проверяться раньше короткого — иначе результат зависел бы от порядка строк.
func TestLoadPairRuleReadsSeveralPairs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "title_rule.txt")
	// Короткая пара написана первой намеренно: порядок в файле не должен ни на что влиять.
	content := "# пример\nбыло: Аккредитация врачей с внесением в ФРДО\n" +
		"стало: Аккредитация врачей с внесением сведений в ЕГИСЗ\n\n" +
		"было: Аккредитация врачей с внесением в ФИС ФРДО\n" +
		"стало: Аккредитация врачей с внесением сведений в ЕГИСЗ\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("подготовить файл: %v", err)
	}
	rule, err := LoadPairRule(path)
	if err != nil {
		t.Fatalf("LoadPairRule: %v", err)
	}
	cases := map[string]string{
		"Первичная аккредитация специалистов с внесением в ФИС ФРДО":     "Первичная аккредитация специалистов с внесением сведений в ЕГИСЗ",
		"Периодическая аккредитация врачей с внесением в ФРДО":           "Периодическая аккредитация врачей с внесением сведений в ЕГИСЗ",
		"Периодическая аккредитация врачей с внесением сведений в ЕГИСЗ": "Периодическая аккредитация врачей с внесением сведений в ЕГИСЗ",
	}
	for title, want := range cases {
		got, applyErr := rule.Apply(title)
		if applyErr != nil {
			t.Fatalf("Apply(%q): %v", title, applyErr)
		}
		if got != want {
			t.Fatalf("Apply(%q) = %q, ожидалось %q", title, got, want)
		}
	}
	if _, applyErr := rule.Apply("Совсем другой заголовок"); applyErr == nil {
		t.Fatal("заголовок без якоря обязан быть ошибкой")
	}
}
