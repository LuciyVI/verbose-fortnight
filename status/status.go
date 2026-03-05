package status

import (
	"encoding/json"
	"errors"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"time"

	"verbose-fortnight/config"
	"verbose-fortnight/logging"
	"verbose-fortnight/models"
)

const StatusSchemaVersion = 2

type BuildInfo struct {
	Commit    string
	BuildTime string
}

var (
	buildInfoMu sync.RWMutex
	buildInfo   = BuildInfo{Commit: "dev", BuildTime: "unknown"}
)

func SetBuildInfo(info BuildInfo) {
	buildInfoMu.Lock()
	buildInfo = info
	buildInfoMu.Unlock()
}

func currentBuildInfoMap() map[string]any {
	buildInfoMu.RLock()
	info := buildInfo
	buildInfoMu.RUnlock()
	commit := strings.TrimSpace(info.Commit)
	if commit == "" {
		commit = "dev"
	}
	buildTime := strings.TrimSpace(info.BuildTime)
	if buildTime == "" {
		buildTime = "unknown"
	}
	return map[string]any{
		"commit":     commit,
		"build_time": buildTime,
		"go":         runtime.Version(),
		// Backward-compatible aliases.
		"buildTime": buildTime,
		"goVersion": runtime.Version(),
	}
}

type statusResponse struct {
	StatusSchemaVersion int                       `json:"statusSchemaVersion"`
	Time                time.Time                 `json:"time"`
	Symbol              string                    `json:"symbol"`
	Trend               string                    `json:"trend"`
	RegimeStreak        int                       `json:"regimeStreak,omitempty"`
	CandleSeq           uint64                    `json:"candleSeq,omitempty"`
	Signal              *models.SignalSnapshot    `json:"signal,omitempty"`
	Indicators          *models.IndicatorSnapshot `json:"indicators,omitempty"`
	Position            *models.PositionSnapshot  `json:"position,omitempty"`
	Counters            models.RuntimeCounters    `json:"counters"`
	Health              models.RuntimeHealth      `json:"health"`
	Features            models.RuntimeFeatures    `json:"features"`
	BuildInfo           map[string]any            `json:"buildInfo"`
}

type configResponse struct {
	Time     time.Time              `json:"time"`
	Config   map[string]any         `json:"config"`
	Features models.RuntimeFeatures `json:"features"`
	Build    map[string]any         `json:"build"`
}

func buildStatusResponse(cfg *config.Config, state *models.State) statusResponse {
	state.StatusLock.RLock()
	lastSignal := state.LastSignal
	lastIndicators := state.LastIndicators
	lastPosition := state.LastPosition
	state.StatusLock.RUnlock()

	var signal *models.SignalSnapshot
	if !lastSignal.Time.IsZero() {
		sigCopy := lastSignal
		if len(sigCopy.Contribs) > 0 {
			sigCopy.Contribs = append([]string(nil), sigCopy.Contribs...)
		}
		signal = &sigCopy
	}

	var indicators *models.IndicatorSnapshot
	if !lastIndicators.Time.IsZero() {
		indCopy := lastIndicators
		indicators = &indCopy
	}

	var position *models.PositionSnapshot
	if !lastPosition.UpdatedAt.IsZero() {
		posCopy := lastPosition
		position = &posCopy
	}
	counters, health := state.RuntimeSnapshot()
	features := state.RuntimeFeaturesSnapshot()

	resp := statusResponse{
		StatusSchemaVersion: StatusSchemaVersion,
		Time:                time.Now(),
		Symbol:              cfg.Symbol,
		Trend:               state.MarketRegime,
		RegimeStreak:        state.RegimeStreak,
		CandleSeq:           state.CandleSeq.Load(),
		Signal:              signal,
		Indicators:          indicators,
		Position:            position,
		Counters:            counters,
		Health:              health,
		Features:            features,
		BuildInfo:           currentBuildInfoMap(),
	}
	return resp
}

func buildConfigResponse(cfg *config.Config, state *models.State) configResponse {
	features := models.RuntimeFeatures{}
	if state != nil {
		features = state.RuntimeFeaturesSnapshot()
	}
	return configResponse{
		Time:     time.Now().UTC(),
		Config:   config.RedactedMap(cfg),
		Features: features,
		Build:    currentBuildInfoMap(),
	}
}

func writeJSON(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(payload); err != nil {
		http.Error(w, "failed to encode response", http.StatusInternalServerError)
		return
	}
}

func buildStatusMux(cfg *config.Config, state *models.State) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		resp := buildStatusResponse(cfg, state)
		writeJSON(w, resp)
	})
	if cfg != nil && cfg.EnableConfigEndpoint {
		mux.HandleFunc("/config", func(w http.ResponseWriter, r *http.Request) {
			resp := buildConfigResponse(cfg, state)
			writeJSON(w, resp)
		})
	}
	return mux
}

// ShouldStartServer returns true when status server is configured to attempt startup.
func ShouldStartServer(cfg *config.Config) bool {
	if cfg == nil {
		return false
	}
	if !cfg.EnableStatusServer {
		return false
	}
	addr := strings.TrimSpace(cfg.StatusAddr)
	if addr == "" || strings.EqualFold(addr, "off") || strings.EqualFold(addr, "disabled") {
		return false
	}
	return true
}

// StartServer starts a local HTTP status server for diagnostics.
func StartServer(cfg *config.Config, state *models.State, logger logging.LoggerInterface) *http.Server {
	if cfg == nil {
		logger.Info("Status server disabled: nil config")
		return nil
	}
	if !cfg.EnableStatusServer {
		logger.Info("Status server disabled by ENABLE_STATUS_SERVER=0")
		return nil
	}
	if !ShouldStartServer(cfg) {
		logger.Info("Status server disabled")
		return nil
	}
	addr := strings.TrimSpace(cfg.StatusAddr)

	mux := buildStatusMux(cfg, state)

	server := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		if state != nil {
			state.RecordStatusServerStarted(time.Now().UTC())
		}
		logger.Info("Status server listening on %s", addr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			if state != nil {
				state.RecordStatusServerError(err.Error(), time.Now().UTC())
			}
			logger.Warning("Status server degraded (continuing without /status): %v", err)
		}
	}()

	return server
}
