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

# Frontend on Vercel. Vercel's recommended (project-specific) records: a CNAME
# at the apex and at www pointing to the project's vercel-dns target (Cloudflare
# flattens the apex CNAME), plus two _vercel TXT verification records. All
# DNS-only (grey) so Vercel terminates TLS. We keep Cloudflare as the DNS
# provider (do NOT delegate nameservers to Vercel; api + DKIM live here). Vercel
# is configured with www as primary (apex 308 -> www). The CNAME target and the
# verify tokens come from Vercel's domain config for this project.
locals {
  vercel_cname = "ab4a9c312f7397c2.vercel-dns-017.com"
}

resource "cloudflare_record" "vercel_apex" {
  zone_id = var.cloudflare_zone_id
  name    = "@"
  type    = "CNAME"
  content = local.vercel_cname
  proxied = false
  ttl     = 300
  comment = "Vercel frontend (apex)"
}

resource "cloudflare_record" "vercel_www" {
  zone_id = var.cloudflare_zone_id
  name    = "www"
  type    = "CNAME"
  content = local.vercel_cname
  proxied = false
  ttl     = 300
  comment = "Vercel frontend (www, primary)"
}

resource "cloudflare_record" "vercel_verify_apex" {
  zone_id = var.cloudflare_zone_id
  name    = "_vercel"
  type    = "TXT"
  content = "vc-domain-verify=aliflabs.dev,00eb5a4c5502c5b06bd8,dc"
  ttl     = 300
  comment = "Vercel domain verification (apex)"
}

resource "cloudflare_record" "vercel_verify_www" {
  zone_id = var.cloudflare_zone_id
  name    = "_vercel"
  type    = "TXT"
  content = "vc-domain-verify=www.aliflabs.dev,dd4e31fd61d4082fbb2b,dc"
  ttl     = 300
  comment = "Vercel domain verification (www)"
}
