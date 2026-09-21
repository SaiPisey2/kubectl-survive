// Package exporter serves the survivability engine's findings as Prometheus
// metrics, on an interval, so decay a command would never be run in time to
// catch -- a node drained, nothing changed in git, one zone now carries
// everything -- shows up as a scrape instead of a report someone has to
// remember to ask for (spec §9).
//
// It is a thin wrapper, same as every other surface: all of the actual
// survivability logic lives in internal/survive, internal/pdbcheck and
// internal/draincheck. This package only turns their output into metrics and
// keeps re-running them off the HTTP goroutine.
package exporter

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/SaiPisey2/kubectl-survive/internal/draincheck"
	"github.com/SaiPisey2/kubectl-survive/internal/sched"
	"github.com/SaiPisey2/kubectl-survive/internal/snapshot"
	"github.com/SaiPisey2/kubectl-survive/internal/survive"
)

// Fetcher builds one fresh snapshot and its version gate, exactly like
// cli.Options.Run does before calling runWithSnapshot. It is the one thing
// this package cannot do without a real cluster, so it is injected: every
// other behaviour here is unit-testable with a snapshot built by hand.
type Fetcher func(ctx context.Context) (*snapshot.Snapshot, sched.VersionGate, error)

// schedulerBuilder and deadlockDetector match sched.New and draincheck.Detect
// respectively. They are fields, not direct calls, purely so tests can
// exercise the error path (a scheduler that fails to build, or a detector
// that errors) without needing to actually break the real scheduler.
type schedulerBuilder func(ctx context.Context, s *snapshot.Snapshot) (*sched.Scheduler, error)
type deadlockDetector func(ctx context.Context, s *sched.Scheduler, snap *snapshot.Snapshot, domainKey string) ([]draincheck.Finding, error)

// Exporter periodically re-analyses a cluster's survivability and exposes the
// result on its own Prometheus registry (never the global default one, so
// more than one Exporter -- e.g. one per test -- can exist in a process at
// once without a duplicate-registration panic).
type Exporter struct {
	domainKey string
	registry  *prometheus.Registry

	newScheduler schedulerBuilder
	detect       deadlockDetector

	workloadsLost     *prometheus.GaugeVec
	workloadsDegraded *prometheus.GaugeVec
	workloadSurvives  *prometheus.GaugeVec
	pdbUnsatisfiable  *prometheus.GaugeVec
	drainDeadlock     *prometheus.GaugeVec
	drainCheckEnabled prometheus.Gauge
	unlabelledNodes   prometheus.Gauge

	scrapeSuccess        prometheus.Gauge
	scrapeErrorsTotal    prometheus.Counter
	lastSuccessTimestamp prometheus.Gauge
	lastDuration         prometheus.Gauge
}

// New builds an Exporter for the given failure-domain label, with its own
// registry and every metric registered up front (so a scrape before the
// first analysis still returns valid, if empty/zeroed, exposition text
// instead of a registry with nothing in it).
func New(domainKey string) *Exporter {
	e := &Exporter{
		domainKey:    domainKey,
		registry:     prometheus.NewRegistry(),
		newScheduler: sched.New,
		detect:       draincheck.Detect,

		workloadsLost: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "survive_workloads_lost",
			Help: "Number of workloads that lose all availability if this domain is lost.",
		}, []string{"domain"}),
		workloadsDegraded: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "survive_workloads_degraded",
			Help: "Number of workloads that fall below their PodDisruptionBudget if this domain is lost.",
		}, []string{"domain"}),
		// workload identifies the owning object as "<namespace>/<name>"
		// (workload.Ref.String()), not the bare name spec §8.4's example
		// shows: a bare name collides across namespaces, and namespace as a
		// second label multiplies cardinality for no benefit over folding it
		// into one value. See the package doc comment on cardinality below.
		workloadSurvives: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "survive_workload_survives",
			Help: "1 if the workload keeps availability after losing this domain, 0 otherwise (includes degraded and unknown).",
		}, []string{"workload", "domain"}),
		// pdbcheck.Finding and draincheck.Finding are keyed by the
		// PodDisruptionBudget, not by the workload it protects -- a PDB's
		// selector can span more than one owner, and neither package
		// resolves it back to a single workload.Ref. "pdb" as
		// "<namespace>/<name>" is what the data actually is; in the common
		// case the PDB is named after its workload, so this still answers
		// spec §8.4's example (session-store) directly.
		pdbUnsatisfiable: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "survive_pdb_unsatisfiable",
			Help: "1 if this PodDisruptionBudget can never permit an eviction (pdbcheck).",
		}, []string{"pdb"}),
		drainDeadlock: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "survive_drain_deadlock",
			Help: "1 if draining this domain deadlocks: the budget is satisfiable in isolation, but the replacement pod cannot schedule outside the domain (draincheck).",
		}, []string{"pdb", "domain"}),
		drainCheckEnabled: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "survive_drain_deadlock_check_enabled",
			Help: "1 if the scheduler-backed drain-deadlock check ran on the last analysis, 0 if it was skipped by the version gate (sched.Gate). A healthy 0 here on every domain must never be read the same as this being 0.",
		}),
		unlabelledNodes: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "survive_unlabelled_nodes",
			Help: "Number of nodes with no value for the domain-key label; workloads placed on them are reported as unknown, never as surviving.",
		}),
		scrapeSuccess: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "survive_scrape_success",
			Help: "1 if the most recent analysis cycle completed successfully, 0 if it errored. Distinguishes a failed analysis from a healthy cluster with nothing wrong -- both would otherwise report zero findings.",
		}),
		scrapeErrorsTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "survive_scrape_errors_total",
			Help: "Cumulative count of analysis cycles that failed to complete (snapshot fetch, scheduler build, or drain-deadlock detection).",
		}),
		lastSuccessTimestamp: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "survive_last_success_timestamp_seconds",
			Help: "Unix time of the last analysis cycle that completed successfully. Compare against time() to detect a stale exporter even while every other metric still reports the last good reading.",
		}),
		lastDuration: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "survive_last_analysis_duration_seconds",
			Help: "Wall-clock duration of the last analysis cycle (snapshot fetch through metric update), successful or not.",
		}),
	}

	e.registry.MustRegister(
		e.workloadsLost, e.workloadsDegraded, e.workloadSurvives,
		e.pdbUnsatisfiable, e.drainDeadlock, e.drainCheckEnabled, e.unlabelledNodes,
		e.scrapeSuccess, e.scrapeErrorsTotal, e.lastSuccessTimestamp, e.lastDuration,
	)
	return e
}

// Handler returns the /metrics HTTP handler. It only ever reads whatever the
// background loop most recently wrote into the registered metrics -- it
// never triggers or waits on an analysis cycle itself, which is what keeps a
// slow (or hung) analysis from stalling Prometheus.
func (e *Exporter) Handler() http.Handler {
	return promhttp.HandlerFor(e.registry, promhttp.HandlerOpts{Registry: e.registry})
}

// Run re-analyses on interval until ctx is done, running the first pass
// immediately rather than waiting a full interval for the first data point.
// Each pass gets its own timeout derived from ctx, so one slow or hanging
// snapshot fetch cannot delay -- let alone block -- the next tick indefinitely;
// passes never overlap, because the next tick is scheduled only after the
// current one (successful or not) returns.
func (e *Exporter) Run(ctx context.Context, fetch Fetcher, interval, timeout time.Duration) error {
	// runOnce's own error is intentionally swallowed here: it already
	// recorded the failure into scrape_success / scrape_errors_total, which
	// is the durable signal. Run keeps looping until ctx says stop; a single
	// bad cycle must not take the exporter down.
	_ = e.runOnce(ctx, fetch, timeout)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
			_ = e.runOnce(ctx, fetch, timeout)
		}
	}
}

// runOnce is Run's single-cycle core, exported to this package's tests so
// they can drive it directly with a synthetic Fetcher and inspect the result
// of exactly one cycle without waiting on a ticker.
func (e *Exporter) runOnce(ctx context.Context, fetch Fetcher, timeout time.Duration) error {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()
	snap, gate, err := fetch(cctx)
	if err != nil {
		e.recordFailure()
		return fmt.Errorf("exporter: fetch snapshot: %w", err)
	}

	report := survive.Analyze(snap, e.domainKey)

	var deadlocks []draincheck.Finding
	if gate.Enabled {
		sc, err := e.newScheduler(cctx, snap)
		if err != nil {
			e.recordFailure()
			return fmt.Errorf("exporter: build scheduler: %w", err)
		}
		deadlocks, err = e.detect(cctx, sc, snap, e.domainKey)
		if err != nil {
			e.recordFailure()
			return fmt.Errorf("exporter: drain-deadlock detection: %w", err)
		}
	}

	e.record(report, gate, deadlocks)
	e.scrapeSuccess.Set(1)
	e.lastSuccessTimestamp.Set(float64(time.Now().Unix()))
	e.lastDuration.Set(time.Since(start).Seconds())
	return nil
}

// recordFailure marks the cycle unhealthy without touching any of the
// findings gauges: the last good reading stays exactly as it was, because a
// stale-but-real reading is more useful -- and less misleading -- than
// wiping it to zero, which would read identically to "nothing is wrong".
// survive_last_success_timestamp_seconds is what tells an operator the
// reading is stale; the findings themselves are not the staleness signal.
func (e *Exporter) recordFailure() {
	e.scrapeSuccess.Set(0)
	e.scrapeErrorsTotal.Inc()
}

// record replaces every findings gauge with exactly this cycle's data.
// Reset-then-set (rather than only ever calling Set) is what keeps
// cardinality bounded to the current cluster shape: a workload or PDB that
// no longer exists loses its series on the very next cycle instead of
// accumulating forever.
func (e *Exporter) record(report *survive.Report, gate sched.VersionGate, deadlocks []draincheck.Finding) {
	e.workloadsLost.Reset()
	e.workloadsDegraded.Reset()
	e.workloadSurvives.Reset()
	for _, d := range report.Domains {
		e.workloadsLost.WithLabelValues(d.Domain).Set(float64(d.Lost))
		e.workloadsDegraded.WithLabelValues(d.Domain).Set(float64(d.Degraded))
		for _, v := range d.Verdicts {
			survives := 0.0
			if v.Outcome == survive.OutcomeSurvives {
				survives = 1.0
			}
			e.workloadSurvives.WithLabelValues(v.Workload.String(), d.Domain).Set(survives)
		}
	}

	e.pdbUnsatisfiable.Reset()
	for _, f := range report.PDBFindings {
		if f.Block == "" {
			continue
		}
		e.pdbUnsatisfiable.WithLabelValues(f.Namespace + "/" + f.PDB).Set(1)
	}

	e.drainDeadlock.Reset()
	for _, f := range deadlocks {
		e.drainDeadlock.WithLabelValues(f.Namespace+"/"+f.PDB, f.Domain).Set(1)
	}

	if gate.Enabled {
		e.drainCheckEnabled.Set(1)
	} else {
		e.drainCheckEnabled.Set(0)
	}

	e.unlabelledNodes.Set(float64(len(report.UnlabelledNodes)))
}
