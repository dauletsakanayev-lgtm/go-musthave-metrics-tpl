package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"
	"time"
)

// serverConfig — итоговая конфигурация сервера.
type serverConfig struct {
	Addr          string
	LogLevel      string
	StoreInterval time.Duration
	FilePath      string
	Restore       bool
	DatabaseDSN   string
	HashKey       string
	AuditFile     string
	AuditURL      string
	EnablePprof   bool
	CryptoKey     string
}

// serverJSONConfig — представление конфигурации сервера из JSON-файла.
// store_interval задаётся строкой в формате time.Duration ("1s", "500ms").
// Restore — указатель, чтобы отличить "не задано в файле" от false.
type serverJSONConfig struct {
	Address       string `json:"address"`
	Restore       *bool  `json:"restore"`
	StoreInterval string `json:"store_interval"`
	StoreFile     string `json:"store_file"`
	DatabaseDSN   string `json:"database_dsn"`
	CryptoKey     string `json:"crypto_key"`
}

// loadServerJSON читает и парсит JSON-файл конфигурации сервера.
func loadServerJSON(path string) (*serverJSONConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("чтение config-файла: %w", err)
	}
	var cfg serverJSONConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("разбор config-файла: %w", err)
	}
	return &cfg, nil
}

// parseConfig собирает конфигурацию сервера с учётом приоритетов:
// env > флаг > значение из JSON-файла > дефолт. Файл конфигурации
// указывается через -c/-config или переменную окружения CONFIG.
//
// StoreInterval хранится как time.Duration; из JSON поддерживается полный
// формат time.ParseDuration ("500ms", "1m30s"), из флагов и env — целые
// секунды (обратная совместимость).
func parseConfig() (serverConfig, error) {
	addr := flag.String("a", ":8080", "адрес сервера")
	logLevel := flag.String("l", "info", "уровень логирования")
	storeInterval := flag.Int("i", 300, "интервал сохранения на диск (сек)")
	filePath := flag.String("f", "metrics.json", "путь до файла")
	restore := flag.Bool("r", true, "загружать при старте")
	databaseDSN := flag.String("d", "", "строка подключения к PostgreSQL")
	hashKey := flag.String("k", "", "ключ для подписи SHA256")
	auditFile := flag.String("audit-file", "", "путь к файлу логов аудита (пусто — аудит в файл отключён)")
	auditURL := flag.String("audit-url", "", "URL приёмника логов аудита (пусто — аудит по сети отключён)")
	enablePprof := flag.Bool("pprof", false, "включить эндпоинты /debug/pprof (только для dev/staging)")
	cryptoKey := flag.String("crypto-key", "", "путь до файла с приватным RSA-ключом (пусто — расшифровка отключена)")
	configPath := flag.String("c", "", "путь до JSON-файла конфигурации")
	configPathLong := flag.String("config", "", "путь до JSON-файла конфигурации (алиас -c)")
	flag.Parse()

	// Явно указанные флаги имеют приоритет над JSON-файлом.
	setFlags := map[string]bool{}
	flag.Visit(func(f *flag.Flag) { setFlags[f.Name] = true })

	// Итоговый интервал сохранения: дефолт флага; JSON может подставить
	// значение полной точности (например, "500ms"); флаг/env затем
	// переопределяют по приоритету.
	storeDur := time.Duration(*storeInterval) * time.Second

	// Путь до JSON-файла: -c или -config; env CONFIG перекрывает флаг.
	cfgPath := *configPath
	if cfgPath == "" {
		cfgPath = *configPathLong
	}
	if v, ok := os.LookupEnv("CONFIG"); ok {
		cfgPath = v
	}

	// JSON-файл — самый низкий приоритет: заполняет только поля,
	// не заданные ни флагом, ни (позже) env.
	if cfgPath != "" {
		jsonCfg, err := loadServerJSON(cfgPath)
		if err != nil {
			return serverConfig{}, err
		}
		if !setFlags["a"] && jsonCfg.Address != "" {
			*addr = jsonCfg.Address
		}
		if !setFlags["r"] && jsonCfg.Restore != nil {
			*restore = *jsonCfg.Restore
		}
		if !setFlags["i"] && jsonCfg.StoreInterval != "" {
			d, err := time.ParseDuration(jsonCfg.StoreInterval)
			if err != nil {
				return serverConfig{}, fmt.Errorf("config store_interval: %w", err)
			}
			storeDur = d
		}
		if !setFlags["f"] && jsonCfg.StoreFile != "" {
			*filePath = jsonCfg.StoreFile
		}
		if !setFlags["d"] && jsonCfg.DatabaseDSN != "" {
			*databaseDSN = jsonCfg.DatabaseDSN
		}
		if !setFlags["crypto-key"] && jsonCfg.CryptoKey != "" {
			*cryptoKey = jsonCfg.CryptoKey
		}
	}

	// Флаг переопределяет JSON, но если явно указан.
	if setFlags["i"] {
		storeDur = time.Duration(*storeInterval) * time.Second
	}

	// Env-переменные — самый высокий приоритет.
	if v, ok := os.LookupEnv("ADDRESS"); ok {
		*addr = v
	}
	if v, ok := os.LookupEnv("STORE_INTERVAL"); ok {
		sec, err := strconv.Atoi(v)
		if err != nil {
			return serverConfig{}, fmt.Errorf("неверное значение STORE_INTERVAL: %w", err)
		}
		storeDur = time.Duration(sec) * time.Second
	}
	if v, ok := os.LookupEnv("FILE_STORAGE_PATH"); ok {
		*filePath = v
	}
	if v, ok := os.LookupEnv("RESTORE"); ok {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return serverConfig{}, fmt.Errorf("неверное значение RESTORE: %w", err)
		}
		*restore = b
	}
	if v, ok := os.LookupEnv("DATABASE_DSN"); ok {
		*databaseDSN = v
	}
	if v, ok := os.LookupEnv("KEY"); ok {
		*hashKey = v
	}
	if v, ok := os.LookupEnv("AUDIT_FILE"); ok {
		*auditFile = v
	}
	if v, ok := os.LookupEnv("AUDIT_URL"); ok {
		*auditURL = v
	}
	if v, ok := os.LookupEnv("PPROF"); ok {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return serverConfig{}, fmt.Errorf("неверное значение PPROF: %w", err)
		}
		*enablePprof = b
	}
	if v, ok := os.LookupEnv("CRYPTO_KEY"); ok {
		*cryptoKey = v
	}

	return serverConfig{
		Addr:          *addr,
		LogLevel:      *logLevel,
		StoreInterval: storeDur,
		FilePath:      *filePath,
		Restore:       *restore,
		DatabaseDSN:   *databaseDSN,
		HashKey:       *hashKey,
		AuditFile:     *auditFile,
		AuditURL:      *auditURL,
		EnablePprof:   *enablePprof,
		CryptoKey:     *cryptoKey,
	}, nil
}
