package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"supportflow/internal/config"
	"supportflow/internal/llm"
	"supportflow/internal/ticket"
)

type evaluationCase struct {
	ticket.Input
	ExpectedCategory string `json:"expected_category"`
}

type caseResult struct {
	evaluationCase
	Analysis   *ticket.Analysis `json:"analysis,omitempty"`
	Usage      ticket.Usage     `json:"usage"`
	DurationMS int64            `json:"duration_ms"`
	Error      string           `json:"error,omitempty"`
}

type evaluationReport struct {
	CreatedAt    time.Time    `json:"created_at"`
	Provider     string       `json:"provider"`
	Correct      int          `json:"correct"`
	Total        int          `json:"total"`
	Failures     int          `json:"failures"`
	InputTokens  int64        `json:"input_tokens"`
	OutputTokens int64        `json:"output_tokens"`
	Cases        []caseResult `json:"cases"`
}

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
	reportPath := flag.String("report", "", "write a new JSON report with inputs, drafts and token usage; existing files are not overwritten")
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
	var cases []evaluationCase
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
	var reportFile *os.File
	if *reportPath != "" {
		reportFile, err = os.OpenFile(*reportPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return fmt.Errorf("create report before calling provider: %w", err)
		}
		defer reportFile.Close()
	}
	fmt.Printf("provider=%s cases=%d (classification only; reply correctness requires human review)\n", cfg.Provider, count)
	report := evaluate(context.Background(), analyzer, cases[:count], cfg.LLMTimeout, os.Stdout)
	report.Provider = cfg.Provider
	if reportFile != nil {
		encoder := json.NewEncoder(reportFile)
		encoder.SetIndent("", "  ")
		if err = encoder.Encode(report); err != nil {
			return fmt.Errorf("write evaluation report: %w", err)
		}
		if err = reportFile.Close(); err != nil {
			return fmt.Errorf("close evaluation report: %w", err)
		}
	}
	fmt.Printf("accuracy=%d/%d (%.1f%%) failures=%d reported_tokens=%d\n", report.Correct, count, 100*float64(report.Correct)/float64(count), report.Failures, report.InputTokens+report.OutputTokens)
	if report.Failures > 0 {
		return fmt.Errorf("evaluation had provider failures")
	}
	return nil
}

func evaluate(ctx context.Context, analyzer llm.Analyzer, cases []evaluationCase, timeout time.Duration, output io.Writer) evaluationReport {
	report := evaluationReport{CreatedAt: time.Now().UTC(), Total: len(cases), Cases: make([]caseResult, 0, len(cases))}
	for index, item := range cases {
		started := time.Now()
		callCtx, cancel := context.WithTimeout(ctx, timeout)
		result, usage, callErr := analyzer.Analyze(callCtx, item.Input)
		cancel()
		// Yandex model URIs contain the local folder ID; reports only need the model name.
		if strings.HasPrefix(usage.Model, "gpt://") {
			_, usage.Model, _ = strings.Cut(strings.TrimPrefix(usage.Model, "gpt://"), "/")
		}
		report.InputTokens += usage.InputTokens
		report.OutputTokens += usage.OutputTokens
		entry := caseResult{evaluationCase: item, Usage: usage, DurationMS: time.Since(started).Milliseconds()}
		if callErr != nil {
			report.Failures++
			entry.Error = callErr.Error()
			report.Cases = append(report.Cases, entry)
			fmt.Fprintf(output, "%02d ERROR %v\n", index+1, callErr)
			continue
		}
		entry.Analysis = &result
		report.Cases = append(report.Cases, entry)
		if result.Category == item.ExpectedCategory {
			report.Correct++
		}
		fmt.Fprintf(output, "%02d expected=%s actual=%s\n", index+1, item.ExpectedCategory, result.Category)
	}
	return report
}
