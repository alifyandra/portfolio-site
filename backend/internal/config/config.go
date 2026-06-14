package config

import (
	"fmt"

	"github.com/caarlos0/env/v11"
)

// Config holds all runtime configuration, populated from environment variables.
// See .env.example for the full list and local defaults.
type Config struct {
	Env  string `env:"APP_ENV" envDefault:"development"`
	Port int    `env:"PORT" envDefault:"8080"`

	// Postgres
	DatabaseURL string `env:"DATABASE_URL,required"`

	// Redis (cache only — see ADR 0007)
	RedisURL string `env:"REDIS_URL" envDefault:"redis://localhost:6379/0"`

	// AutoMigrate runs Ent's schema auto-migration on startup. Convenient for a
	// single-box deploy; set false once versioned migrations are adopted.
	AutoMigrate bool `env:"AUTO_MIGRATE" envDefault:"true"`

	// CORS: comma-separated allowed origins for the frontend.
	CORSAllowedOrigins []string `env:"CORS_ALLOWED_ORIGINS" envSeparator:"," envDefault:"http://localhost:3000"`

	// TrustCloudflareIP makes the rate limiter key off CF-Connecting-IP (the real
	// visitor IP Cloudflare sets) instead of the connecting IP. Only safe once the
	// origin security group is locked to Cloudflare's ranges, otherwise the header
	// is spoofable by a direct request. Flipped on at the proxy cutover, in lock
	// step with lock_origin_to_cloudflare (see docs/security.md). Default false
	// keeps it inert until the cutover.
	TrustCloudflareIP bool `env:"TRUST_CLOUDFLARE_IP" envDefault:"false"`

	// AWS / S3 / SQS. Endpoint overrides let us point at LocalStack/MinIO/ElasticMQ locally.
	AWSRegion    string `env:"AWS_REGION" envDefault:"ap-southeast-2"`
	S3Bucket     string `env:"S3_BUCKET" envDefault:"portfolio-assets"`
	S3Endpoint   string `env:"S3_ENDPOINT_URL"`  // empty in prod (real S3); set locally for MinIO
	SQSEndpoint  string `env:"SQS_ENDPOINT_URL"` // empty in prod (real SQS); set locally for ElasticMQ
	SQSQueueURL  string `env:"SQS_QUEUE_URL"`
	S3PathStyle  bool   `env:"S3_FORCE_PATH_STYLE" envDefault:"false"` // true for MinIO

	// Spotify proxy (see CONTEXT.md). Client credentials + a long-lived refresh token.
	SpotifyClientID     string `env:"SPOTIFY_CLIENT_ID"`
	SpotifyClientSecret string `env:"SPOTIFY_CLIENT_SECRET"`
	SpotifyRefreshToken string `env:"SPOTIFY_REFRESH_TOKEN"`

	// Email (AWS SES). The sender must be a verified SES identity. ContactNotifyTo
	// is where contact-form notifications are delivered. Both blank => email
	// disabled (the contact form still stores messages).
	SESSenderEmail string `env:"SES_SENDER_EMAIL"`
	ContactNotifyTo string `env:"CONTACT_NOTIFY_TO" envDefault:"alifyandra@gmail.com"`
}

// Load reads and validates configuration from the environment.
func Load() (*Config, error) {
	cfg, err := env.ParseAs[Config]()
	if err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}
	return &cfg, nil
}

// IsProduction reports whether we are running in the production environment.
func (c *Config) IsProduction() bool { return c.Env == "production" }
