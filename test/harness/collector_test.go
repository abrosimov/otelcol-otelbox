package harness

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

type collectorSpec struct {
	name     string
	stateDir string
	env      map[string]string
	configs  []string
}

type collector struct {
	name    string
	logPath string
	cmd     *exec.Cmd
	log     *os.File
	exited  chan struct{}
	once    sync.Once
}

func startCollector(t *testing.T, spec collectorSpec) *collector {
	t.Helper()

	logPath := filepath.Join(spec.stateDir, spec.name+".log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatalf("could not open a log file for the %s collector: %v", spec.name, err)
	}

	args := make([]string, 0, 2*len(spec.configs))
	for _, config := range spec.configs {
		args = append(args, "--config", config)
	}

	cmd := exec.Command(binaryPath, args...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Env = collectorEnvironment(t, spec.env)

	if err := cmd.Start(); err != nil {
		logFile.Close()
		t.Fatalf("could not start the %s collector: %v", spec.name, err)
	}

	c := &collector{name: spec.name, logPath: logPath, cmd: cmd, log: logFile, exited: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		close(c.exited)
	}()
	t.Cleanup(func() { c.stop(t) })

	t.Logf("%s: started (pid %d), logging to %s", c.name, cmd.Process.Pid, logPath)
	return c
}

func collectorEnvironment(t *testing.T, overrides map[string]string) []string {
	t.Helper()

	env := make([]string, 0, len(os.Environ())+len(overrides))
	stripped := make([]string, 0)
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "OTELBOX_") {
			stripped = append(stripped, key)
			continue
		}
		env = append(env, entry)
	}
	for key, value := range overrides {
		env = append(env, key+"="+value)
	}
	if len(stripped) > 0 {
		sort.Strings(stripped)
		t.Logf("stripped ambient collector settings: %s", strings.Join(stripped, ", "))
	}
	return env
}

// SIGTERM first so the collector shuts its pipelines down and flushes; SIGKILL
// only if it will not go.
func (c *collector) stop(t *testing.T) {
	t.Helper()

	c.once.Do(func() {
		defer c.log.Close()

		if c.alive() {
			_ = c.cmd.Process.Signal(syscall.SIGTERM)
		}
		timer := time.NewTimer(stopTimeout)
		defer timer.Stop()
		select {
		case <-c.exited:
		case <-timer.C:
			t.Logf("%s did not stop on SIGTERM, sending SIGKILL", c.name)
			_ = c.cmd.Process.Kill()
			<-c.exited
		}
	})
}

// The durability assertion needs process death without the pipeline shutdown
// that would flush an in-memory processor batch and mask the loss it guards.
func (c *collector) crash(t *testing.T) {
	t.Helper()

	c.once.Do(func() {
		defer c.log.Close()

		if c.alive() {
			if err := c.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
				t.Fatalf("could not kill the %s collector: %v", c.name, err)
			}
		}
		<-c.exited
	})
}

func (c *collector) alive() bool {
	select {
	case <-c.exited:
		return false
	default:
		return true
	}
}

func (c *collector) logTail(lines int) string {
	raw, err := os.ReadFile(c.logPath)
	if err != nil {
		return fmt.Sprintf("(no %s log: %v)", c.name, err)
	}
	all := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(all) > lines {
		all = all[len(all)-lines:]
	}
	return strings.Join(all, "\n")
}

func (c *collector) firstLogLine(needles ...string) string {
	raw, err := os.ReadFile(c.logPath)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(raw), "\n") {
		lower := strings.ToLower(line)
		for _, needle := range needles {
			if strings.Contains(lower, strings.ToLower(needle)) {
				return line
			}
		}
	}
	return ""
}

// grpc-go treats a rejected certificate as an ordinary export failure and keeps
// retrying, so unlike a bad config it never kills the process — without this a
// certificate fault reaches the output as a bare timeout with no cause named.
func (c *collector) tlsFault() string {
	return c.firstLogLine("authentication handshake failed", "x509:", "remote error: tls:")
}

type diagnosticSection struct {
	title string
	body  string
}

// CI logs are the only debugging surface this harness has: nobody can attach to
// a runner afterwards, and the temporary state is gone by then. Callers run
// this as a deferred call rather than a t.Cleanup so that it fires while every
// collector is still alive — the exporter counters live in the collector's own
// process, and stopping it takes the one number worth having with it.
func dumpDiagnostics(t *testing.T, sections []diagnosticSection, collectors ...*collector) {
	t.Helper()

	var report strings.Builder
	report.WriteString("\n===== diagnostics =====\n")
	for _, c := range collectors {
		fmt.Fprintf(&report, "--- %s.log (last 60 lines) ---\n%s\n", c.name, c.logTail(60))
	}
	for _, section := range sections {
		fmt.Fprintf(&report, "--- %s ---\n%s\n", section.title, section.body)
	}
	report.WriteString("=======================")
	t.Log(report.String())
}

// Bounded poll. Watches the collector as well as the predicate, so a process
// that died on a bad config is reported immediately, with its last log lines,
// instead of after the full timeout with nothing.
func poll(timeout time.Duration, watch *collector, what string, ready func() bool) error {
	deadline := time.Now().Add(timeout)
	for {
		if ready() {
			return nil
		}
		if watch != nil && !watch.alive() {
			return fmt.Errorf("the %s collector exited while waiting for %s; its last log lines were:\n%s",
				watch.name, what, watch.logTail(20))
		}
		if time.Now().After(deadline) {
			if watch != nil {
				if line := watch.tlsFault(); line != "" {
					return fmt.Errorf("timed out after %s waiting for %s; the %s collector's log names a TLS fault, which is the likelier cause:\n%s",
						timeout, what, watch.name, line)
				}
			}
			return fmt.Errorf("timed out after %s waiting for %s", timeout, what)
		}
		time.Sleep(pollInterval)
	}
}

// The mirror of poll, for the assertions whose subject is that nothing happens.
// A bounded poll returning false proves only that the thing had not arrived
// yet; this holds the window open for all of it, and reports the collector
// dying under it as the separate fact that it is.
func staysFalse(timeout time.Duration, watch *collector, what string, never func() bool) error {
	started := time.Now()
	deadline := started.Add(timeout)
	for time.Now().Before(deadline) {
		if never() {
			return fmt.Errorf("%s happened %s into a %s window", what, time.Since(started).Round(time.Second), timeout)
		}
		if watch != nil && !watch.alive() {
			return fmt.Errorf("the %s collector exited during the window, so nothing can be concluded from its silence; its last log lines were:\n%s",
				watch.name, watch.logTail(20))
		}
		time.Sleep(pollInterval)
	}
	return nil
}
