package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"supportflow/internal/config"
	"supportflow/internal/llm"
	"supportflow/internal/ticket"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	path := flag.String("dataset", "testdata/eval.json", "JSON evaluation dataset")
	limit := flag.Int("limit", 5, "maximum number of cases; 0 means all")
	allowDemo := flag.Bool("allow-demo", false, "evaluate keyword demo instead of an LLM (not model quality)")
	flag.Parse()
	if *limit < 0 {
		return fmt.Errorf("limit must be non-negative")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.Provider == "demo" && !*allowDemo {
		return fmt.Errorf("set LLM_PROVIDER=yandex, YANDEX_API_KEY and YANDEX_FOLDER_ID, or use -allow-demo for rules-only checks")
	}
	data, err := os.ReadFile(*path)
	if err != nil {
		return err
	}
	var cases []struct {
		ticket.Input
		ExpectedCategory string `json:"expected_category"`
	}
	if err = json.Unmarshal(data, &cases); err != nil {
		return err
	}
	var analyzer llm.Analyzer = llm.Demo{}
	if cfg.Provider != "demo" {
		var client *llm.Client
		if cfg.Provider == "yandex" {
			client = llm.NewYandex(cfg.BaseURL, cfg.APIKey, cfg.FolderID, cfg.Model, cfg.MaxOutputTokens, cfg.LLMTimeout)
		} else {
			client = llm.New(cfg.BaseURL, cfg.APIKey, cfg.Model, cfg.MaxOutputTokens, cfg.LLMTimeout)
		}
		defer client.Close()
		analyzer = client
	}
	count := len(cases)
	if *limit > 0 {
		count = min(count, *limit)
	}
	if count == 0 {
		return fmt.Errorf("empty dataset")
	}
	for _, item := range cases[:count] {
		if err = item.Input.Validate(); err != nil || !ticket.ValidCategory(item.ExpectedCategory) {
			return fmt.Errorf("invalid dataset case")
		}
	}
	fmt.Printf("provider=%s cases=%d (classification only; reply correctness requires human review)\n", cfg.Provider, count)
	correct, failures := 0, 0
	var tokens int64
	for index, item := range cases[:count] {
		ctx, cancel := context.WithTimeout(context.Background(), cfg.LLMTimeout)
		result, usage, callErr := analyzer.Analyze(ctx, item.Input)
		cancel()
		tokens += usage.InputTokens + usage.OutputTokens
		if callErr != nil {
			failures++
			fmt.Printf("%02d ERROR %v\n", index+1, callErr)
			continue
		}
		if result.Category == item.ExpectedCategory {
			correct++
		}
		fmt.Printf("%02d expected=%s actual=%s\n", index+1, item.ExpectedCategory, result.Category)
	}
	fmt.Printf("accuracy=%d/%d (%.1f%%) failures=%d reported_tokens=%d\n", correct, count, 100*float64(correct)/float64(count), failures, tokens)
	if failures > 0 {
		return fmt.Errorf("evaluation had provider failures")
	}
	return nil
}
