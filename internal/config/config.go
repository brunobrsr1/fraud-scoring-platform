package config

import (
	"fmt"
	"net"
	"os"
	"strconv"
)

const (
	defaultHTTPAddr  = ":8080"
	defaultModelPath = "models/v1.0.0/model.json"
)

type Config struct {
	HTTPAddr  string
	ModelPath string
}

func Load() (Config, error) {
	addr := os.Getenv("HTTP_ADDR")
	if addr == "" {
		addr = defaultHTTPAddr
	}

	if err := validateTCPAddr(addr); err != nil {
		return Config{}, fmt.Errorf(
			"config: invalid HTTP_ADDR: %q: %v", addr, err,
		)
	}

	modelPath := os.Getenv("MODEL_PATH")
	if modelPath == "" {
		modelPath = defaultModelPath
	}

	return Config{
		HTTPAddr:  addr,
		ModelPath: modelPath,
	}, nil

}

func validateTCPAddr(addr string) error {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("expected host:port: %w", err)
	}

	p, err := strconv.Atoi(port)
	if err != nil {
		return fmt.Errorf("invalid port: %w", err)
	}

	if p < 0 || p > 65535 {
		return fmt.Errorf("port %d outside valid range [0, 65535]", p)
	}

	return nil
}
