// Package knowledge retrieves a small, embedded collection of support articles.
package knowledge

import (
	"crypto/sha256"
	"embed"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
	"unicode"

	"supportflow/internal/ticket"
)

//go:embed articles/*.md
var articles embed.FS

type article struct {
	source ticket.Source
	terms  map[string]int
}

// Search returns at most three snapshots. No external service or model is called.
func Search(query string) ([]ticket.Source, error) {
	catalog, err := load(articles)
	if err != nil {
		return nil, err
	}
	return rank(catalog, query), nil
}

func load(files fs.FS) ([]article, error) {
	names, err := fs.Glob(files, "articles/*.md")
	if err != nil || len(names) == 0 {
		return nil, fmt.Errorf("knowledge catalog is unavailable")
	}
	var catalog []article
	for _, name := range names {
		data, err := fs.ReadFile(files, name)
		if err != nil || len(data) > 8192 {
			return nil, fmt.Errorf("invalid knowledge article: %s", name)
		}
		lines := strings.SplitN(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n", 5)
		if len(lines) != 5 || !strings.HasPrefix(lines[0], "# ") || lines[1] != "" ||
			!strings.HasPrefix(lines[2], "Ключевые слова: ") || lines[3] != "" {
			return nil, fmt.Errorf("invalid knowledge article header: %s", name)
		}
		source := ticket.Source{
			ID: strings.TrimSuffix(path.Base(name), ".md"), Title: strings.TrimSpace(lines[0][2:]),
			Content: strings.TrimSpace(lines[4]), Version: fmt.Sprintf("%x", sha256.Sum256(data)),
		}
		if err := source.Validate(); err != nil {
			return nil, fmt.Errorf("invalid knowledge article: %s: %w", name, err)
		}
		terms := make(map[string]int)
		for term := range tokens(strings.TrimPrefix(lines[2], "Ключевые слова: ")) {
			terms[term] = 1
		}
		if len(terms) == 0 {
			return nil, fmt.Errorf("knowledge article has no keywords: %s", name)
		}
		for term := range tokens(source.Title) {
			terms[term] = 2
		}
		catalog = append(catalog, article{source: source, terms: terms})
	}
	return catalog, nil
}

func rank(catalog []article, query string) []ticket.Source {
	type match struct {
		source ticket.Source
		score  int
	}
	terms := tokens(query)
	var matches []match
	for _, item := range catalog {
		score := 0
		for term := range terms {
			score += item.terms[term]
		}
		if score > 0 {
			matches = append(matches, match{source: item.source, score: score})
		}
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].score == matches[j].score {
			return matches[i].source.ID < matches[j].source.ID
		}
		return matches[i].score > matches[j].score
	})
	var result []ticket.Source
	for _, item := range matches {
		if len(result) == 3 || item.score*2 < matches[0].score {
			break
		}
		result = append(result, item.source)
	}
	return result
}

// A short prefix matches common Russian inflections; this is intentionally not a
// morphological analyzer. Search uses headings and keywords, not generic advice.
func tokens(text string) map[string]bool {
	text = strings.ReplaceAll(strings.ToLower(text), "ё", "е")
	words := strings.FieldsFunc(text, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	result := make(map[string]bool)
	for _, word := range words {
		if strings.Contains("|это|как|что|для|при|или|без|его|она|они|все|мне|мой|моя|уже|еще|так|там|чем|когда|после|нужно|можно|очень|пожалуйста|здравствуйте|спасибо|", "|"+word+"|") {
			continue
		}
		runes := []rune(word)
		if len(runes) < 3 {
			continue
		}
		if len(runes) > 5 {
			word = string(runes[:5])
		}
		result[word] = true
	}
	return result
}
