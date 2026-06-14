# Cloudflare DNS as code. The api record is DNS-only (grey cloud) so Caddy can
# run its own ACME challenge against the origin. The three DKIM CNAMEs let SES
# sign and verify the domain.

resource "cloudflare_record" "api" {
  zone_id = var.cloudflare_zone_id
  name    = var.api_subdomain
  type    = "A"
  content = aws_eip.app.public_ip
  proxied = false
  ttl     = 300
  comment = "Backend API origin (DNS-only; Caddy terminates TLS)."
}

resource "cloudflare_record" "ses_dkim" {
  count = 3

  zone_id = var.cloudflare_zone_id
  name    = "${aws_ses_domain_dkim.main.dkim_tokens[count.index]}._domainkey"
  type    = "CNAME"
  content = "${aws_ses_domain_dkim.main.dkim_tokens[count.index]}.dkim.amazonses.com"
  proxied = false
  ttl     = 300
  comment = "SES Easy DKIM"
}
