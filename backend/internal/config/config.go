package config

import (
	"fmt"
	"strings"

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
	AWSRegion   string `env:"AWS_REGION" envDefault:"ap-southeast-2"`
	S3Bucket    string `env:"S3_BUCKET" envDefault:"portfolio-assets"`
	S3Endpoint  string `env:"S3_ENDPOINT_URL"`  // empty in prod (real S3); set locally for MinIO
	SQSEndpoint string `env:"SQS_ENDPOINT_URL"` // empty in prod (real SQS); set locally for ElasticMQ
	SQSQueueURL string `env:"SQS_QUEUE_URL"`
	S3PathStyle bool   `env:"S3_FORCE_PATH_STYLE" envDefault:"false"` // true for MinIO

	// Spotify proxy (see CONTEXT.md). Client credentials + a long-lived refresh token.
	SpotifyClientID     string `env:"SPOTIFY_CLIENT_ID"`
	SpotifyClientSecret string `env:"SPOTIFY_CLIENT_SECRET"`
	SpotifyRefreshToken string `env:"SPOTIFY_REFRESH_TOKEN"`

	// Email (AWS SES). The sender must be a verified SES identity. ContactNotifyTo
	// is where contact-form notifications are delivered. Both blank => email
	// disabled (the contact form still stores messages).
	SESSenderEmail  string `env:"SES_SENDER_EMAIL"`
	ContactNotifyTo string `env:"CONTACT_NOTIFY_TO" envDefault:"alifyandra@gmail.com"`

	// Auth / Google OAuth (see ADR 10). Blank client id/secret => auth disabled:
	// the app still boots and the auth endpoints report "not configured".
	GoogleClientID     string `env:"GOOGLE_CLIENT_ID"`
	GoogleClientSecret string `env:"GOOGLE_CLIENT_SECRET"`
	// GoogleRedirectURL is the backend callback Google returns to. It must match a
	// redirect URI registered on the Google OAuth client.
	GoogleRedirectURL string `env:"GOOGLE_REDIRECT_URL" envDefault:"http://localhost:8080/api/auth/google/callback"`
	// AdminEmails are the verified Google emails granted the admin role. Matched on
	// every login so the role self-heals and a fresh prod DB needs no manual step.
	AdminEmails []string `env:"ADMIN_EMAILS" envSeparator:","`
	// FriendEmails are the verified Google emails granted the friend role: the tier
	// between member and admin with access to friends-only tools (e.g. the WhatsApp
	// sender). Matched on every login like AdminEmails so the role self-heals; admin
	// takes precedence over friend. See ADR 10.
	FriendEmails []string `env:"FRIEND_EMAILS" envSeparator:","`
	// SessionCookieDomain scopes the session cookie. Set to ".aliflabs.dev" in prod
	// so it is shared across the apex and the api subdomain; blank in local dev
	// yields a host-only cookie.
	SessionCookieDomain string `env:"SESSION_COOKIE_DOMAIN"`
	// FrontendURL is where the OAuth callback redirects the browser after sign-in.
	FrontendURL string `env:"FRONTEND_URL" envDefault:"http://localhost:3000"`

	// WhatsApp Sender sidecar (see ADR 11). The sidecar runs whatsapp-web.js +
	// Chromium off-box; the backend dials it over a shared secret. A blank URL
	// disables the send path only: template and recipient-list CRUD keep working,
	// and POST /api/wa/batches reports "not configured". The secret is required
	// whenever the URL is set (validated in Load).
	WaSidecarURL    string `env:"WA_SIDECAR_URL"`
	WaSidecarSecret string `env:"WA_SIDECAR_SECRET"`
	// WhatsApp send caps (ADR 11), tunable so a number can be ramped conservatively
	// and raised as it proves stable. Defaults match the ADR: 250 recipients per
	// batch, 3 sent-batches per rolling 24h.
	WaMaxBatchRecipients int `env:"WA_MAX_BATCH_RECIPIENTS" envDefault:"250"`
	WaMaxBatchesPerDay   int `env:"WA_MAX_BATCHES_PER_DAY" envDefault:"3"`

	// WhatsApp sidecar launch mode (ADR 11 Fargate amendment). "static" (default,
	// local dev) dials the fixed WaSidecarURL for every batch. "fargate" launches a
	// per-batch ECS Fargate task, streams to its private IP, then stops it — nothing
	// runs (or bills) while idle. The fargate identifiers below are required and
	// validated in Load only when the mode is "fargate"; they are prod/SSM-managed.
	WaSidecarMode string `env:"WA_SIDECAR_MODE" envDefault:"static"`
	// WaEcsCluster is the ECS cluster the sidecar task runs in.
	WaEcsCluster string `env:"WA_ECS_CLUSTER"`
	// WaTaskDefinition is the task-definition family; ECS resolves the latest ACTIVE
	// revision at RunTask time, so a redeploy needs no config change.
	WaTaskDefinition string `env:"WA_TASK_DEFINITION"`
	// WaSubnetIDs are the (public) subnets the task's ENI is placed in.
	WaSubnetIDs []string `env:"WA_SUBNET_IDS" envSeparator:","`
	// WaSecurityGroupID is the security group attached to the task's ENI (open to
	// the backend host on the sidecar port).
	WaSecurityGroupID string `env:"WA_SECURITY_GROUP_ID"`
	// WaAssignPublicIP must be true when the task runs in a public subnet with no NAT
	// gateway: the ECS agent needs a public IP to pull the image from ECR and read
	// the secret from SSM, or the task never starts.
	WaAssignPublicIP bool `env:"WA_ASSIGN_PUBLIC_IP" envDefault:"true"`
	// WaSidecarPort is the port the sidecar's HTTP server listens on (both the
	// readiness /healthz probe and the /sessions stream). The task's private IP is
	// resolved at launch; this is the port appended to it.
	WaSidecarPort int `env:"WA_SIDECAR_PORT" envDefault:"8081"`

	// Digest (see ADR 0013). The scheduled digest.build Job fetches public Sources
	// and summarizes them with the Anthropic Messages API into a dated Digest. A
	// blank ANTHROPIC_API_KEY disables the summary: the builder degrades gracefully
	// (Configured() is false) and the Job is acked with nothing written, mirroring
	// the Spotify/SES no-op-without-creds pattern.
	AnthropicAPIKey string `env:"ANTHROPIC_API_KEY"`
	// DigestModel is the Anthropic model id used for the summary. A small model
	// bounds cost (ADR 0013): one call per run, capped by DigestMaxTokens.
	DigestModel string `env:"DIGEST_MODEL" envDefault:"claude-haiku-4-5"`
	// DigestMaxTokens caps the summary length, and so the per-run cost.
	DigestMaxTokens int `env:"DIGEST_MAX_TOKENS" envDefault:"4096"`

	// DigestMode selects how the worker runs digest.build. "local" (default, local
	// dev) runs the builder inline in the worker process — no AWS needed. "fargate"
	// (prod) launches a run-to-completion ECS Fargate task, blocks on DescribeTasks
	// until it STOPs, and acks SQS only on a clean (exit 0) run. The DIGEST_*
	// identifiers below are required and validated in Load only when the mode is
	// "fargate"; they are prod/SSM-managed.
	DigestMode string `env:"DIGEST_MODE" envDefault:"local"`
	// DigestEcsCluster is the ECS cluster the digest task runs in.
	DigestEcsCluster string `env:"DIGEST_ECS_CLUSTER"`
	// DigestTaskDefinition is the task-definition family; ECS resolves the latest
	// ACTIVE revision at RunTask time, so a redeploy needs no config change.
	DigestTaskDefinition string `env:"DIGEST_TASK_DEFINITION"`
	// DigestContainerName is the container in the task definition that runs the
	// `digest` binary; the launcher targets it for the DIGEST_DATE env override and
	// reads its exit code.
	DigestContainerName string `env:"DIGEST_CONTAINER_NAME" envDefault:"digest"`
	// DigestSubnetIDs are the subnets the task's ENI is placed in.
	DigestSubnetIDs []string `env:"DIGEST_SUBNET_IDS" envSeparator:","`
	// DigestSecurityGroupID is the security group attached to the task's ENI. It
	// only needs egress: the worker never dials the task, it polls DescribeTasks.
	DigestSecurityGroupID string `env:"DIGEST_SECURITY_GROUP_ID"`
	// DigestAssignPublicIP must be true when the task runs in a public subnet with no
	// NAT gateway: the ECS agent needs a public IP to pull the image from ECR and
	// read the Anthropic key from SSM, or the task never starts.
	DigestAssignPublicIP bool `env:"DIGEST_ASSIGN_PUBLIC_IP" envDefault:"true"`
	// DigestResultPrefix is the S3 key prefix (in S3_BUCKET) the worker writes the
	// per-run result key under. The Fargate task writes its Result JSON there and the
	// worker reads it back to persist the Digest row (ADR 0013, Shape B). Trailing
	// slash so keys concatenate cleanly.
	DigestResultPrefix string `env:"DIGEST_RESULT_PREFIX" envDefault:"digest-results/"`
}

// Load reads and validates configuration from the environment.
func Load() (*Config, error) {
	cfg, err := env.ParseAs[Config]()
	if err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}
	// Credentialed CORS (cookies) requires exact origins: a wildcard would be
	// rejected by browsers and is unsafe, so fail fast rather than silently
	// breaking the auth flow in prod. See ADR 10.
	for _, o := range cfg.CORSAllowedOrigins {
		if strings.Contains(o, "*") {
			return nil, fmt.Errorf("CORS_ALLOWED_ORIGINS must list exact origins (no wildcards) because credentialed CORS is enabled; got %q", o)
		}
	}
	// The sidecar is dialed with a bearer secret; a URL without a secret would send
	// unauthenticated requests it will reject, so fail fast rather than at runtime.
	// (In fargate mode WaSidecarURL is intentionally absent, so this stays inert.)
	if cfg.WaSidecarURL != "" && cfg.WaSidecarSecret == "" {
		return nil, fmt.Errorf("WA_SIDECAR_SECRET is required when WA_SIDECAR_URL is set")
	}
	// The launch mode selects how a batch reaches a sidecar. static keeps the fixed
	// URL (checked above); fargate launches a per-batch task and needs the ECS
	// identifiers. Fail fast, naming the missing var, rather than at RunTask time.
	switch cfg.WaSidecarMode {
	case "static":
		// nothing further: the URL/secret pair is validated above.
	case "fargate":
		if cfg.WaSidecarSecret == "" {
			return nil, fmt.Errorf("WA_SIDECAR_SECRET is required when WA_SIDECAR_MODE=fargate")
		}
		if cfg.WaEcsCluster == "" {
			return nil, fmt.Errorf("WA_ECS_CLUSTER is required when WA_SIDECAR_MODE=fargate")
		}
		if cfg.WaTaskDefinition == "" {
			return nil, fmt.Errorf("WA_TASK_DEFINITION is required when WA_SIDECAR_MODE=fargate")
		}
		if len(cfg.WaSubnetIDs) == 0 {
			return nil, fmt.Errorf("WA_SUBNET_IDS is required when WA_SIDECAR_MODE=fargate")
		}
		if cfg.WaSecurityGroupID == "" {
			return nil, fmt.Errorf("WA_SECURITY_GROUP_ID is required when WA_SIDECAR_MODE=fargate")
		}
	default:
		return nil, fmt.Errorf(`WA_SIDECAR_MODE must be "static" or "fargate", got %q`, cfg.WaSidecarMode)
	}
	// digest.build runs inline (local) or on a run-to-completion Fargate task
	// (fargate). fargate needs the ECS identifiers; fail fast naming the missing var
	// rather than at RunTask time. ANTHROPIC_API_KEY is intentionally NOT required
	// in either mode — a blank key degrades to a no-op (ADR 0013).
	switch cfg.DigestMode {
	case "local":
		// nothing further: the builder runs in-process.
	case "fargate":
		if cfg.DigestEcsCluster == "" {
			return nil, fmt.Errorf("DIGEST_ECS_CLUSTER is required when DIGEST_MODE=fargate")
		}
		if cfg.DigestTaskDefinition == "" {
			return nil, fmt.Errorf("DIGEST_TASK_DEFINITION is required when DIGEST_MODE=fargate")
		}
		if len(cfg.DigestSubnetIDs) == 0 {
			return nil, fmt.Errorf("DIGEST_SUBNET_IDS is required when DIGEST_MODE=fargate")
		}
		if cfg.DigestSecurityGroupID == "" {
			return nil, fmt.Errorf("DIGEST_SECURITY_GROUP_ID is required when DIGEST_MODE=fargate")
		}
	default:
		return nil, fmt.Errorf(`DIGEST_MODE must be "local" or "fargate", got %q`, cfg.DigestMode)
	}
	return &cfg, nil
}

// IsProduction reports whether we are running in the production environment.
func (c *Config) IsProduction() bool { return c.Env == "production" }
