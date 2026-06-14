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

# Frontend on Vercel. Apex uses Vercel's documented A record; www is a CNAME to
# Vercel. Both DNS-only (grey) so Vercel terminates TLS. We keep Cloudflare as
# the DNS provider (do NOT delegate nameservers to Vercel; the api + DKIM
# records live here). Vercel is configured with www as primary (apex 308 -> www).
resource "cloudflare_record" "vercel_apex" {
  zone_id = var.cloudflare_zone_id
  name    = "@"
  type    = "A"
  content = "76.76.21.21"
  proxied = false
  ttl     = 300
  comment = "Vercel frontend (apex)"
}

resource "cloudflare_record" "vercel_www" {
  zone_id = var.cloudflare_zone_id
  name    = "www"
  type    = "CNAME"
  content = "cname.vercel-dns.com"
  proxied = false
  ttl     = 300
  comment = "Vercel frontend (www, primary)"
}
