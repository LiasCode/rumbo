// Command evals runs rumbo on a fixed set of small tasks and reports, for
// each one, whether the result is correct and what it cost. It exists so a
// change to the loop, the prompt, or the tools can be measured.
//
// Run it from the repository root:
//
//	go run ./evals -model anthropic/claude-opus-5-5
//	go run ./evals -selftest
//
// See evals/README.md for how tasks are defined.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"rumbo/internal/cli"
	"rumbo/internal/store"
	"rumbo/internal/transcript"
	"rumbo/protocol"
)

// baseTag marks the commit a task starts from, so a check can tell what the
// agent changed even if the agent made commits of its own.
const baseTag = "eval-base"

// result is the outcome of one run of one task.
type result struct {
	Task      string  `json:"task"`
	Model     string  `json:"model"`
	Run       int     `json:"run"`
	Passed    bool    `json:"passed"`
	ExitCode  int     `json:"exit_code"`
	Seconds   float64 `json:"seconds"`
	Turns     int     `json:"turns"`
	ToolCalls int     `json:"tool_calls"`
	Compacted int     `json:"compactions"`
	Input     int64   `json:"input_tokens"`
	Cached    int64   `json:"cached_tokens"`
	Output    int64   `json:"output_tokens"`
	// Check is what the check printed when it failed.
	Check string `json:"check,omitempty"`
	// Workdir is kept, and set, only for failed runs and with -keep.
	Workdir string `json:"workdir,omitempty"`
}

type options struct {
	model    string
	filter   string
	runs     int
	timeout  time.Duration
	keep     bool
	out      string
	selftest bool
	settings settings
}

// settings are key=value pairs applied to every run's database, to measure
// the same tasks under a different configuration.
type settings []string

func (s *settings) String() string {
	return strings.Join(*s, ",")
}

func (s *settings) Set(value string) error {
	if key, _, found := strings.Cut(value, "="); !found || key == "" {
		return errors.New("expected key=value")
	}

	*s = append(*s, value)

	return nil
}

func main() {
	var opts options

	flag.StringVar(&opts.model, "model", "", "provider/model to evaluate")
	flag.StringVar(&opts.filter, "task", "", "run only the tasks whose name contains this text")
	flag.IntVar(&opts.runs, "runs", 1, "times to run each task")
	flag.DurationVar(&opts.timeout, "timeout", 10*time.Minute, "time limit for one run")
	flag.BoolVar(&opts.keep, "keep", false, "keep the working directory of every run, not only of failed ones")
	flag.StringVar(&opts.out, "out", "", "append each result to this file as a JSON line")
	flag.BoolVar(&opts.selftest, "selftest", false, "check the tasks themselves, without a model")
	flag.Var(&opts.settings, "set", "a rumbo setting for every run, as key=value; may be repeated")
	flag.Parse()

	if err := run(opts); err != nil {
		fmt.Fprintln(os.Stderr, "evals:", err)
		os.Exit(1)
	}
}

func run(opts options) error {
	tasks, err := findTasks(opts.filter)

	if err != nil {
		return err
	}

	if opts.selftest {
		return selftest(tasks)
	}

	if opts.model == "" {
		return errors.New("pass -model <provider>/<model>, or -selftest")
	}

	scratch, err := os.MkdirTemp("", "rumbo-evals-")

	if err != nil {
		return err
	}

	binary := filepath.Join(scratch, "rumbo")

	if output, err := exec.Command("go", "build", "-o", binary, "./cmd/rumbo").CombinedOutput(); err != nil {
		return fmt.Errorf("build rumbo: %s", output)
	}

	provider, err := registeredProvider(opts.model)

	if err != nil {
		return err
	}

	var results []result

	for _, task := range tasks {
		for attempt := 1; attempt <= opts.runs; attempt++ {
			fmt.Fprintf(os.Stderr, "%s (run %d)... ", filepath.Base(task), attempt)

			outcome, err := evaluate(task, binary, scratch, attempt, opts, provider)

			if err != nil {
				return fmt.Errorf("%s: %w", filepath.Base(task), err)
			}

			fmt.Fprintln(os.Stderr, verdict(outcome.Passed))
			results = append(results, outcome)

			if err := record(opts.out, outcome); err != nil {
				return err
			}
		}
	}

	report(results)

	// Failed runs keep their working directory; the rest of the scratch space
	// goes, and with it the directory itself when nothing was kept.
	_ = os.Remove(binary)
	_ = os.Remove(scratch)

	return nil
}

// findTasks returns the task directories, sorted by name.
func findTasks(filter string) ([]string, error) {
	root, err := filepath.Abs(filepath.Join("evals", "tasks"))

	if err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(root)

	if err != nil {
		return nil, fmt.Errorf("run from the repository root: %w", err)
	}

	var tasks []string

	for _, entry := range entries {
		if entry.IsDir() && strings.Contains(entry.Name(), filter) {
			tasks = append(tasks, filepath.Join(root, entry.Name()))
		}
	}

	if len(tasks) == 0 {
		return nil, fmt.Errorf("no task matches %q", filter)
	}

	sort.Strings(tasks)

	return tasks, nil
}

// evaluate runs rumbo on one task in a fresh copy of its project and checks
// the result.
func evaluate(task, binary, scratch string, attempt int, opts options, provider *store.Program) (result, error) {
	outcome := result{Task: filepath.Base(task), Model: opts.model, Run: attempt}

	workdir, err := os.MkdirTemp(scratch, outcome.Task+"-")

	if err != nil {
		return outcome, err
	}

	repo, err := prepare(task, workdir)

	if err != nil {
		return outcome, err
	}

	objective, err := os.ReadFile(filepath.Join(task, "objective.txt"))

	if err != nil {
		return outcome, err
	}

	env := environment(task, workdir)

	if err := configure(filepath.Join(workdir, "eval.db"), provider, opts.settings); err != nil {
		return outcome, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), opts.timeout)
	defer cancel()

	agent := exec.CommandContext(ctx, binary, "-f", "plain", "-m", opts.model, strings.TrimSpace(string(objective)))
	agent.Dir = repo
	agent.Env = env
	// The interrupt goes to the whole group, because it is the front end,
	// not the host, that turns it into a cancel.
	agent.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	agent.Cancel = func() error { return syscall.Kill(-agent.Process.Pid, syscall.SIGINT) }
	agent.WaitDelay = 30 * time.Second

	if err := redirect(agent, workdir); err != nil {
		return outcome, err
	}

	started := time.Now()
	err = agent.Run()
	outcome.Seconds = time.Since(started).Seconds()
	outcome.ExitCode = exitCode(err)

	if err := measure(&outcome, filepath.Join(workdir, "eval.db")); err != nil {
		return outcome, err
	}

	output, err := check(task, repo, env)
	outcome.Passed = err == nil

	if !outcome.Passed {
		outcome.Check = strings.TrimSpace(output)
	}

	if outcome.Passed && !opts.keep {
		return outcome, os.RemoveAll(workdir)
	}

	outcome.Workdir = workdir

	return outcome, nil
}

// prepare copies the task's project into workdir, runs its setup, and commits
// it, so every run starts from the same repository.
func prepare(task, workdir string) (string, error) {
	repo := filepath.Join(workdir, "repo")

	if err := os.CopyFS(repo, os.DirFS(filepath.Join(task, "repo"))); err != nil {
		return "", err
	}

	steps := [][]string{
		{"git", "init", "--quiet"},
		{"git", "add", "--all"},
		{"git", "-c", "user.name=eval", "-c", "user.email=eval@example.invalid", "commit", "--quiet", "--message", "initial"},
		{"git", "tag", baseTag},
	}

	if setup := filepath.Join(task, "setup.sh"); exists(setup) {
		steps = append([][]string{{"bash", setup}}, steps...)
	}

	for _, step := range steps {
		command := exec.Command(step[0], step[1:]...)
		command.Dir = repo

		if output, err := command.CombinedOutput(); err != nil {
			return "", fmt.Errorf("%s: %s", strings.Join(step, " "), output)
		}
	}

	return repo, nil
}

// environment isolates a run from the machine: its own database, and no
// global instructions, so results depend only on the task and the code.
func environment(task, workdir string) []string {
	return append(os.Environ(),
		"RUMBO_DB="+filepath.Join(workdir, "eval.db"),
		"XDG_CONFIG_HOME="+filepath.Join(workdir, "config"),
		"RUMBO_EVAL_TASK="+task,
		"RUMBO_EVAL_OUTPUT="+filepath.Join(workdir, "output.txt"),
	)
}

// registeredProvider looks the model's provider up in the user's own
// database. Runs use a fresh database, which knows only the built-in
// providers, so one the user registered has to be carried over.
func registeredProvider(model string) (*store.Program, error) {
	name, _, _ := strings.Cut(model, "/")
	path, err := cli.DatabasePath()

	if err != nil || !exists(path) {
		return nil, err
	}

	opened, err := store.Open(path)

	if err != nil {
		return nil, err
	}

	defer opened.Close()

	program, err := opened.Program(store.KindProvider, name)

	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}

	return &program, err
}

// configure prepares a run's database: the provider carried over from the
// user's, and the settings asked for on the command line.
func configure(database string, provider *store.Program, settings []string) error {
	if provider == nil && len(settings) == 0 {
		return nil
	}

	opened, err := store.Open(database)

	if err != nil {
		return err
	}

	defer opened.Close()

	if provider != nil {
		if err := opened.PutProgram(store.KindProvider, *provider); err != nil {
			return err
		}
	}

	for _, setting := range settings {
		key, value, _ := strings.Cut(setting, "=")

		if err := opened.SetSetting(store.GlobalScope, key, value); err != nil {
			return err
		}
	}

	return nil
}

// redirect sends the agent's answer and its progress to files in workdir.
func redirect(agent *exec.Cmd, workdir string) error {
	output, err := os.Create(filepath.Join(workdir, "output.txt"))

	if err != nil {
		return err
	}

	progress, err := os.Create(filepath.Join(workdir, "progress.txt"))

	if err != nil {
		return err
	}

	agent.Stdout = output
	agent.Stderr = progress

	return nil
}

// check runs the task's check in the project. It passes when it exits zero.
func check(task, repo string, env []string) (string, error) {
	command := exec.Command("bash", filepath.Join(task, "check.sh"))
	command.Dir = repo
	command.Env = env

	output, err := command.CombinedOutput()

	return string(output), err
}

// measure reads what the run cost from the session it left in the database.
func measure(outcome *result, database string) error {
	if !exists(database) {
		return nil
	}

	opened, err := store.Open(database)

	if err != nil {
		return err
	}

	defer opened.Close()

	sessions, err := opened.Sessions("")

	if err != nil {
		return err
	}

	for _, session := range sessions {
		entries, err := opened.Entries(session.ID)

		if err != nil {
			return err
		}

		for _, entry := range entries {
			switch entry.Kind {
			case transcript.KindCompaction:
				var compaction transcript.Compaction

				if err := json.Unmarshal(entry.Payload, &compaction); err != nil {
					return err
				}

				outcome.Compacted++
				outcome.Input += compaction.Usage.Input + compaction.Usage.CacheWrite
				outcome.Cached += compaction.Usage.CacheRead
				outcome.Output += compaction.Usage.Output

			case transcript.KindAssistant:
				var assistant transcript.Assistant

				if err := json.Unmarshal(entry.Payload, &assistant); err != nil {
					return err
				}

				outcome.Turns++
				outcome.Input += assistant.Usage.Input + assistant.Usage.CacheWrite
				outcome.Cached += assistant.Usage.CacheRead
				outcome.Output += assistant.Usage.Output

				for _, block := range assistant.Message.Content {
					if block.Type == protocol.BlockToolCall {
						outcome.ToolCalls++
					}
				}
			}
		}
	}

	return nil
}

// selftest checks every task without a model: its check must fail on the
// untouched project and pass once the reference solution is applied.
// Otherwise a result would say nothing about the agent.
func selftest(tasks []string) error {
	scratch, err := os.MkdirTemp("", "rumbo-evals-selftest-")

	if err != nil {
		return err
	}

	defer os.RemoveAll(scratch)

	failed := 0

	for _, task := range tasks {
		name := filepath.Base(task)
		problem, err := selftestTask(task, scratch)

		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}

		if problem != "" {
			failed++
			fmt.Printf("%-22s BROKEN  %s\n", name, problem)
			continue
		}

		fmt.Printf("%-22s ok\n", name)
	}

	if failed > 0 {
		return fmt.Errorf("%d task(s) are broken", failed)
	}

	return nil
}

func selftestTask(task, scratch string) (string, error) {
	workdir, err := os.MkdirTemp(scratch, filepath.Base(task)+"-")

	if err != nil {
		return "", err
	}

	repo, err := prepare(task, workdir)

	if err != nil {
		return "", err
	}

	env := environment(task, workdir)

	if err := os.WriteFile(filepath.Join(workdir, "output.txt"), nil, 0o644); err != nil {
		return "", err
	}

	if _, err := check(task, repo, env); err == nil {
		return "the check passes before any work is done", nil
	}

	solution := exec.Command("bash", filepath.Join(task, "solution.sh"))
	solution.Dir = repo
	solution.Env = env

	if output, err := solution.CombinedOutput(); err != nil {
		return "", fmt.Errorf("solution.sh: %s", output)
	}

	if output, err := check(task, repo, env); err != nil {
		return "the check rejects the reference solution: " + strings.TrimSpace(output), nil
	}

	return "", nil
}

// record appends one result to the results file, if one was asked for.
func record(path string, outcome result) error {
	if path == "" {
		return nil
	}

	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)

	if err != nil {
		return err
	}

	defer file.Close()

	return json.NewEncoder(file).Encode(outcome)
}

func report(results []result) {
	table := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', tabwriter.AlignRight)

	fmt.Fprintln(table, "task\tresult\texit\tseconds\tturns\ttools\tcompact\tinput\tcached\toutput\t")

	passed := 0
	var total result

	for _, outcome := range results {
		if outcome.Passed {
			passed++
		}

		total.Seconds += outcome.Seconds
		total.Turns += outcome.Turns
		total.ToolCalls += outcome.ToolCalls
		total.Compacted += outcome.Compacted
		total.Input += outcome.Input
		total.Cached += outcome.Cached
		total.Output += outcome.Output

		fmt.Fprintf(table, "%s\t%s\t%d\t%.0f\t%d\t%d\t%d\t%d\t%d\t%d\t\n",
			outcome.Task, verdict(outcome.Passed), outcome.ExitCode, outcome.Seconds,
			outcome.Turns, outcome.ToolCalls, outcome.Compacted, outcome.Input, outcome.Cached, outcome.Output)
	}

	fmt.Fprintf(table, "total\t%d/%d\t\t%.0f\t%d\t%d\t%d\t%d\t%d\t%d\t\n",
		passed, len(results), total.Seconds, total.Turns, total.ToolCalls, total.Compacted, total.Input, total.Cached, total.Output)

	table.Flush()

	for _, outcome := range results {
		if !outcome.Passed {
			fmt.Printf("\n%s failed. Kept in %s\n%s\n", outcome.Task, outcome.Workdir, outcome.Check)
		}
	}
}

func verdict(passed bool) string {
	if passed {
		return "pass"
	}

	return "FAIL"
}

func exitCode(err error) int {
	var exit *exec.ExitError

	if errors.As(err, &exit) {
		return exit.ExitCode()
	}

	if err != nil {
		return -1
	}

	return 0
}

func exists(path string) bool {
	_, err := os.Stat(path)

	return err == nil
}
