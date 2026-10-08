package config

import (
	"fmt"
	"strconv"
	"time"
)

func (c *Config) loadMetrics() error {
	var err error
	c.MetricsEnabled, err = strconv.ParseBool(getEnv("METRICS_ENABLED", "true"))
	if err != nil {
		return fmt.Errorf("invalid METRICS_ENABLED")
	}
	c.MetricsCollectInterval, err = durationEnv("METRICS_COLLECT_INTERVAL", 15*time.Second)
	if err != nil {
		return err
	}
	c.MetricsCollectTimeout, err = durationEnv("METRICS_COLLECT_TIMEOUT", 2*time.Second)
	return err
}

func (c Config) validateMetrics() error {
	if c.MetricsEnabled && (c.MetricsCollectInterval < time.Second || c.MetricsCollectTimeout <= 0 || c.MetricsCollectTimeout >= c.MetricsCollectInterval) {
		return fmt.Errorf("metrics interval must be >= 1s and exceed a positive collection timeout")
	}
	return nil
}
