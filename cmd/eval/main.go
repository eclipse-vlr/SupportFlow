package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"supportflow/internal/billing"
	"supportflow/internal/config"
	"supportflow/internal/llm"
	"supportflow/internal/store"
	"supportflow/internal/ticket"
)

type evaluationCase struct {
	ticket.Input
	ExpectedCategory string   `json:"expected_category"`
	ExpectedPriority string   `json:"expected_priority,omitempty"`
	ForbiddenDraft   []string `json:"forbidden_draft,omitempty"`
}

type caseResult struct {
	evaluationCase
	Analysis      *ticket.Analysis `json:"analysis,omitempty"`
	Usage         ticket.Usage     `json:"usage"`
	DurationMS    int64            `json:"duration_ms"`
	Error         string           `json:"error,omitempty"`
	ForbiddenHits []string         `json:"forbidden_hits,omitempty"`
	ManualReview  string           `json:"manual_review"`
}

type evaluationReport struct {
	CreatedAt         time.Time    `json:"created_at"`
	Provider          string       `json:"provider"`
	Correct           int          `json:"correct"`
	Total             int          `json:"total"`
	Failures          int          `json:"failures"`
	InputTokens       int64        `json:"input_tokens"`
	OutputTokens      int64        `json:"output_tokens"`
	Cases             []caseResult `json:"cases"`
	PriorityCorrect   int          `json:"priority_correct"`
	PriorityTotal     int          `json:"priority_total"`
	DraftRuleFailures int          `json:"draft_rule_failures"`
	UnknownCalls      int          `json:"unknown_cases"`
}

func main() {
	if err := run(); err != nil && !errors.Is(err, flag.ErrHelp) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	return runArgs(os.Args[1:], os.Stdout)
}

func runArgs(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("eval", flag.ContinueOnError)
	flags.SetOutput(output)
	path := flags.String("dataset", "testdata/eval.json", "JSON evaluation dataset")
	limit := flags.Int("limit", 5, "maximum number of cases; 0 means all")
	allowDemo := flags.Bool("allow-demo", false, "evaluate keyword demo instead of an LLM (not model quality)")
	reportPath := flags.String("report", "", "write a new JSON report with inputs, drafts and token usage; existing files are not overwritten")
	minAccuracy := flags.Float64("min-accuracy", 0, "minimum classification accuracy in percent (0-100); provider failures always fail")
	checkDraft := flags.Bool("check-draft-rules", false, "fail on dataset forbidden phrases; human review is still required")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments")
	}
	if *limit < 0 {
		return fmt.Errorf("limit must be non-negative")
	}
	if math.IsNaN(*minAccuracy) || math.IsInf(*minAccuracy, 0) || *minAccuracy < 0 || *minAccuracy > 100 {
		return fmt.Errorf("min-accuracy must be between 0 and 100")
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
		startup, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		pool, err := pgxpool.New(startup, cfg.DatabaseURL)
		if err != nil {
			return err
		}
		defer pool.Close()
		repository := store.New(pool, cfg.MaxQueued)
		if err = repository.Migrate(startup); err != nil {
			return err
		}
		client.SetBilling(repository, cfg.Billing)
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
	fmt.Fprintf(output, "provider=%s cases=%d (classification only; reply correctness requires human review)\n", cfg.Provider, count)
	report := evaluate(context.Background(), analyzer, cases[:count], cfg.LLMTimeout, output)
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
	accuracy := 100 * float64(report.Correct) / float64(count)
	fmt.Fprintf(output, "accuracy=%d/%d (%.1f%%) failures=%d reported_tokens=%d\n", report.Correct, count, accuracy, report.Failures, report.InputTokens+report.OutputTokens)
	if report.Failures > 0 {
		return fmt.Errorf("evaluation had provider failures")
	}
	fmt.Fprintf(output, "priority=%d/%d draft_rule_failures=%d unknown_usage_cases=%d human_review=pending\n", report.PriorityCorrect, report.PriorityTotal, report.DraftRuleFailures, report.UnknownCalls)
	if *checkDraft && report.DraftRuleFailures > 0 {
		return fmt.Errorf("draft violated dataset rules")
	}
	if accuracy < *minAccuracy {
		return fmt.Errorf("classification accuracy %.1f%% is below required %.1f%%", accuracy, *minAccuracy)
	}
	return nil
}

func evaluate(ctx context.Context, analyzer llm.Analyzer, cases []evaluationCase, timeout time.Duration, output io.Writer) evaluationReport {
	report := evaluationReport{CreatedAt: time.Now().UTC(), Total: len(cases), Cases: make([]caseResult, 0, len(cases))}
	for index, item := range cases {
		started := time.Now()
		callCtx, cancel := context.WithTimeout(ctx, timeout)
		result, usage, callErr := analyzer.Analyze(billing.WithOwner(callCtx, "eval-"+rand.Text(), 1), item.Input)
		cancel()
		// Yandex model URIs contain the local folder ID; reports only need the model name.
		if strings.HasPrefix(usage.Model, "gpt://") {
			_, usage.Model, _ = strings.Cut(strings.TrimPrefix(usage.Model, "gpt://"), "/")
		}
		report.InputTokens += usage.InputTokens
		report.OutputTokens += usage.OutputTokens
		entry := caseResult{evaluationCase: item, Usage: usage, DurationMS: time.Since(started).Milliseconds()}
		entry.ManualReview = "pending"
		if usage.Unknown {
			report.UnknownCalls++
		}
		if callErr != nil {
			report.Failures++
			entry.Error = callErr.Error()
			report.Cases = append(report.Cases, entry)
			fmt.Fprintf(output, "%02d ERROR %v\n", index+1, callErr)
			continue
		}
		entry.Analysis = &result
		if item.ExpectedPriority != "" {
			report.PriorityTotal++
			if result.Priority == item.ExpectedPriority {
				report.PriorityCorrect++
			}
		}
		for _, phrase := range item.ForbiddenDraft {
			if strings.Contains(strings.ToLower(result.DraftReply), strings.ToLower(phrase)) {
				entry.ForbiddenHits = append(entry.ForbiddenHits, phrase)
			}
		}
		if len(entry.ForbiddenHits) > 0 {
			report.DraftRuleFailures++
		}
		report.Cases = append(report.Cases, entry)
		if result.Category == item.ExpectedCategory {
			report.Correct++
		}
		fmt.Fprintf(output, "%02d expected=%s actual=%s\n", index+1, item.ExpectedCategory, result.Category)
	}
	return report
}
