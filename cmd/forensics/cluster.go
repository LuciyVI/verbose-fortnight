package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func buildClusters(events []Event, symbol string) (map[string]*TradeCluster, []Event) {
	clusters := make(map[string]*TradeCluster, 128)
	positionEvents := make([]Event, 0, 128)

	for _, ev := range events {
		if symbol != "" && ev.Symbol != "" && !strings.EqualFold(ev.Symbol, symbol) {
			continue
		}
		switch ev.Type {
		case eventPositionListResp, eventPositionWSUpdate:
			positionEvents = append(positionEvents, ev)
		}

		key := firstNonEmpty(ev.TraceKey, ev.LifecycleID, ev.TradeID, ev.OrderID, ev.ExecID)
		if key == "" {
			continue
		}
		c := clusters[key]
		if c == nil {
			c = &TradeCluster{
				Key:         key,
				TraceKey:    ev.TraceKey,
				LifecycleID: ev.LifecycleID,
				TradeID:     ev.TradeID,
				Symbol:      ev.Symbol,
				StartTS:     ev.TS,
				EndTS:       ev.TS,
				Events:      make([]Event, 0, 16),
			}
			clusters[key] = c
		}
		appendClusterEvent(c, ev)
	}

	for _, c := range clusters {
		attachPositionEvents(c, positionEvents)
		finalizeCluster(c)
	}

	return clusters, positionEvents
}

func appendClusterEvent(c *TradeCluster, ev Event) {
	if c == nil {
		return
	}
	if c.Symbol == "" {
		c.Symbol = ev.Symbol
	}
	if c.TraceKey == "" {
		c.TraceKey = ev.TraceKey
	}
	if c.LifecycleID == "" {
		c.LifecycleID = ev.LifecycleID
	}
	if c.TradeID == "" {
		c.TradeID = ev.TradeID
	}
	if ev.TS.Before(c.StartTS) || c.StartTS.IsZero() {
		c.StartTS = ev.TS
	}
	if ev.TS.After(c.EndTS) || c.EndTS.IsZero() {
		c.EndTS = ev.TS
	}

	c.Events = append(c.Events, ev)
	c.OrderIDs = appendUnique(c.OrderIDs, ev.OrderID)
	c.OrderLinkIDs = appendUnique(c.OrderLinkIDs, ev.OrderLinkID)
	c.ExecIDs = appendUnique(c.ExecIDs, ev.ExecID)

	switch ev.Type {
	case eventExecutionFill:
		c.ExecutionFills = append(c.ExecutionFills, ev)
		closedQty := ev.ClosedSize
		if closedQty <= 0 && ev.ReduceOnly {
			closedQty = ev.Qty
		}
		if !ev.ReduceOnly && ev.Qty > 0 {
			c.QtyOpened += ev.Qty
			c.EntryVWAP += ev.Price * ev.Qty
			c.FeeOpenAlloc += math.Abs(ev.ExecFee)
		}
		if closedQty > 0 {
			c.QtyClosedTotal += closedQty
			c.ExitVWAP += ev.Price * closedQty
			c.FeeClose += math.Abs(ev.ExecFee)
		}
		c.FeeTotal += math.Abs(ev.ExecFee)
	case eventTradeCloseSummary:
		c.HasSummary = true
		c.PositionSide = firstNonEmpty(ev.PositionSide, ev.Side, c.PositionSide)
		c.QtyOpened = firstNonZero(ev.QtyOpened, c.QtyOpened)
		c.QtyClosedTotal = firstNonZero(ev.QtyClosedTotal, c.QtyClosedTotal)
		c.EntryVWAP = firstNonZero(ev.EntryVWAP, c.EntryVWAP)
		c.ExitVWAP = firstNonZero(ev.ExitVWAP, c.ExitVWAP)
		c.GrossCalc = ev.GrossCalc
		c.GrossSource = ev.GrossSource
		c.FeeOpenAlloc = firstNonZero(ev.FeeOpenAlloc, c.FeeOpenAlloc)
		c.FeeClose = firstNonZero(ev.FeeClose, c.FeeClose)
		c.FeeTotal = firstNonZero(ev.FeeTotal, c.FeeTotal)
		c.FundingSigned = ev.FundingSigned
		c.RealisedDelta = ev.RealisedDelta
		c.NetCalc = ev.NetCalc
		c.NetExchange = ev.NetExchange
		c.NetSource = ev.NetSource
		c.CloseReason = ev.CloseReason
	}
}

func attachPositionEvents(c *TradeCluster, positions []Event) {
	if c == nil || len(positions) == 0 {
		return
	}
	ref := c.EndTS
	if ref.IsZero() {
		ref = c.StartTS
	}
	for _, p := range positions {
		if c.Symbol != "" && p.Symbol != "" && !strings.EqualFold(c.Symbol, p.Symbol) {
			continue
		}
		if absDuration(p.TS.Sub(ref)) <= defaultPositionAttachGap {
			c.PositionEvents = append(c.PositionEvents, p)
		}
	}
	if len(c.PositionEvents) >= 2 {
		first := c.PositionEvents[0]
		last := c.PositionEvents[len(c.PositionEvents)-1]
		c.RealisedDelta = last.CumRealisedPnl - first.CumRealisedPnl
	}
}

func finalizeCluster(c *TradeCluster) {
	if c == nil {
		return
	}
	sort.SliceStable(c.Events, func(i, j int) bool { return c.Events[i].TS.Before(c.Events[j].TS) })
	sort.SliceStable(c.ExecutionFills, func(i, j int) bool { return c.ExecutionFills[i].TS.Before(c.ExecutionFills[j].TS) })
	sort.SliceStable(c.PositionEvents, func(i, j int) bool { return c.PositionEvents[i].TS.Before(c.PositionEvents[j].TS) })

	if !c.HasSummary {
		if c.QtyOpened > 0 && c.EntryVWAP > 0 {
			c.EntryVWAP = c.EntryVWAP / c.QtyOpened
		}
		if c.QtyClosedTotal > 0 && c.ExitVWAP > 0 {
			c.ExitVWAP = c.ExitVWAP / c.QtyClosedTotal
		}
		if c.FeeTotal == 0 {
			c.FeeTotal = math.Abs(c.FeeOpenAlloc) + math.Abs(c.FeeClose)
		}
	}
	if c.NetCalc == 0 && (c.GrossCalc != 0 || c.FeeTotal != 0 || c.FundingSigned != 0) {
		c.NetCalc = c.GrossCalc - c.FeeTotal + c.FundingSigned
	}
	if c.NetSource == "" {
		if c.NetExchange != nil {
			c.NetSource = "exchange"
		} else {
			c.NetSource = "calculated"
		}
	}
}

func matchReports(reports []ReportTrade, clusters map[string]*TradeCluster, positionEvents []Event) []MatchResult {
	out := make([]MatchResult, 0, len(reports))
	clusterList := make([]*TradeCluster, 0, len(clusters))
	for _, c := range clusters {
		clusterList = append(clusterList, c)
	}
	for _, rep := range reports {
		match := MatchResult{Report: rep}
		c, score := findBestCluster(rep, clusterList, defaultMatchWindow)
		if c == nil {
			c, score = findBestCluster(rep, clusterList, fallbackMatchWindow)
		}
		if c == nil {
			c = buildDeltaOnlyCluster(rep, positionEvents)
			if c != nil {
				score = 0.6
			}
		}
		if c == nil {
			match.Matched = false
			match.Confidence = 0
			match.Tier = "unmatched"
			match.Reason = "no cluster in time window"
			out = append(out, match)
			continue
		}
		match.Matched = true
		match.ClusterKey = c.Key
		match.TraceKey = c.TraceKey
		match.LifecycleID = c.LifecycleID
		match.TradeID = c.TradeID
		match.MatchedStart = c.StartTS
		match.MatchedEnd = c.EndTS
		match.PositionSide = c.PositionSide
		match.CloseReason = c.CloseReason
		match.QtyLog = firstNonZero(c.QtyClosedTotal, c.QtyOpened)
		match.EntryLog = c.EntryVWAP
		match.ExitLog = c.ExitVWAP
		match.FeeOpenAlloc = c.FeeOpenAlloc
		match.FeeClose = c.FeeClose
		match.FeeTotal = c.FeeTotal
		match.FundingSigned = c.FundingSigned
		match.GrossCalc = c.GrossCalc
		match.GrossSource = c.GrossSource
		match.NetCalc = c.NetCalc
		match.NetExchange = c.NetExchange
		match.NetSource = c.NetSource
		match.Tier, match.Confidence = confidenceTier(c, score)
		match.Reason = confidenceReason(c)
		out = append(out, match)
	}
	return out
}

func findBestCluster(rep ReportTrade, clusters []*TradeCluster, window time.Duration) (*TradeCluster, float64) {
	var best *TradeCluster
	bestScore := -1.0
	for _, c := range clusters {
		if rep.Symbol != "" && c.Symbol != "" && !strings.EqualFold(rep.Symbol, c.Symbol) {
			continue
		}
		refTS := c.EndTS
		if !c.HasSummary && !c.StartTS.IsZero() {
			refTS = c.StartTS.Add(c.EndTS.Sub(c.StartTS) / 2)
		}
		td := absDuration(refTS.Sub(rep.Time))
		if td > window {
			continue
		}
		score := 0.2
		if c.HasSummary {
			score += 0.2
		}
		if c.TraceKey != "" || c.LifecycleID != "" {
			score += 0.15
		}
		if len(c.ExecIDs) > 0 {
			score += 0.15
		}
		score += 0.15 * (1.0 - minFloat(1.0, float64(td)/float64(window)))
		qtyLog := firstNonZero(c.QtyClosedTotal, c.QtyOpened)
		if rep.Qty > 0 && qtyLog > 0 {
			score += 0.15 * (1.0 - minFloat(1.0, math.Abs(rep.Qty-qtyLog)/rep.Qty))
		}
		if rep.Exit > 0 && c.ExitVWAP > 0 {
			score += 0.1 * (1.0 - minFloat(1.0, math.Abs(rep.Exit-c.ExitVWAP)/5.0))
		}
		if rep.Entry > 0 && c.EntryVWAP > 0 {
			score += 0.1 * (1.0 - minFloat(1.0, math.Abs(rep.Entry-c.EntryVWAP)/5.0))
		}
		if score > bestScore {
			best = c
			bestScore = score
		}
	}
	if best == nil {
		return nil, 0
	}
	if bestScore < 0 {
		bestScore = 0
	}
	if bestScore > 1 {
		bestScore = 1
	}
	return best, bestScore
}

func buildDeltaOnlyCluster(rep ReportTrade, positionEvents []Event) *TradeCluster {
	cands := make([]Event, 0, 4)
	for _, ev := range positionEvents {
		if rep.Symbol != "" && ev.Symbol != "" && !strings.EqualFold(rep.Symbol, ev.Symbol) {
			continue
		}
		if absDuration(ev.TS.Sub(rep.Time)) <= fallbackMatchWindow {
			cands = append(cands, ev)
		}
	}
	if len(cands) == 0 {
		return nil
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].TS.Before(cands[j].TS) })
	c := &TradeCluster{
		Key:            fmt.Sprintf("delta:%d", rep.Time.UnixMilli()),
		Symbol:         rep.Symbol,
		StartTS:        cands[0].TS,
		EndTS:          cands[len(cands)-1].TS,
		PositionEvents: cands,
		HasSummary:     false,
		MatchedBy:      eventPositionDeltaOnly,
	}
	if len(cands) >= 2 {
		c.RealisedDelta = cands[len(cands)-1].CumRealisedPnl - cands[0].CumRealisedPnl
	}
	c.GrossCalc = rep.Gross
	c.FeeTotal = rep.Fee
	c.NetCalc = rep.Gross - rep.Fee + rep.FundingSigned
	return c
}

func confidenceTier(c *TradeCluster, score float64) (string, float64) {
	if c != nil && (c.TraceKey != "" || c.LifecycleID != "") && len(c.ExecIDs) > 0 {
		return "tier1", clamp(0.95+0.05*score, 0.95, 1.0)
	}
	if c != nil && (c.TraceKey != "" || c.LifecycleID != "") && (c.HasSummary || len(c.PositionEvents) > 0) {
		return "tier2", clamp(0.75+0.15*score, 0.75, 0.9)
	}
	return "tier3", clamp(0.5+0.2*score, 0.5, 0.7)
}

func confidenceReason(c *TradeCluster) string {
	if c == nil {
		return "no cluster"
	}
	if (c.TraceKey != "" || c.LifecycleID != "") && len(c.ExecIDs) > 0 {
		return "lifecycle + direct executions"
	}
	if c.HasSummary || len(c.PositionEvents) > 0 {
		return "lifecycle/summary + position delta"
	}
	return "position delta only"
}

func writeMatchTable(path string, matches []MatchResult) error {
	var b strings.Builder
	b.WriteString("| # | report_time_utc | traceKey | lifecycleId | matched_range_utc | side/qty | entry_report/log | exit_report/log | fee_open_alloc | fee_close | fee_total | funding | net_source | close_reason | confidence | tier |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|---:|---:|---:|---:|---|---|---:|---|\n")
	for _, m := range matches {
		if !m.Matched {
			fmt.Fprintf(&b, "| %d | %s | - | - | - | %s/%.6f | %.2f/- | %.2f/- | - | - | - | - | - | unmatched | %.2f | unmatched |\n",
				m.Report.Index, m.Report.Time.Format(time.RFC3339), normalizeSideLabel(m.Report), m.Report.Qty, m.Report.Entry, m.Report.Exit, m.Confidence)
			continue
		}
		rangeStr := fmt.Sprintf("%s..%s", m.MatchedStart.Format(time.RFC3339), m.MatchedEnd.Format(time.RFC3339))
		fmt.Fprintf(&b, "| %d | %s | %s | %s | %s | %s/%.6f | %.2f/%.2f | %.2f/%.2f | %.6f | %.6f | %.6f | %.6f | %s | %s | %.2f | %s |\n",
			m.Report.Index,
			m.Report.Time.Format(time.RFC3339),
			nullDash(m.TraceKey),
			nullDash(m.LifecycleID),
			rangeStr,
			normalizeSideLabel(m.Report), m.Report.Qty,
			m.Report.Entry, m.EntryLog,
			m.Report.Exit, m.ExitLog,
			m.FeeOpenAlloc, m.FeeClose, m.FeeTotal, m.FundingSigned,
			nullDash(m.NetSource), nullDash(m.CloseReason), m.Confidence, m.Tier)
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

func writeMismatchReport(path string, matches []MatchResult) error {
	type row struct {
		Match         MatchResult
		ExpectedNet   float64
		DeltaExpected float64
		DeltaCalc     float64
		DeltaExchange *float64
		Class         string
	}
	rows := make([]row, 0, len(matches))
	for _, m := range matches {
		if !m.Matched {
			continue
		}
		exp := m.Report.Gross - m.Report.Fee + m.Report.FundingSigned
		deltaExp := m.Report.Net - exp
		deltaCalc := m.Report.Net - m.NetCalc
		var deltaEx *float64
		if m.NetExchange != nil {
			v := m.Report.Net - *m.NetExchange
			deltaEx = &v
		}
		rows = append(rows, row{
			Match:         m,
			ExpectedNet:   exp,
			DeltaExpected: deltaExp,
			DeltaCalc:     deltaCalc,
			DeltaExchange: deltaEx,
			Class:         classifyMismatch(m, deltaExp, deltaCalc),
		})
	}
	sort.SliceStable(rows, func(i, j int) bool {
		return math.Abs(rows[i].DeltaExpected) > math.Abs(rows[j].DeltaExpected)
	})

	var b strings.Builder
	b.WriteString("## PnL Mismatch\n\n")
	b.WriteString("| # | report_time_utc | gross_report | fee_report | funding_report | net_report | net_expected(gross-fee+funding) | delta_expected | net_calc(cluster) | delta_vs_calc | class |\n")
	b.WriteString("|---|---|---:|---:|---:|---:|---:|---:|---:|---:|---|\n")
	for _, r := range rows {
		fmt.Fprintf(&b, "| %d | %s | %.6f | %.6f | %.6f | %.6f | %.6f | %.6f | %.6f | %.6f | %s |\n",
			r.Match.Report.Index,
			r.Match.Report.Time.Format(time.RFC3339),
			r.Match.Report.Gross,
			r.Match.Report.Fee,
			r.Match.Report.FundingSigned,
			r.Match.Report.Net,
			r.ExpectedNet,
			r.DeltaExpected,
			r.Match.NetCalc,
			r.DeltaCalc,
			r.Class,
		)
	}
	b.WriteString("\n### Top 3 Deltas\n\n")
	top := 3
	if len(rows) < top {
		top = len(rows)
	}
	for i := 0; i < top; i++ {
		r := rows[i]
		fmt.Fprintf(&b, "#### Trade #%d (%s)\n", r.Match.Report.Index, r.Match.Report.Time.Format(time.RFC3339))
		fmt.Fprintf(&b, "- traceKey: `%s`\n", nullDash(r.Match.TraceKey))
		fmt.Fprintf(&b, "- net_report=%.6f expected=%.6f delta=%.6f\n", r.Match.Report.Net, r.ExpectedNet, r.DeltaExpected)
		fmt.Fprintf(&b, "- net_calc=%.6f delta_vs_calc=%.6f net_source=%s\n", r.Match.NetCalc, r.DeltaCalc, nullDash(r.Match.NetSource))
		if r.Match.NetExchange != nil {
			fmt.Fprintf(&b, "- net_exchange=%.6f\n", *r.Match.NetExchange)
		}
		fmt.Fprintf(&b, "- classification: `%s`\n\n", r.Class)
	}

	return os.WriteFile(path, []byte(b.String()), 0o644)
}

func classifyMismatch(m MatchResult, deltaExpected, deltaCalc float64) string {
	if sign(m.Report.Gross) != 0 && sign(m.GrossCalc) != 0 && sign(m.Report.Gross) != sign(m.GrossCalc) {
		return "gross_sign_mismatch"
	}
	if math.Abs(deltaExpected) <= 0.02 {
		return "report_formula_consistent"
	}
	if m.NetExchange != nil && math.Abs(m.Report.Net-*m.NetExchange) <= 0.05 {
		return "exchange_net_source"
	}
	if math.Abs(m.Report.Gross) > 1e-9 && m.Report.Fee > math.Abs(m.Report.Gross)*0.7 {
		return "fee_drag_outlier"
	}
	if math.Abs(deltaCalc) <= 0.05 {
		return "cluster_calc_consistent"
	}
	return "unclassified"
}

func writeClustersJSON(path, symbol string, fromTS, toTS time.Time, clusters map[string]*TradeCluster, matches []MatchResult) error {
	list := make([]TradeCluster, 0, len(clusters))
	for _, c := range clusters {
		list = append(list, *c)
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].StartTS.Before(list[j].StartTS) })
	out := ForensicsArtifacts{
		GeneratedAt: time.Now().UTC(),
		Symbol:      symbol,
		From:        fromTS,
		To:          toTS,
		Clusters:    list,
		Matches:     matches,
	}
	raw, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o644)
}

func ensureDir(path string) error {
	return os.MkdirAll(path, 0o755)
}

func resolveOutPath(outDir, name string) string {
	return filepath.Join(outDir, name)
}

func appendUnique(dst []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return dst
	}
	for _, v := range dst {
		if v == value {
			return dst
		}
	}
	return append(dst, value)
}

func nullDash(v string) string {
	if strings.TrimSpace(v) == "" {
		return "-"
	}
	return v
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func sign(v float64) int {
	const eps = 1e-9
	switch {
	case v > eps:
		return 1
	case v < -eps:
		return -1
	default:
		return 0
	}
}

func normalizeSideLabel(rep ReportTrade) string {
	if rep.PositionSide != "" {
		return strings.ToUpper(strings.TrimSpace(rep.PositionSide))
	}
	return strings.ToUpper(strings.TrimSpace(rep.CloseSide))
}
