package knowledge

import (
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"supportflow/internal/ticket"
)

func TestSearchArticles(t *testing.T) {
	for _, tc := range []struct{ query, first string }{
		{"Дважды списали деньги за подписку", "billing-double-charge"},
		{"Оплатил тариф, доступ не появился", "billing-paid-access"},
		{"Забыл пароль, письмо для сброса не пришло", "access-password"},
		{"Приглашение не работает, администратор проверил права", "access-invitation"},
		{"Ошибка при выгрузке отчета CSV", "technical-export"},
		{"Массовый сбой, у всех ошибка 500", "technical-outage"},
		{"Добавьте темную тему", "product-feedback"},
		{"Здравствуйте! Спасибо за помощь", ""},
		{"Когда открывается магазин на соседней улице?", ""},
	} {
		t.Run(tc.query, func(t *testing.T) {
			found, err := Search(tc.query)
			if err != nil {
				t.Fatal(err)
			}
			if tc.first == "" {
				if len(found) != 0 {
					t.Fatalf("unrelated query returned %+v", found)
				}
				return
			}
			if len(found) == 0 || found[0].ID != tc.first || len(found) > 3 {
				t.Fatalf("Search() = %+v, want first %s", found, tc.first)
			}
			for _, source := range found {
				if err := source.Validate(); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestSearchNormalizationAndIsolation(t *testing.T) {
	first, err := Search("Ошибка выгрузки отчёта")
	if err != nil {
		t.Fatal(err)
	}
	second, err := Search("ОШИБКА ВЫГРУЗКИ ОТЧЕТА отчета отчета")
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatalf("normalization changed ranking: %+v %+v %v", first, second, err)
	}
	first[0].Content = "changed by caller"
	again, err := Search("Ошибка выгрузки отчёта")
	if err != nil || !reflect.DeepEqual(second, again) {
		t.Fatalf("caller mutated catalog: %+v %v", again, err)
	}
}

func TestRankBoundsAndStableTies(t *testing.T) {
	var catalog []article
	for _, id := range []string{"d", "c", "b", "a"} {
		catalog = append(catalog, article{source: ticket.Source{ID: id}, terms: map[string]int{"тест": 1}})
	}
	found := rank(catalog, "тест")
	if len(found) != 3 || found[0].ID != "a" || found[1].ID != "b" || found[2].ID != "c" {
		t.Fatalf("rank = %+v, want a,b,c", found)
	}
}

func TestRejectMalformedArticles(t *testing.T) {
	for _, content := range []string{
		"no header", "# Title\n\nКлючевые слова: \n\nBody",
		"# Title\n\nКлючевые слова: test\n\n" + strings.Repeat("x", 4097),
		"# Title\n\nКлючевые слова: test\n\nBad\x00text",
	} {
		files := fstest.MapFS{"articles/test.md": &fstest.MapFile{Data: []byte(content)}}
		if _, err := load(files); err == nil {
			t.Errorf("accepted invalid article")
		}
	}
	if _, err := load(fstest.MapFS{}); err == nil {
		t.Fatal("accepted empty catalog")
	}
}
