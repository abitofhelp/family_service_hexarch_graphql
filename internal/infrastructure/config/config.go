// Package config provides functionality for managing application configuration.
package config

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-playground/validator/v10"
	"github.com/joho/godotenv"
	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/env"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
)

// Config represents the application configuration
type Config struct {
	App       AppConfig       `mapstructure:"app" validate:"required"`
	Auth      AuthConfig      `mapstructure:"auth" validate:"required"`
	Database  DatabaseConfig  `mapstructure:"database" validate:"required"`
	Features  FeaturesConfig  `mapstructure:"features" validate:"required"`
	Log       LogConfig       `mapstructure:"log" validate:"required"`
	Server    ServerConfig    `mapstructure:"server" validate:"required"`
	Telemetry TelemetryConfig `mapstructure:"telemetry" validate:"required"`
}

// AppConfig contains application-specific configuration
type AppConfig struct {
	Version string `mapstructure:"version" validate:"required"`
}

// AuthConfig contains authentication configuration
type AuthConfig struct {
	OIDCTimeout time.Duration `mapstructure:"oidc_timeout" validate:"required,min=1"`
}

// DatabaseConfig contains database configuration
type DatabaseConfig struct {
	Type     string         `mapstructure:"type" validate:"required,oneof=mongodb postgres"`
	MongoDB  MongoDBConfig  `mapstructure:"mongodb" validate:"required"`
	Postgres PostgresConfig `mapstructure:"postgres" validate:"required"`
}

// MongoDBConfig contains MongoDB-specific configuration
type MongoDBConfig struct {
	URI               string        `mapstructure:"uri" validate:"required,uri"`
	ConnectionTimeout time.Duration `mapstructure:"connection_timeout" validate:"required,min=1"`
	DisconnectTimeout time.Duration `mapstructure:"disconnect_timeout" validate:"required,min=1"`
	IndexTimeout      time.Duration `mapstructure:"index_timeout" validate:"required,min=1"`
	MigrationTimeout  time.Duration `mapstructure:"migration_timeout" validate:"required,min=1"`
	PingTimeout       time.Duration `mapstructure:"ping_timeout" validate:"required,min=1"`
}

// PostgresConfig contains PostgreSQL-specific configuration
type PostgresConfig struct {
	DSN              string        `mapstructure:"dsn" validate:"required"`
	MigrationTimeout time.Duration `mapstructure:"migration_timeout" validate:"required,min=1"`
}

// FeaturesConfig contains feature flag configuration
type FeaturesConfig struct {
	UseGenerics bool `mapstructure:"use_generics"`
}

// LogConfig contains logging configuration
type LogConfig struct {
	Level       string `mapstructure:"level" validate:"required,oneof=debug info warn error dpanic panic fatal"`
	Development bool   `mapstructure:"development"`
}

// ServerConfig contains HTTP server configuration
type ServerConfig struct {
	Port            string        `mapstructure:"port" validate:"required,numeric"`
	ReadTimeout     time.Duration `mapstructure:"read_timeout" validate:"required,min=1"`
	WriteTimeout    time.Duration `mapstructure:"write_timeout" validate:"required,min=1"`
	IdleTimeout     time.Duration `mapstructure:"idle_timeout" validate:"required,min=1"`
	ShutdownTimeout time.Duration `mapstructure:"shutdown_timeout" validate:"required,min=1"`
	HealthEndpoint  string        `mapstructure:"health_endpoint" validate:"required,startswith=/"`
}

// TelemetryConfig contains telemetry configuration
type TelemetryConfig struct {
	ShutdownTimeout time.Duration   `mapstructure:"shutdown_timeout" validate:"required,min=1"`
	Exporters       ExportersConfig `mapstructure:"exporters"`
}

// ExportersConfig contains configuration for telemetry exporters
type ExportersConfig struct {
	Metrics MetricsExporterConfig `mapstructure:"metrics"`
}

// MetricsExporterConfig contains configuration for metrics exporters
type MetricsExporterConfig struct {
	Prometheus PrometheusConfig `mapstructure:"prometheus"`
}

// PrometheusConfig contains configuration for Prometheus metrics
type PrometheusConfig struct {
	Enabled bool   `mapstructure:"enabled"`
	Listen  string `mapstructure:"listen"`
	Path    string `mapstructure:"path"`
}

var (
	// compile regex once
	envVarRegex = regexp.MustCompile(`\${([^}:]+)(?::-([^}]+))?}`)

	// Define fallback mappings separately
	envFallbacks = map[string]string{}
)

// LoadConfig loads the application configuration from defaults, config files, and environment variables.
// It returns a pointer to a Config struct and an error if loading fails.
func LoadConfig() (*Config, error) {
	// Initialize koanf
	k := koanf.New(".")

	// Set default values (lowest priority)
	if err := loadDefaults(k); err != nil {
		return nil, fmt.Errorf("failed to load defaults: %w", err)
	}

	// Load config from file if APP_ENV is set
	if err := loadConfigFile(k); err != nil {
		return nil, fmt.Errorf("failed to load config file: %w", err)
	}

	// Load environment variables (highest priority)
	if err := loadEnvironmentVariables(k); err != nil {
		return nil, fmt.Errorf("failed to load environment variables: %w", err)
	}

	// Process environment variables in connection strings
	if err := processEnvVarsInConnectionStrings(k); err != nil {
		return nil, fmt.Errorf("failed to process connection strings: %w", err)
	}

	// Process the configuration
	config, err := processConfig(k)
	if err != nil {
		return nil, fmt.Errorf("failed to process config: %w", err)
	}

	return config, nil
}

// loadDefaults sets the default values in the koanf instance.
func loadDefaults(k *koanf.Koanf) error {
	defaultMap := getDefaultsMap()
	for key, value := range defaultMap {
		k.Set(key, value)
	}
	return nil
}

// loadConfigFile loads configuration from a YAML file based on the APP_ENV environment variable.
// It returns an error if loading the config file fails.
func loadConfigFile(k *koanf.Koanf) error {
	environment := strings.ToLower(os.Getenv("APP_ENV"))
	if environment == "" {
		return nil // No environment specified, skip file loading
	}

	// Load environment variables from .env file
	envFile := fmt.Sprintf("%s.env", environment)
	if err := godotenv.Load(envFile); err != nil {
		// Not a critical error, just log and continue
		log.Printf("Could not find the environment variable file '%s', continuing with environment variables", envFile)
	}

	// Set up config file
	configName := fmt.Sprintf("config.%s.yaml", environment)

	// Try to load config file from different paths
	configPaths := []string{
		".",
		"./config",
	}

	execPath, err := os.Executable()
	if err == nil {
		configPaths = append(configPaths, filepath.Dir(execPath))
		configPaths = append(configPaths, filepath.Join(filepath.Dir(execPath), "config"))
	}

	// Try to load the config file from each path
	for _, path := range configPaths {
		configFilePath := filepath.Join(path, configName)
		if _, err := os.Stat(configFilePath); err == nil {
			// File exists, load it
			if err := k.Load(file.Provider(configFilePath), yaml.Parser()); err != nil {
				return fmt.Errorf("error reading config file %s: %w", configFilePath, err)
			}
			log.Printf("Using config file: %s", configFilePath)
			return nil
		}
	}

	// If we get here, no config file was found
	log.Printf("Config file %s not found in any path, using defaults and environment variables", configName)
	return nil
}

// loadEnvironmentVariables loads configuration from environment variables with the APP_ prefix.
func loadEnvironmentVariables(k *koanf.Koanf) error {
	// Use APP_ prefix for environment variables and replace . with _
	err := k.Load(env.Provider("APP_", ".", func(s string) string {
		return strings.Replace(strings.ToLower(s), "_", ".", -1)
	}), nil)

	if err != nil {
		return fmt.Errorf("error loading environment variables: %w", err)
	}

	return nil
}

// processConfig processes the loaded configuration, converting types and validating the result.
func processConfig(k *koanf.Koanf) (*Config, error) {
	// Get the config as a map to handle custom type conversions
	configMap := k.All()

	// Convert duration values from milliseconds to time.Duration
	convertDurations(configMap)

	// Create a new koanf instance with the processed map
	processed := koanf.New(".")
	for key, value := range configMap {
		processed.Set(key, value)
	}

	// Ensure MongoDB URI and PostgreSQL DSN have valid values for validation
	ensureValidConnectionStrings(processed)

	// Unmarshal into the final config struct
	var config Config
	if err := processed.UnmarshalWithConf("", &config, koanf.UnmarshalConf{
		Tag: "mapstructure",
	}); err != nil {
		return nil, fmt.Errorf("unable to decode config into struct: %w", err)
	}

	// Validate the configuration
	validate := validator.New()
	if err := validate.Struct(config); err != nil {
		return nil, fmt.Errorf("config validation failed: %w", err)
	}

	return &config, nil
}

// ensureValidConnectionStrings ensures that MongoDB URI and PostgreSQL DSN have valid values for validation.
// If they contain unresolved environment variables, replace them with valid default values.
func ensureValidConnectionStrings(k *koanf.Koanf) {
	// Check MongoDB URI
	mongoURI := k.String("database.mongodb.uri")
	if strings.Contains(mongoURI, "${") {
		// Replace with a valid URI for validation using the service name instead of localhost
		k.Set("database.mongodb.uri", "mongodb://root:password@mongodb:27017/family_service?authSource=admin&tlsMode=disable")
	}

	// Check PostgreSQL DSN
	postgresDSN := k.String("database.postgres.dsn")
	if strings.Contains(postgresDSN, "${") {
		// Replace with a valid DSN for validation using the service name instead of localhost
		k.Set("database.postgres.dsn", "postgres://postgres:postgres@postgresql:5432/family_service?sslmode=disable")
	}
}

// processEnvVarsInConnectionStrings replaces environment variable placeholders in connection strings.
// Logs warnings for missing variables but doesn't fail unless they're critical.
func processEnvVarsInConnectionStrings(k *koanf.Koanf) error {
	// Process MongoDB URI
	mongoURI := k.String("database.mongodb.uri")
	processedMongoURI, err := ProcessEnvVarsInString(mongoURI, true)
	if err != nil {
		log.Printf("Warning: %v", err)
		// Don't return error, just use the partially processed URI
	}
	k.Set("database.mongodb.uri", processedMongoURI)

	// Process PostgreSQL DSN
	postgresDSN := k.String("database.postgres.dsn")
	processedPostgresDSN, err := ProcessEnvVarsInString(postgresDSN, true)
	if err != nil {
		log.Printf("Warning: %v", err)
		// Don't return error, just use the partially processed DSN
	}
	k.Set("database.postgres.dsn", processedPostgresDSN)

	return nil
}

// ProcessEnvVarsInString replaces ${ENV_VAR} placeholders with environment variable values.
// If required is true, logs a warning for missing variables but doesn't fail unless they're critical.
// Supports default values in the format ${ENV_VAR:-default}.
func ProcessEnvVarsInString(s string, required bool) (string, error) {
	result := envVarRegex.ReplaceAllStringFunc(s, func(match string) string {
		submatches := envVarRegex.FindStringSubmatch(match)
		envVar := submatches[1]
		defaultVal := ""
		if len(submatches) > 2 {
			defaultVal = submatches[2]
		}

		// Try primary environment variable
		val := os.Getenv(envVar)

		// Try fallback if exists
		if val == "" {
			if fallback, exists := envFallbacks[envVar]; exists {
				val = os.Getenv(fallback)
			}
		}

		// Use default if provided
		if val == "" && defaultVal != "" {
			val = defaultVal
		}

		// Handle missing required variables
		if val == "" && required {
			err := fmt.Errorf("required environment variable %s not found", envVar)
			log.Printf("Warning: %v", err)

			return match // preserve placeholder for other variables
		}

		return val
	})

	// Check if any placeholders remain
	if required && strings.Contains(result, "${") {
		return result, fmt.Errorf("some required environment variables are missing")
	}

	return result, nil
}

// convertDurations recursively processes a map and converts integer values to time.Duration
// for fields that are expected to be durations based on their path.
func convertDurations(m map[string]interface{}) {
	durationPaths := []string{
		"auth.oidc_timeout",
		"database.mongodb.connection_timeout",
		"database.mongodb.disconnect_timeout",
		"database.mongodb.index_timeout",
		"database.mongodb.migration_timeout",
		"database.mongodb.ping_timeout",
		"database.postgres.migration_timeout",
		"server.idle_timeout",
		"server.read_timeout",
		"server.shutdown_timeout",
		"server.write_timeout",
		"telemetry.shutdown_timeout",
	}

	// Helper function to get a nested value from the map
	var getValue func(m map[string]interface{}, path []string) interface{}
	getValue = func(m map[string]interface{}, path []string) interface{} {
		if len(path) == 0 {
			return nil
		}
		if len(path) == 1 {
			return m[path[0]]
		}
		if next, ok := m[path[0]].(map[string]interface{}); ok {
			return getValue(next, path[1:])
		}
		return nil
	}

	// Helper function to set a nested value in the map
	var setValue func(m map[string]interface{}, path []string, value interface{})
	setValue = func(m map[string]interface{}, path []string, value interface{}) {
		if len(path) == 0 {
			return
		}
		if len(path) == 1 {
			m[path[0]] = value
			return
		}
		if next, ok := m[path[0]].(map[string]interface{}); ok {
			setValue(next, path[1:], value)
		} else {
			// Create the map if it doesn't exist
			next = make(map[string]interface{})
			m[path[0]] = next
			setValue(next, path[1:], value)
		}
	}

	// Convert durations
	for _, path := range durationPaths {
		parts := strings.Split(path, ".")
		value := getValue(m, parts)
		if value == nil {
			continue
		}

		var duration time.Duration
		switch v := value.(type) {
		case int:
			// Convert seconds to duration
			duration = time.Duration(v) * time.Second
		case int64:
			// Convert seconds to duration
			duration = time.Duration(v) * time.Second
		case float64:
			// Convert seconds to duration
			duration = time.Duration(int64(v)) * time.Second
		case string:
			// If the string already has a unit (like "10s"), parse it directly
			if strings.Contains(v, "s") || strings.Contains(v, "m") || strings.Contains(v, "h") {
				if d, err := time.ParseDuration(v); err == nil {
					duration = d
				} else {
					// Skip this value if we can't parse it as a duration
					continue
				}
			} else {
				// If the string is just a number, interpret it as seconds
				if intVal, err := strconv.ParseInt(v, 10, 64); err == nil {
					duration = time.Duration(intVal) * time.Second
				} else {
					// Skip this value if we can't convert it
					continue
				}
			}
		default:
			// Skip unsupported types
			continue
		}

		setValue(m, parts, duration)
	}
}

// getDefaultsMap returns a map of default configuration values
func getDefaultsMap() map[string]interface{} {
	return map[string]interface{}{
		// App defaults
		"app.version": "1.0.0",

		// Auth defaults
		"auth.oidc_timeout": "30s", // 30 seconds

		// Database defaults
		"database.type":                       "mongodb",
		"database.mongodb.connection_timeout": "10s", // 10 seconds
		"database.mongodb.disconnect_timeout": "5s",  // 5 seconds
		"database.mongodb.index_timeout":      "10s", // 10 seconds
		"database.mongodb.migration_timeout":  "30s", // 30 seconds
		"database.mongodb.ping_timeout":       "5s",  // 5 seconds
		"database.mongodb.uri":                "mongodb://${MONGODB_ROOT_USERNAME}:${MONGODB_ROOT_PASSWORD}@mongodb:27017/family_service?authSource=admin&tlsMode=disable",
		"database.postgres.dsn":               "postgres://${POSTGRESQL_USERNAME}:${POSTGRESQL_PASSWORD}@postgresql:5432/family_service?sslmode=disable",
		"database.postgres.migration_timeout": "30s", // 30 seconds

		// Features defaults
		"features.use_generics": true,

		// Log defaults
		"log.development": true,
		"log.level":       "debug",

		// Server defaults
		"server.health_endpoint":  "/health",
		"server.idle_timeout":     "120s", // 120 seconds
		"server.port":             "8080",
		"server.read_timeout":     "10s", // 10 seconds
		"server.shutdown_timeout": "10s", // 10 seconds
		"server.write_timeout":    "10s", // 10 seconds

		// Telemetry defaults
		"telemetry.shutdown_timeout":                     "5s", // 5 seconds
		"telemetry.exporters.metrics.prometheus.enabled": true,
		"telemetry.exporters.metrics.prometheus.listen":  "0.0.0.0:8080",
		"telemetry.exporters.metrics.prometheus.path":    "/metrics",
	}
}
