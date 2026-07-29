package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"
	"time"
)

// agentConfig — итоговая конфигурация агента.
type agentConfig struct {
	Addr           string
	PollInterval   time.Duration
	ReportInterval time.Duration
	HashKey        string
	RateLimit      int
	CryptoKey      string
	GRPCAddress    string
	Transport      string // "http" (по умолчанию) | "grpc"
}

// agentJSONConfig — представление конфигурации из JSON-файла.
// Интервалы задаются строкой в формате time.Duration ("1s", "500ms", "1m30s").
type agentJSONConfig struct {
	Address        string `json:"address"`
	ReportInterval string `json:"report_interval"`
	PollInterval   string `json:"poll_interval"`
	CryptoKey      string `json:"crypto_key"`
	GRPCAddress    string `json:"grpc_address"`
	Transport      string `json:"transport"`
}

// loadAgentJSON читает и парсит JSON-файл конфигурации агента.
func loadAgentJSON(path string) (*agentJSONConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("чтение config-файла: %w", err)
	}
	var cfg agentJSONConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("разбор config-файла: %w", err)
	}
	return &cfg, nil
}

// parseConfig собирает конфигурацию агента с учётом приоритетов:
// env > флаг > значение из JSON-файла > дефолт. Файл конфигурации
// указывается через -c/-config или переменную окружения CONFIG.
//
// Интервалы хранятся как time.Duration; из JSON поддерживается полный
// формат time.ParseDuration ("500ms", "1m30s"), из флагов и env — целые
// секунды (обратная совместимость).
func parseConfig() (agentConfig, error) {
	addr := flag.String("a", "localhost:8080", "адрес сервера (host:port)")
	pollInterval := flag.Int("p", 2, "интервал сбора метрик (сек)")
	reportInterval := flag.Int("r", 10, "интервал отправки метрик на сервер (сек)")
	hashKey := flag.String("k", "", "ключ для подписи SHA256")
	rateLimit := flag.Int("l", 1, "количество одновременных запросов")
	cryptoKey := flag.String("crypto-key", "", "путь до файла с публичным RSA-ключом (пусто — шифрование отключено)")
	grpcAddress := flag.String("grpc-address", "", "адрес gRPC-сервера метрик (используется при -transport=grpc)")
	transport := flag.String("transport", "http", "транспорт отправки метрик: http | grpc")
	configPath := flag.String("c", "", "путь до JSON-файла конфигурации")
	configPathLong := flag.String("config", "", "путь до JSON-файла конфигурации (алиас -c)")

	flag.Parse()

	// Определяем какие флаги были указаны явно — только их значения
	// имеют приоритет над JSON-файлом.
	setFlags := map[string]bool{}
	flag.Visit(func(f *flag.Flag) { setFlags[f.Name] = true })

	// Итоговые длительности: сначала берём дефолты флагов, дальше
	// последовательно поднимаем приоритеты (JSON → флаг → env).
	// Хранение в time.Duration сохраняет полную точность JSON-формата.
	pollDur := time.Duration(*pollInterval) * time.Second
	reportDur := time.Duration(*reportInterval) * time.Second

	// Путь до JSON-файла: -c или -config; env CONFIG перекрывает флаг.
	cfgPath := *configPath
	if cfgPath == "" {
		cfgPath = *configPathLong
	}
	if v, ok := os.LookupEnv("CONFIG"); ok {
		cfgPath = v
	}

	// JSON-файл имеет самый низкий приоритет: заполняет только те поля,
	// которые не заданы флагом и не заданы через env.
	if cfgPath != "" {
		jsonCfg, err := loadAgentJSON(cfgPath)
		if err != nil {
			return agentConfig{}, err
		}
		if !setFlags["a"] && jsonCfg.Address != "" {
			*addr = jsonCfg.Address
		}
		if !setFlags["p"] && jsonCfg.PollInterval != "" {
			d, err := time.ParseDuration(jsonCfg.PollInterval)
			if err != nil {
				return agentConfig{}, fmt.Errorf("config poll_interval: %w", err)
			}
			pollDur = d
		}
		if !setFlags["r"] && jsonCfg.ReportInterval != "" {
			d, err := time.ParseDuration(jsonCfg.ReportInterval)
			if err != nil {
				return agentConfig{}, fmt.Errorf("config report_interval: %w", err)
			}
			reportDur = d
		}
		if !setFlags["crypto-key"] && jsonCfg.CryptoKey != "" {
			*cryptoKey = jsonCfg.CryptoKey
		}
		if !setFlags["grpc-address"] && jsonCfg.GRPCAddress != "" {
			*grpcAddress = jsonCfg.GRPCAddress
		}
		if !setFlags["transport"] && jsonCfg.Transport != "" {
			*transport = jsonCfg.Transport
		}
	}

	// Флаг переопределяет JSON, но если явно указан.
	if setFlags["p"] {
		pollDur = time.Duration(*pollInterval) * time.Second
	}
	if setFlags["r"] {
		reportDur = time.Duration(*reportInterval) * time.Second
	}

	// Env-переменные имеют самый высокий приоритет.
	if v, ok := os.LookupEnv("ADDRESS"); ok {
		*addr = v
	}
	if v, ok := os.LookupEnv("REPORT_INTERVAL"); ok {
		sec, err := strconv.Atoi(v)
		if err != nil {
			return agentConfig{}, fmt.Errorf("неверное значение REPORT_INTERVAL: %w", err)
		}
		reportDur = time.Duration(sec) * time.Second
	}
	if v, ok := os.LookupEnv("POLL_INTERVAL"); ok {
		sec, err := strconv.Atoi(v)
		if err != nil {
			return agentConfig{}, fmt.Errorf("неверное значение POLL_INTERVAL: %w", err)
		}
		pollDur = time.Duration(sec) * time.Second
	}
	if v, ok := os.LookupEnv("RATE_LIMIT"); ok {
		rl, err := strconv.Atoi(v)
		if err != nil {
			return agentConfig{}, fmt.Errorf("неверное значение RATE_LIMIT: %w", err)
		}
		*rateLimit = rl
	}
	if v, ok := os.LookupEnv("KEY"); ok {
		*hashKey = v
	}
	if v, ok := os.LookupEnv("CRYPTO_KEY"); ok {
		*cryptoKey = v
	}
	if v, ok := os.LookupEnv("GRPC_ADDRESS"); ok {
		*grpcAddress = v
	}
	if v, ok := os.LookupEnv("TRANSPORT"); ok {
		*transport = v
	}

	return agentConfig{
		Addr:           *addr,
		PollInterval:   pollDur,
		ReportInterval: reportDur,
		HashKey:        *hashKey,
		RateLimit:      *rateLimit,
		CryptoKey:      *cryptoKey,
		GRPCAddress:    *grpcAddress,
		Transport:      *transport,
	}, nil
}
