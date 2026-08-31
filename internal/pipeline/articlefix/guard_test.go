package articlefix

import (
	"errors"
	"fmt"
	"testing"
)

// Единичный отказ пачку не останавливает: длинная статья вправе не даться, а остальные —
// пройти. Ради этого предохранитель и считает подряд идущие, а не все отказы прогона.
func TestFailureGuardLetsSingleFailuresThrough(t *testing.T) {
	guard := NewFailureGuard()
	for range 5 {
		if stop := guard.Failed(errors.New("статья не далась")); stop != nil {
			t.Fatalf("прогон остановлен на единичном отказе: %v", stop)
		}
		guard.Passed()
	}
}

// Две подряд статьи с одной и той же причиной останавливают прогон: одинаковая формулировка
// дважды означает, что дело уже не в тексте статьи.
func TestFailureGuardStopsOnSameReasonTwice(t *testing.T) {
	guard := NewFailureGuard()
	fail := func(article int) error {
		return fmt.Errorf(`правка статьи %d: LLM stage "rewrite" provider "deepseek_web": `+
			`wait for complete DeepSeek answer`, article)
	}
	if stop := guard.Failed(fail(30)); stop != nil {
		t.Fatalf("прогон остановлен на первом отказе: %v", stop)
	}
	stop := guard.Failed(fail(31))
	if stop == nil {
		t.Fatal("две подряд одинаковые причины прогон не остановили")
	}
	if !errors.Is(stop, ErrRunStopped) {
		t.Fatalf("ошибка не оборачивает ErrRunStopped: %v", stop)
	}
}

// Номер статьи и длительности внутри сообщения не делают причину другой: иначе одинаковые по
// сути отказы считались бы разными и предохранитель молчал бы.
func TestFailureGuardIgnoresVaryingNumbersInReason(t *testing.T) {
	if a, b := failureReason(errors.New(`запись 21615 не ответила за 300s`)),
		failureReason(errors.New(`запись 21777 не ответила за 305s`)); a != b {
		t.Fatalf("причины разошлись:\n  %q\n  %q", a, b)
	}
}

// Разные причины подряд — тоже остановка, просто порог выше: сломалось окружение, а не текст.
func TestFailureGuardStopsOnThreeDifferentReasons(t *testing.T) {
	guard := NewFailureGuard()
	if stop := guard.Failed(errors.New("площадка отказала")); stop != nil {
		t.Fatalf("остановка на первом отказе: %v", stop)
	}
	if stop := guard.Failed(errors.New("запись не найдена по слагу")); stop != nil {
		t.Fatalf("остановка на втором отказе с другой причиной: %v", stop)
	}
	if stop := guard.Failed(errors.New("правило переименования не подошло")); stop == nil {
		t.Fatal("три разные причины подряд прогон не остановили")
	}
}

// Успешная статья снимает накопленное: чередование «упала — прошла» это не сплошной отказ.
func TestFailureGuardResetsAfterSuccess(t *testing.T) {
	guard := NewFailureGuard()
	same := errors.New("одна и та же беда")
	if stop := guard.Failed(same); stop != nil {
		t.Fatalf("остановка на первом отказе: %v", stop)
	}
	guard.Passed()
	if stop := guard.Failed(same); stop != nil {
		t.Fatalf("остановка после успешной статьи между отказами: %v", stop)
	}
}
