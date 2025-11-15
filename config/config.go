package config

import (
	"encoding/json"
	"os"
)

// Config holds all application configuration
type Config struct {
	Debug          bool    `json:"debug"`
	APIKey         string  `json:"api_key"`
	APISecret      string  `json:"api_secret"`
	RESTHost       string  `json:"rest_host"`
	WSPrivateURL   string  `json:"ws_private_url"`
	WSPublicURL    string  `json:"ws_public_url"`
	PongWait       int     `json:"pong_wait"`
	PingPeriod     int     `json:"ping_period"`
	RecvWindow     string  `json:"recv_window"`
	AccountType    string  `json:"account_type"`
	Symbol         string  `json:"symbol"`
	Interval       string  `json:"interval"`
	WindowSize     int     `json:"window_size"`
	BBMult         float64 `json:"bb_mult"`
	ContractSize   float64 `json:"contract_size"`
	OBDepth        int     `json:"ob_depth"`
	TpThresholdQty float64 `json:"tp_threshold_qty"`
	TpOffset       float64 `json:"tp_offset"`
	SlThresholdQty float64 `json:"sl_threshold_qty"`
	TPPercent      float64 `json:"tp_percent"`
	SlPercent      float64 `json:"sl_percent"`
	TickSize       float64 `json:"tick_size"`
}

// DefaultConfig returns a configuration with default values
func DefaultConfig() *Config {
	return &Config{
		Debug:          false,
		APIKey:         "iAk6FbPXdSri6jFU1J",
		APISecret:      "svqVf30XLzbaxmByb3qcMBBBUGN0NwXc2lSL",
		RESTHost:       "https://api-demo.bybit.com",
		WSPrivateURL:   "wss://stream-demo.bybit.com/v5/private",
		WSPublicURL:    "wss://stream.bybit.com/v5/public/linear",
		PongWait:       70,
		PingPeriod:     30,
		RecvWindow:     "5000",
		AccountType:    "UNIFIED",
		Symbol:         "BTCUSDT",
		Interval:       "1",
		WindowSize:     20,
		BBMult:         2.0,
		ContractSize:   0.001,
		OBDepth:        50,
		TpThresholdQty: 500.0,
		TpOffset:       0.002,
		SlThresholdQty: 500.0,
		TPPercent:      0.005,
		SlPercent:      0.001,
		TickSize:       0.1,
	}
}

// LoadConfigFromFile loads configuration from a JSON file
func LoadConfigFromFile(filename string) (*Config, error) {
	var config Config
	
	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	
	decoder := json.NewDecoder(file)
	err = decoder.Decode(&config)
	if err != nil {
		return nil, err
	}
	
	return &config, nil
}

// SaveConfigToFile saves configuration to a JSON file
func SaveConfigToFile(config *Config, filename string) error {
	file, err := os.Create(filename)
	if err != nil {
		return err
	}
	defer file.Close()
	
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	return encoder.Encode(config)
}