# Cloudflare edge rate limit on the contact endpoint, layered on top of the
# app's per-IP httprate limiter (internal/server). The free plan allows a single
# rate limiting rule with period and mitigation timeout fixed at 10s and per-colo
# counting, so the rule keys on (colo, client IP). This catches POST bursts at
# the edge before they ever reach the origin.
resource "cloudflare_ruleset" "contact_ratelimit" {
  zone_id     = var.cloudflare_zone_id
  name        = "Contact form rate limit"
  description = "Block POST bursts on the contact endpoint"
  kind        = "zone"
  phase       = "http_ratelimit"

  rules {
    ref         = "contact_post_ratelimit"
    description = "Limit POST ${local.api_fqdn}/api/contact per client IP"
    action      = "block"
    expression  = "(http.host eq \"${local.api_fqdn}\" and http.request.method eq \"POST\" and http.request.uri.path eq \"/api/contact\")"

    ratelimit {
      characteristics     = ["cf.colo.id", "ip.src"]
      period              = 10
      requests_per_period = 5
      mitigation_timeout  = 10
    }
  }
}
