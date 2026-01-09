package status

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"verbose-fortnight/config"
	"verbose-fortnight/logging"
	"verbose-fortnight/models"
)

type statusResponse struct {
	Time         time.Time                 `json:"time"`
	Symbol       string                    `json:"symbol"`
	Trend        string                    `json:"trend"`
	RegimeStreak int                       `json:"regimeStreak,omitempty"`
	CandleSeq    uint64                    `json:"candleSeq,omitempty"`
	Signal       *models.SignalSnapshot    `json:"signal,omitempty"`
	Indicators   *models.IndicatorSnapshot `json:"indicators,omitempty"`
	Position     *models.PositionSnapshot  `json:"position,omitempty"`
}

// StartServer starts a local HTTP status server for diagnostics.
func StartServer(cfg *config.Config, state *models.State, logger logging.LoggerInterface) *http.Server {
	addr := strings.TrimSpace(cfg.StatusAddr)
	if addr == "" || strings.EqualFold(addr, "off") || strings.EqualFold(addr, "disabled") {
		logger.Info("Status server disabled")
		return nil
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
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

		resp := statusResponse{
			Time:         time.Now(),
			Symbol:       cfg.Symbol,
			Trend:        state.MarketRegime,
			RegimeStreak: state.RegimeStreak,
			CandleSeq:    state.CandleSeq.Load(),
			Signal:       signal,
			Indicators:   indicators,
			Position:     position,
		}

		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		if err := enc.Encode(resp); err != nil {
			http.Error(w, "failed to encode status", http.StatusInternalServerError)
			return
		}
	})

	server := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		logger.Info("Status server listening on %s", addr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("Status server error: %v", err)
		}
	}()

	return server
}
